package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/console"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/ui"
)

// The console path of `axis agent` paints first and finishes startup in the
// background: cluster context, MCP servers, the agent itself, and the model
// all arrive as status lines on the start screen. Every background step is
// bounded, reports its failure on screen, and never takes the terminal down.

// Startup bounds. Model resolution keeps its own internal timeouts.
const (
	consoleRuntimeLoadTimeout = 5 * time.Second
	// consoleLiveRuntimeLoadTimeout bounds the --live SSH sweep. Nodes are
	// probed in parallel, each under its own timeout_sec (default 10s).
	consoleLiveRuntimeLoadTimeout = 30 * time.Second
	consoleMCPConnectTimeout      = 10 * time.Second
	consoleStartupShutdownWait    = 3 * time.Second
	// consoleGitBranchTimeout bounds the cwd line's branch lookup.
	consoleGitBranchTimeout = 2 * time.Second
)

// Exact operator-facing copy for the startup path.
const (
	consoleAdvisory  = "advisory: agent output is not cluster truth — it uses tools to read the fact plane"
	consoleNotReady  = "agent still starting — try again in a moment"
	consoleNoModel   = "no model · /model or axis model start"
	consoleKeptModel = "kept /model choice"
	consoleCancelMsg = "startup still cancelling; exiting"
)

// errNoModelYet is what the placeholder backend answers until a model is
// bound. Prompts never reach it (the console holds them while a model is
// pending); commands that call the model directly, such as /compact, fail
// fast with this text instead of hanging.
var errNoModelYet = errors.New("no model is ready yet — wait for it, or choose one with /model")

// Seams. Tests replace these; the startup job captures them once at launch so
// a test restoring them cannot race a job that is still winding down.
var (
	consoleSetupBackendFn = setupAgentStartupBackend
	newConsoleProgramFn   = func(ctx context.Context, opts console.Options) consoleProgram {
		return tea.NewProgram(console.NewModel(opts), consoleOptions(ctx).teaOptions()...)
	}
	consoleConnectMCPFn = func(ctx context.Context, reg *mcpclient.Registry, cfg *config.Config) {
		reg.ConnectAll(ctx, cfg)
	}
	consoleCloseMCPFn          = (*mcpclient.Registry).Close
	consoleGitBranchFn         = gitCurrentBranch
	consoleRuntimeLoadWait     = consoleRuntimeLoadTimeout
	consoleLiveRuntimeLoadWait = consoleLiveRuntimeLoadTimeout
	// consoleRuntimePartialGrace is how long a timed-out loader may still
	// take to hand back what it gathered before the deadline.
	consoleRuntimePartialGrace    = time.Second
	consoleMCPConnectWait         = consoleMCPConnectTimeout
	consoleStartupShutdownWaitFor = consoleStartupShutdownWait
)

// consoleProgram is the part of *tea.Program the console path drives.
type consoleProgram interface {
	Send(tea.Msg)
	Run() (tea.Model, error)
	Kill()
}

// agentStartupInputs holds every agent flag value the console session needs.
type agentStartupInputs struct {
	model, role, provider, cloudModel, cheapModel string
	systemMsg                                     string
	maxTurns, maxTokens                           int
	timeout                                       time.Duration
	autoApprove                                   bool
	autonomy                                      string
	verbose, dryRun, allowRawCommandEvidence      bool
	resume, selectModel, live                     bool

	// out is the agent's default output writer; nil discards.
	out io.Writer
}

// hasExplicitModelFlags reports whether the operator pinned the model. Pinned
// choices are resolved before paint and still fail loudly (exit 2); implicit
// defaults open the console anyway and resolve in the background.
func hasExplicitModelFlags(in agentStartupInputs) bool {
	return in.model != "" || in.role != "" || in.cloudModel != "" || (in.provider != "" && in.provider != "auto")
}

// startupPendingBackend stands in for the model until resolution binds one.
type startupPendingBackend struct{}

func (startupPendingBackend) ChatStream(context.Context, []chat.Message, []chat.ToolDef, io.Writer) (chat.Message, error) {
	return chat.Message{}, errNoModelYet
}

// consolePrelude is what the explicit-flag path resolves before paint.
type consolePrelude struct {
	rt     *runtimectx.Context
	rtErr  error
	result agentStartupBackendResult
	notes  []string
}

// consoleExplicitPrelude is today's synchronous model setup for pinned
// choices, with its output captured instead of printed and the stdin picker
// disabled (-s opens the in-console picker instead). On error the captured
// lines are returned with the unchanged error, so exit codes stay as today.
func consoleExplicitPrelude(ctx context.Context, in *agentStartupInputs, loader func(context.Context) (*runtimectx.Context, error), wait time.Duration, setup func(agentStartupBackendParams) (agentStartupBackendResult, error)) (consolePrelude, error) {
	var notes bytes.Buffer
	rt, rtErr := loadRuntimeBounded(ctx, loader, wait)
	if rtErr != nil {
		fmt.Fprintf(&notes, "Could not load cluster context: %v\n", rtErr)
	}
	fail := func(err error) (consolePrelude, error) {
		return consolePrelude{notes: noteLines(notes.String())}, err
	}
	if strings.TrimSpace(in.model) != "" && strings.TrimSpace(in.role) != "" {
		return fail(fmt.Errorf("use either --model or --role, not both"))
	}
	if strings.TrimSpace(in.model) == "" && strings.TrimSpace(in.role) != "" {
		fromRole := modelFromAIRoleFn(in.role)
		if fromRole == "" {
			return fail(fmt.Errorf("unknown or empty ai.yaml role %q (axis ai roles)", in.role))
		}
		in.model = fromRole
	}
	requested := effectiveStartupRequestedModel(in.model, rt)
	if in.verbose && strings.TrimSpace(in.model) == "" {
		label := requested
		if label == "" {
			label = resolveAgentModel(in.model, rt)
		}
		fmt.Fprintf(&notes, "Resolved model: %s\n", label)
	}
	res, err := setup(agentStartupBackendParams{
		Model:                 in.model,
		StartupRequestedModel: requested,
		Provider:              in.provider,
		CloudModel:            in.cloudModel,
		CheapModel:            in.cheapModel,
		Verbose:               in.verbose,
		RT:                    rt,
		Ctx:                   ctx,
		Out:                   &notes,
		ErrOut:                &notes,
		HideRoutingTrace:      true,
	})
	if err != nil {
		return fail(err)
	}
	return consolePrelude{rt: rt, rtErr: rtErr, result: res, notes: noteLines(notes.String())}, nil
}

