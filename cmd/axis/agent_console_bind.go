package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/console"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

// launcherBound is a consistent copy of the agent-derived launcher fields.
type launcherBound struct {
	agent       *agent.Agent
	target      ModelChoice
	run         consoleRunner
	slash       func(string) (string, error)
	safety      agent.ShellSafetyGate
	modelSwitch func(choice ModelChoice, out io.Writer) error
	skillEffect func(ctx context.Context, turn console.TurnID, command string, out io.Writer) error
	mcpAction   func(server, action string, out io.Writer)
	mcpRegistry func() *mcpclient.Registry
}

// newStartupConsoleLauncher builds a launcher whose agent arrives later via
// bind; until then every submit answers with the not-ready notice.
func newStartupConsoleLauncher(timeout time.Duration, now func() time.Time) *consoleLauncher {
	l := newConsoleLauncher(nil, timeout, now)
	l.pending = true
	return l
}

// bound copies the agent-derived fields under l.mu. ok is false before bind.
func (l *consoleLauncher) bound() (launcherBound, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return launcherBound{
		agent:       l.agent,
		target:      l.activeTarget,
		run:         l.run,
		slash:       l.slash,
		safety:      l.safety,
		modelSwitch: l.modelSwitch,
		skillEffect: l.skillEffect,
		mcpAction:   l.mcpAction,
		mcpRegistry: l.mcpRegistry,
	}, !l.pending
}

// boundAgent returns the bound agent, nil before bind.
func (l *consoleLauncher) boundAgent() *agent.Agent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.agent
}

// headerModel names the live model for the scrollback header. It runs inside
// the Bubble Tea update loop, so it only reads memory: the bound agent under
// l.mu, then the agent's model name under its own read lock.
func (l *consoleLauncher) headerModel() string {
	if a := l.boundAgent(); a != nil {
		return a.Model()
	}
	return ""
}

// notReady answers a submit that arrived before the agent was bound.
func (l *consoleLauncher) notReady(turn console.TurnID) tea.Cmd {
	return func() tea.Msg {
		if l.prog != nil {
			l.prog.Send(console.EntryMsg{Turn: turn, Entry: console.NewNoticeEntry(l.now(), consoleNotReady)})
		}
		return console.TurnDoneMsg{Turn: turn}
	}
}

// bind attaches a built agent. All agent-derived fields change under l.mu in
// one step, so a concurrent submit sees either the unbound launcher or the
// whole binding, never a mix.
func (l *consoleLauncher) bind(a *agent.Agent, mcpReg *mcpclient.Registry, target ModelChoice) {
	loader := l.loader
	if loader == nil {
		loader = loadAgentShellRuntime
	}
	timeout := l.timeout

	run := consoleRunner(func(context.Context, string, agent.Observer, io.Writer) error {
		return errors.New("agent is not available in this console")
	})
	var safety agent.ShellSafetyGate
	if a != nil {
		run = a.RunWithSinks
		safety = a.SafetyGate()
	}
	switcher := consoleModelSwitch(a, loader, target)
	modelSwitch := func(choice ModelChoice, out io.Writer) error {
		return l.commitOperatorModel(func() error { return switcher(choice, out) })
	}
	skillEffect := func(ctx context.Context, _ console.TurnID, command string, out io.Writer) error {
		if a == nil {
			return fmt.Errorf("agent is not available in this console")
		}
		ctx2, cancel := agentRequestContext(ctx, timeout)
		defer cancel()
		return a.RunWithSinks(ctx2, command, nil, out)
	}
	var mcpRegistry func() *mcpclient.Registry
	var mcpAction func(server, action string, out io.Writer)
	if mcpReg != nil {
		mcpRegistry = func() *mcpclient.Registry { return mcpReg }
		mcpAction = consoleMCPAction(mcpReg)
	}
	slash := l.guardModelSlash(consoleSlashRunner(a, mcpReg, target, loader))

	l.mu.Lock()
	defer l.mu.Unlock()
	l.agent = a
	l.activeTarget = target
	l.run = run
	l.safety = safety
	l.slash = slash
	l.modelSwitch = modelSwitch
	l.skillEffect = skillEffect
	l.mcpRegistry = mcpRegistry
	l.mcpAction = mcpAction
	l.pending = false
}

