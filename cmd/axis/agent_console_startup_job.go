package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/console"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

// consoleStartupJob is the background half of console startup. Its fields are
// fixed before run starts; modelSettled is touched only by the job goroutine.
type consoleStartupJob struct {
	in          agentStartupInputs
	loader      func(context.Context) (*runtimectx.Context, error)
	setup       func(agentStartupBackendParams) (agentStartupBackendResult, error)
	connectMCP  func(context.Context, *mcpclient.Registry, *config.Config)
	closeMCP    func(*mcpclient.Registry)
	branch      func(context.Context) (string, error)
	runtimeWait time.Duration
	mcpWait     time.Duration
	historyPath string
	cwd         string
	out         io.Writer
	prelude     *consolePrelude

	// modelPending is true when the model resolves in the background.
	modelPending bool

	launcher *consoleLauncher
	fleet    *consoleFleet
	prog     interface{ Send(tea.Msg) }

	modelSettled bool
	noteSeq      int

	// regReady carries the MCP registry (or nil) to the closer once connect
	// has returned, so the closer never races a connect that is still
	// adding servers. Buffered; sent exactly once via sendReg.
	regReady chan *mcpclient.Registry
	regOnce  sync.Once
}

// initialStatus is the start screen's first frame of status lines.
func (j *consoleStartupJob) initialStatus() []console.StatusLine {
	var lines []console.StatusLine
	if j.cwd != "" {
		lines = append(lines, console.StatusLine{Key: "cwd", State: console.StatusInfo, Text: j.cwd})
	}
	lines = append(lines, console.StatusLine{Key: "snapshot", State: console.StatusRunning, Text: "loading cluster context…"})
	if j.modelPending {
		lines = append(lines, console.StatusLine{Key: "model", State: console.StatusRunning, Text: "finding a model…"})
	}
	lines = append(lines, console.StatusLine{Key: "advisory", State: console.StatusInfo, Text: consoleAdvisory, Persist: true})
	if j.prelude != nil {
		for _, n := range j.prelude.notes {
			j.noteSeq++
			lines = append(lines, console.StatusLine{Key: fmt.Sprintf("note-%d", j.noteSeq), State: console.StatusInfo, Text: n, Persist: true})
		}
	}
	return lines
}

func (j *consoleStartupJob) status(l console.StatusLine) {
	j.prog.Send(console.StartupStatusMsg{Line: l})
}

func (j *consoleStartupJob) note(state console.StatusState, text string) {
	j.noteSeq++
	j.status(console.StatusLine{Key: fmt.Sprintf("note-%d", j.noteSeq), State: state, Text: text, Persist: true})
}

// settleModel sends the model's final status line and state, exactly once.
func (j *consoleStartupJob) settleModel(state console.ModelState, line console.StatusLine) {
	if j.modelSettled {
		return
	}
	j.modelSettled = true
	j.status(line)
	j.prog.Send(console.ModelStateMsg{State: state})
}

func (j *consoleStartupJob) settleNoModel() {
	j.settleModel(console.ModelNone, console.StatusLine{Key: "model", State: console.StatusFailed, Text: consoleNoModel})
}

// launch starts the job, the branch lookup, and the MCP closer. The returned
// channel closes once the steps and the MCP close have both finished.
func (j *consoleStartupJob) launch(ctx context.Context) <-chan struct{} {
	j.regReady = make(chan *mcpclient.Registry, 1)
	branchDone := j.startBranchLookup(ctx)
	closed := j.closeMCPWhenDone(ctx, branchDone)
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		j.run(ctx)
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-runDone
		<-closed
	}()
	return done
}

