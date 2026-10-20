package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/console"
	"github.com/toasterbook88/axis/internal/git"
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

// run performs the startup steps in order. Sends after the program exits
// return at once (bubbletea's Send selects on the program context, which Run
// cancels on return), so no step can wedge on a closed program.
func (j *consoleStartupJob) run(ctx context.Context) {
	var reg *mcpclient.Registry
	defer func() {
		// The registry serves the agent's MCP tools for the whole session,
		// so it closes only once the program has exited. By then there is
		// no screen to report a close failure on, and a panic here must not
		// take down a process that is already shutting down cleanly.
		<-ctx.Done()
		if reg != nil {
			defer func() { _ = recover() }()
			j.closeMCP(reg)
		}
	}()
	defer j.recoverPanic()

	j.sendBranch()
	rt := j.loadRuntime(ctx)
	if j.stopped(ctx) {
		return
	}
	reg = j.startMCP(ctx, rt)
	if j.stopped(ctx) {
		return
	}
	a := j.buildAgent(ctx, rt, reg)
	if j.stopped(ctx) {
		return
	}
	if j.in.selectModel {
		j.prog.Send(console.RunCommandMsg{Text: "/model"})
	}
	if j.prelude != nil {
		target := j.prelude.result.ActiveTarget
		j.settleModel(console.ModelReady, console.StatusLine{Key: "model", State: console.StatusDone, Text: modelLabel(target) + " ready"})
		return
	}
	j.resolveModel(ctx, a, rt)
}

// recoverPanic turns a startup panic into a visible failure. Held prompts are
// released (as the no-model notice) if the model had not settled.
func (j *consoleStartupJob) recoverPanic() {
	r := recover()
	if r == nil {
		return
	}
	j.status(console.StatusLine{Key: "startup", State: console.StatusFailed, Text: fmt.Sprintf("startup failed: %v", r), Persist: true})
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

// sendBranch adds the git branch to the cwd line. It runs in the job, not
// before paint, because the repository probe shells out to git.
func (j *consoleStartupJob) sendBranch() {
	if j.cwd == "" {
		return
	}
	state, err := git.GetRepoState(".")
	if err != nil || !state.IsRepo || state.Branch == "" {
		return
	}
	j.status(console.StatusLine{Key: "cwd", State: console.StatusInfo, Text: j.cwd + " (" + state.Branch + ")"})
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
// stdin picker, and apply it unless the operator already chose one.
func (j *consoleStartupJob) resolveModel(ctx context.Context, a *agent.Agent, rt *runtimectx.Context) {
	j.status(console.StatusLine{Key: "model", State: console.StatusRunning, Text: "finding a model…"})
	gen := j.launcher.currentModelGen()

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
	})
	for _, n := range noteLines(notes.String()) {
		j.note(console.StatusInfo, n)
	}
	if err == nil && res.Backend == nil {
		err = errors.New("model resolution returned no backend")
	}

	applied := false
	if err == nil {
		applied, err = j.launcher.applyStartupModel(gen, func() error {
			bindAgentModelChoice(a, res.ActiveTarget, res.Backend, j.loader)
			return nil
		})
	}
	switch {
	case applied:
		j.settleModel(console.ModelReady, console.StatusLine{Key: "model", State: console.StatusDone, Text: modelLabel(res.ActiveTarget) + " ready"})
	case j.launcher.currentModelGen() != gen:
		// The operator chose a model meanwhile; theirs stands.
		j.settleModel(console.ModelReady, console.StatusLine{Key: "model", State: console.StatusInfo, Text: consoleKeptModel})
	default:
		if err == nil {
			err = errors.New("model was not applied")
		}
		j.note(console.StatusFailed, err.Error())
		j.settleNoModel()
	}
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