// noteLines splits captured output into non-empty lines.
func noteLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// consoleColorDepth is the start screen's color depth; none when color is off.
func consoleColorDepth() ui.ColorDepth {
	if !ui.Enabled() {
		return ui.ColorNone
	}
	return ui.DetectColorDepth(os.Getenv)
}

// consoleCwd is the working directory, ~-relative under the home directory.
func consoleCwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if cwd == home {
			return "~"
		}
		if rel, err := filepath.Rel(home, cwd); err == nil && !strings.HasPrefix(rel, "..") {
			return "~" + string(filepath.Separator) + rel
		}
	}
	return cwd
}

// consoleRuntimeSource picks the cluster-context loader and its bound: the
// cached snapshot is quick, a --live sweep gets the longer live bound.
func consoleRuntimeSource(live bool) (func(context.Context) (*runtimectx.Context, error), time.Duration) {
	if live {
		return runtimectx.LoadLive, consoleLiveRuntimeLoadWait
	}
	return loadAgentShellRuntime, consoleRuntimeLoadWait
}

// runAgentConsoleStartup runs the console path of `axis agent`: it paints the
// start screen first and finishes startup in a background job. It returns the
// bound agent (nil if startup never got that far) so the caller's session
// summary still works. Nothing is written to errW before the program runs.
func runAgentConsoleStartup(ctx context.Context, in agentStartupInputs, errW io.Writer) (*agent.Agent, error) {
	loader, runtimeWait := consoleRuntimeSource(in.live)
	job := &consoleStartupJob{
		in:           in,
		loader:       loader,
		setup:        consoleSetupBackendFn,
		connectMCP:   consoleConnectMCPFn,
		closeMCP:     consoleCloseMCPFn,
		branch:       consoleGitBranchFn,
		runtimeWait:  runtimeWait,
		mcpWait:      consoleMCPConnectWait,
		historyPath:  chat.PersistPath("agent"),
		cwd:          consoleCwd(),
		out:          in.out,
		modelPending: true,
	}
	shutdownWait := consoleStartupShutdownWaitFor
	if job.out == nil {
		job.out = io.Discard
	}

	if hasExplicitModelFlags(in) {
		p, err := consoleExplicitPrelude(ctx, &job.in, loader, runtimeWait, job.setup)
		if err != nil {
			// No program was started: the captured lines go where they
			// always went, ahead of the unchanged error.
			for _, n := range p.notes {
				fmt.Fprintln(errW, n)
			}
			return nil, err
		}
		job.prelude = &p
		job.modelPending = false
	}

	l := newStartupConsoleLauncher(in.timeout, time.Now)
	l.loader = loader
	fleet := newConsoleFleet(ctx, loader)
	fleet.wait = runtimeWait
	job.launcher, job.fleet = l, fleet

	opts := consoleBaseOptions(ctx, l, fleet, consoleInitialHistory(nil))
	opts.Start = console.HeroScreen{}
	opts.Version = buildinfo.Version
	opts.Unicode = ui.UnicodeSupported(os.Getenv)
	opts.Depth = consoleColorDepth()
	opts.HeaderModel = l.headerModel
	opts.InitialStatus = job.initialStatus()
	// Pending even when the model was resolved before paint: the agent is
	// bound in the background, and a prompt typed before that must be held,
	// not refused. The job settles Ready right after bind.
	opts.InitialModelState = console.ModelPending

	prog := newConsoleProgramFn(ctx, opts)
	l.prog = prog
	job.prog = prog

	// The job owns jobCtx; it is cancelled as soon as the program exits.
	jobCtx, cancelJob := context.WithCancel(ctx)
	defer cancelJob()
	jobDone := job.launch(jobCtx)

	// Restore the terminal whatever happens, including a panic in a view.
	defer prog.Kill()
	_, runErr := prog.Run()

	cancelJob()
	timer := time.NewTimer(shutdownWait)
	defer timer.Stop()
	select {
	case <-jobDone:
	case <-timer.C:
		fmt.Fprintln(errW, consoleCancelMsg)
	}

	a := l.boundAgent()
	if runErr != nil {
		return a, fmt.Errorf("console: %w", runErr)
	}
	// An unused session must not overwrite the saved history with an empty
	// one; the plain REPL has the same guard.
	if a != nil && job.historyPath != "" && a.Conversation().HistoryCount() > 0 {
		_ = saveAgentConversation(a.Conversation(), job.historyPath, errW)
	}
	return a, nil
}