// closeMCPWhenDone closes the MCP registry exactly once, as soon as ctx is
// done and connect has returned. It does not wait for model resolution: a
// quit during a slow probe must not leave stdio servers running after axis
// exits. The registry serves the agent's MCP tools for the whole session, so
// it closes only once the program has exited; by then there is no screen to
// report a close failure on, and a panic here must not take down a process
// that is already shutting down cleanly.
func (j *consoleStartupJob) closeMCPWhenDone(ctx context.Context, branchDone <-chan struct{}) <-chan struct{} {
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		<-ctx.Done()
		<-branchDone // bounded: the lookup runs under ctx, now cancelled
		reg := <-j.regReady
		if reg == nil {
			return
		}
		defer func() { _ = recover() }()
		j.closeMCP(reg)
	}()
	return closed
}

// sendReg hands the registry (or nil) to the closer, once.
func (j *consoleStartupJob) sendReg(reg *mcpclient.Registry) {
	j.regOnce.Do(func() { j.regReady <- reg })
}

// run performs the startup steps in order. Sends after the program exits
// return at once (bubbletea's Send selects on the program context, which Run
// cancels on return), so no step can wedge on a closed program.
func (j *consoleStartupJob) run(ctx context.Context) {
	defer j.sendReg(nil) // no-op once startMCP handed over a registry
	defer j.recoverPanic()

	rt := j.loadRuntime(ctx)
	if j.stopped(ctx) {
		return
	}
	reg := j.startMCP(ctx, rt)
	j.sendReg(reg)
	if j.stopped(ctx) {
		return
	}
	// The startup generation is read before bind: bind enables /model, and
	// an operator switch landing between bind and resolution must retire
	// this resolution, not be captured by it.
	gen := j.launcher.currentModelGen()
	a := j.buildAgent(ctx, rt, reg)
	if j.stopped(ctx) {
		return
	}
	if j.in.selectModel {
		j.prog.Send(console.RunCommandMsg{Text: "/model"})
	}
	if j.prelude != nil {
		r := j.prelude.result
		j.settleModel(console.ModelReady, startupModelLine(r.ActiveTarget, r.Readiness))
		return
	}
	j.resolveModel(ctx, a, rt, gen)
}

// recoverPanic turns a startup panic into a visible failure. Held prompts are
// released (as the no-model notice) if the model had not settled.
func (j *consoleStartupJob) recoverPanic() {
	r := recover()
	if r == nil {
		return
	}
	err := fmt.Errorf("%v", r)
	j.launcher.failStart(err)
	j.status(console.StatusLine{Key: "startup", State: console.StatusFailed, Text: "startup failed: " + err.Error(), Persist: true})
	j.settleNoModel()
}

// stopped ends the job early once the program has exited, still settling the
// model so no held prompt is stranded.
func (j *consoleStartupJob) stopped(ctx context.Context) bool {
	if ctx.Err() == nil {
		return false
	}
	j.settleNoModel()
	return true
}

// startBranchLookup adds the git branch to the cwd line from its own
// goroutine, off the startup critical path: a slow repository must not delay
// the agent. The goroutine is owned by the job, bounded by
// consoleGitBranchTimeout and ctx, and the returned channel closes when it
// exits. Every failure (no git, not a repository, detached HEAD, timeout) is
// silent by design: the cwd line simply stays without a branch.
func (j *consoleStartupJob) startBranchLookup(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	if j.cwd == "" || j.branch == nil {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		defer func() { _ = recover() }() // a decoration must never crash startup
		bctx, cancel := context.WithTimeout(ctx, consoleGitBranchTimeout)
		defer cancel()
		branch, err := j.branch(bctx)
		if err != nil || branch == "" || ctx.Err() != nil {
			return
		}
		j.status(console.StatusLine{Key: "cwd", State: console.StatusInfo, Text: j.cwd + " (" + branch + ")"})
	}()
	return done
}

// gitCurrentBranch is one cancellable `git branch --show-current` in the
// working directory. Output (and stderr) never reaches the terminal.
func gitCurrentBranch(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "branch", "--show-current")
	cmd.WaitDelay = 100 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// loadRuntime is step 1: cluster context, bounded, priming the fleet snapshot.