// consoleMCPAction prints a server listing for the selected /mcp action id.
func consoleMCPAction(mcpReg *mcpclient.Registry) func(server, action string, out io.Writer) {
	return func(server, action string, out io.Writer) {
		sc := mcpReg.Get(server)
		if sc == nil {
			fmt.Fprintf(out, "Server %q is no longer connected.\n", server)
			return
		}
		switch action {
		case "tools":
			slashMCPListTools(out, sc)
		case "resources":
			slashMCPListResources(out, sc)
		case "diagnostics":
			slashMCPDiagnostics(out, sc)
		default:
			fmt.Fprintf(out, "Unknown MCP action %q.\n", action)
		}
	}
}

// guardModelSlash routes `/model <name>` through the operator commit so a
// slower startup resolution can never overwrite it. Other commands pass
// straight through.
func (l *consoleLauncher) guardModelSlash(base func(string) (string, error)) func(string) (string, error) {
	return func(line string) (string, error) {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "/model") {
			return base(line)
		}
		var out string
		err := l.commitOperatorModel(func() error {
			var err error
			out, err = base(line)
			return err
		})
		return out, err
	}
}

// commitOperatorModel runs an operator model switch under l.modelMu. A
// successful switch retires any in-flight startup resolution (modelGen++) and
// marks the model ready, which also lifts a "no model" state. modelMu is held
// across routing on purpose: it is never taken by the UI loop, only by model
// switches and the startup job, and serializing those is the point.
func (l *consoleLauncher) commitOperatorModel(apply func() error) error {
	l.modelMu.Lock()
	err := apply()
	if err == nil {
		l.modelGen++
	}
	l.modelMu.Unlock()
	if err == nil && l.prog != nil {
		l.prog.Send(console.ModelStateMsg{State: console.ModelReady})
	}
	return err
}

// currentModelGen reads the model generation.
func (l *consoleLauncher) currentModelGen() uint64 {
	l.modelMu.Lock()
	defer l.modelMu.Unlock()
	return l.modelGen
}

// applyStartupModel runs apply only while no operator switch has landed since
// gen was read. The check and apply are one critical section with
// commitOperatorModel, so the later writer always wins.
func (l *consoleLauncher) applyStartupModel(gen uint64, apply func() error) (applied bool, err error) {
	l.modelMu.Lock()
	defer l.modelMu.Unlock()
	if gen != l.modelGen {
		return false, nil
	}
	if err := apply(); err != nil {
		return false, err
	}
	return true, nil
}

// startupTunnelInstall is the tunnel installer the background resolver uses.
// A tunnel found after the operator switched models is closed instead of
// installed, so it cannot close the operator's tunnel.
func (l *consoleLauncher) startupTunnelInstall(gen uint64) func(func()) {
	return func(tunnel func()) {
		l.modelMu.Lock()
		defer l.modelMu.Unlock()
		if gen != l.modelGen {
			if tunnel != nil {
				tunnel()
			}
			return
		}
		installModelTunnel(tunnel)
	}
}

// bindAgentModelChoice installs backend as the agent's model for choice. It
// is the one place a model change reaches the agent, shared by /model
// (applyModelChoice) and background startup resolution: guarded runners are
// rebuilt so OwnerLabel provenance tracks the live model.
func bindAgentModelChoice(a *agent.Agent, choice ModelChoice, backend agent.ChatBackend, loader func(context.Context) (*runtimectx.Context, error)) {
	a.SetBackend(backend, choice.SecurityClass)
	a.SetModel(choice.Model)
	a.SetRunShell(guardedAgentShellRunner(choice.Model, loader))
	a.SetRunOnNode(func(ctx context.Context, node, command string) (string, error) {
		return guardedAgentCommandRunner(choice.Model, node, loader)(ctx, command)
	})
}