func (j *consoleStartupJob) loadRuntime(ctx context.Context) *runtimectx.Context {
	var rt *runtimectx.Context
	var err error
	if j.prelude != nil {
		rt, err = j.prelude.rt, j.prelude.rtErr
	} else {
		rt, err = loadRuntimeBounded(ctx, j.loader, j.runtimeWait)
	}
	j.fleet.prime(rt, err)
	if err != nil {
		j.note(console.StatusInfo, fmt.Sprintf("Could not load cluster context: %v", err))
	}
	j.status(snapshotStatus(rt))
	return rt
}

// snapshotStatus is the snapshot line for a loaded runtime.
func snapshotStatus(rt *runtimectx.Context) console.StatusLine {
	line := console.StatusLine{Key: "snapshot", State: console.StatusInfo, Text: "no snapshot"}
	if rt == nil || rt.Snapshot == nil {
		return line
	}
	line.State = console.StatusDone
	if age, ok := publicationAge(rt.Snapshot); ok {
		line.Text = "snapshot " + formatDuration(age) + " ago"
	} else {
		line.Text = "snapshot (age unknown)"
	}
	return line
}

// startMCP is step 2: connect configured MCP servers, bounded.
func (j *consoleStartupJob) startMCP(ctx context.Context, rt *runtimectx.Context) *mcpclient.Registry {
	if rt == nil || rt.Config == nil {
		return nil
	}
	reg := mcpclient.NewRegistry()
	if len(rt.Config.MCPServers) == 0 {
		return reg
	}
	j.status(console.StatusLine{Key: "mcp", State: console.StatusRunning, Text: "connecting MCP…"})
	mctx, cancel := context.WithTimeout(ctx, j.mcpWait)
	// Deferred so a connect that panics after spawning a server still hands
	// the registry to the closer.
	defer j.sendReg(reg)
	j.connectMCP(mctx, reg, rt.Config)
	cancel()
	j.status(mcpStatus(reg, rt.Config))
	return reg
}

// mcpStatus summarizes the connect outcome over the configured servers.
func mcpStatus(reg *mcpclient.Registry, cfg *config.Config) console.StatusLine {
	var failed []string
	for name := range cfg.MCPServers {
		if sc := reg.Get(name); sc == nil || !sc.Connected() {
			failed = append(failed, name)
		}
	}
	n := len(cfg.MCPServers)
	if len(failed) > 0 {
		sort.Strings(failed)
		return console.StatusLine{Key: "mcp", State: console.StatusFailed,
			Text: fmt.Sprintf("MCP: %d of %d failed (%s)", len(failed), n, strings.Join(failed, ", "))}
	}
	text := fmt.Sprintf("%d MCP servers", n)
	if n == 1 {
		text = "1 MCP server"
	}
	return console.StatusLine{Key: "mcp", State: console.StatusDone, Text: text}
}

// buildAgent is step 3: the agent exactly as the command builds it today,
// with the pre-resolved backend or the placeholder, then bound to the
// launcher.
func (j *consoleStartupJob) buildAgent(ctx context.Context, rt *runtimectx.Context, reg *mcpclient.Registry) *agent.Agent {
	cluster, k, tc := agentToolRuntime(rt, j.loader)
	var target ModelChoice
	var backend agent.ChatBackend = startupPendingBackend{}
	if j.prelude != nil {
		target, backend = j.prelude.result.ActiveTarget, j.prelude.result.Backend
	}
	endpoint := target.Endpoint
	if endpoint == "" {
		endpoint = chat.DefaultEndpoint
	}
	a := agent.New(buildAgentSessionConfig(agentSessionParams{
		Endpoint:                endpoint,
		Model:                   target.Model,
		Backend:                 backend,
		MaxTurns:                j.in.maxTurns,
		MaxTokens:               j.in.maxTokens,
		AutoApprove:             j.in.autoApprove,
		Autonomy:                agent.AutonomyMode(j.in.autonomy),
		SystemExtra:             j.in.systemMsg,
		Verbose:                 j.in.verbose,
		DryRun:                  j.in.dryRun,
		AllowRawCommandEvidence: j.in.allowRawCommandEvidence,
		BackendSecurityClass:    target.SecurityClass,
		Cluster:                 cluster,
		Knowledge:               k,
		ToolContext:             tc,
		Output:                  j.out,
		MCPRegistry:             reg,
		RuntimeLoader:           j.loader,
	}))
	if j.in.resume {
		if err := a.Conversation().LoadFromFile(j.historyPath); err != nil {
			j.note(console.StatusFailed, fmt.Sprintf("could not resume conversation: %v", err))
		} else if n := a.Conversation().HistoryCount(); n > 0 {
			j.note(console.StatusInfo, fmt.Sprintf("Resumed %d messages from previous session.", n))
		}
	}
	// Approvals fail closed. Installed before bind, so no turn can reach the
	// agent's stdin prompt while Bubble Tea owns the terminal.
	a.SetConfirm(consoleConfirm(ctx, j.prog.Send, time.Now))
	j.launcher.bind(a, reg, target)
	return a
}

// resolveModel is step 4: find a model the way startup always has, minus the
// stdin picker, and apply it unless the operator already chose one. gen is
// the model generation read before bind.
func (j *consoleStartupJob) resolveModel(ctx context.Context, a *agent.Agent, rt *runtimectx.Context, gen uint64) {
	j.status(console.StatusLine{Key: "model", State: console.StatusRunning, Text: "finding a model…"})

	var notes bytes.Buffer
	requested := effectiveStartupRequestedModel("", rt)
	if j.in.verbose {
		label := requested
		if label == "" {
			label = resolveAgentModel("", rt)
		}
		fmt.Fprintf(&notes, "Resolved model: %s\n", label)
	}
	res, err := j.setup(agentStartupBackendParams{
		StartupRequestedModel: requested,
		Provider:              j.in.provider,
		CloudModel:            j.in.cloudModel,
		CheapModel:            j.in.cheapModel,
		Verbose:               j.in.verbose,
		RT:                    rt,
		Ctx:                   withStartupTunnelInstaller(ctx, j.launcher.startupTunnelInstall(gen)),
		Out:                   &notes,
		ErrOut:                &notes,
		HideRoutingTrace:      true,
	})
	if j.stopped(ctx) {
		return // the program exited during resolution: bind nothing
	}
	if err == nil && res.Backend == nil {
		err = errors.New("model resolution returned no backend")
	}
	// This path has no explicit model flags, so a model that is not
	// answering is a fallback nobody chose: it is not bound.
	down := err == nil && res.Readiness == modelDown
	j.startupNotes(notes.String(), down, res.ActiveTarget)

	// Decide and report under modelMu, so an operator switch is either
	// fully before this (and wins) or fully after (and its Ready follows).
	j.launcher.finishStartupModel(gen, func(current bool) {
		switch {
		case !current:
			// The operator chose a model meanwhile; theirs stands.
			j.settleModel(console.ModelReady, console.StatusLine{Key: "model", State: console.StatusInfo, Text: consoleKeptModel})
		case err != nil:
			j.note(console.StatusFailed, err.Error())
			j.settleNoModel()
		case down:
			j.settleNoModel()
		default:
			bindAgentModelChoice(a, res.ActiveTarget, res.Backend, j.loader)
			j.settleModel(console.ModelReady, startupModelLine(res.ActiveTarget, res.Readiness))
		}
	})
}

// modelLabel names a model target for status lines.
func modelLabel(t ModelChoice) string {
	if t.Model != "" {
		return t.Model
	}
	if t.ID != "" {
		return t.ID
	}
	return "model"
}
