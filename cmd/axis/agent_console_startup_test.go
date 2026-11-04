package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/console"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func TestHasExplicitModelFlags(t *testing.T) {
	cases := []struct {
		name string
		in   agentStartupInputs
		want bool
	}{
		{"zero", agentStartupInputs{}, false},
		{"model", agentStartupInputs{model: "example-model"}, true},
		{"role", agentStartupInputs{role: "default"}, true},
		{"cloudModel", agentStartupInputs{cloudModel: "example-cloud"}, true},
		{"provider local", agentStartupInputs{provider: "local"}, true},
		{"provider auto", agentStartupInputs{provider: "auto"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasExplicitModelFlags(tc.in); got != tc.want {
				t.Fatalf("hasExplicitModelFlags(%+v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestConsoleStartup_NothingWrittenBeforePaint(t *testing.T) {
	h := newConsoleStartupHarness(t, nil)
	consoleSetupBackendFn = func(agentStartupBackendParams) (agentStartupBackendResult, error) {
		return readyResult("example-model", &stubBackend{name: "b"}), nil
	}
	done := h.launch(t, defaultStartupInputs())

	if n := h.errWAtPaint.Load(); n != 0 {
		t.Fatalf("errW had %d bytes before paint: %q", n, h.errW.String())
	}
	opts := h.prog.opts
	if _, ok := opts.Start.(console.HeroScreen); !ok {
		t.Fatalf("Start = %T, want console.HeroScreen", opts.Start)
	}
	if opts.Version != buildinfo.Version {
		t.Fatalf("Version = %q, want %q", opts.Version, buildinfo.Version)
	}
	if opts.InitialModelState != console.ModelPending {
		t.Fatalf("InitialModelState = %v, want ModelPending", opts.InitialModelState)
	}
	if opts.HeaderModel == nil || opts.Submit == nil || opts.Cancel == nil || opts.Footer == nil {
		t.Fatal("console options missing HeaderModel/Submit/Cancel/Footer")
	}
	var advisory, finding, loading bool
	for _, l := range opts.InitialStatus {
		switch {
		case l.Text == testAdvisory && l.Persist && l.State == console.StatusInfo:
			advisory = true
		case l.Key == "model" && l.State == console.StatusRunning && l.Text == "finding a model…":
			finding = true
		case l.Key == "snapshot" && l.State == console.StatusRunning && l.Text == "loading cluster context…":
			loading = true
		}
	}
	if !advisory || !finding || !loading {
		t.Fatalf("initial status missing lines (advisory=%v finding=%v loading=%v): %+v", advisory, finding, loading, opts.InitialStatus)
	}

	h.prog.waitMsg(t, "model ready", modelStateIs(console.ModelReady))
	h.prog.exit()
	out := awaitOutcome(t, done, 5*time.Second)
	if out.err != nil || out.agent == nil {
		t.Fatalf("outcome = %+v, want bound agent and nil error", out)
	}
	if strings.Contains(h.errW.String(), "advisory") {
		t.Fatalf("advisory went to errW: %q", h.errW.String())
	}
}

func TestConsoleStartup_SlowResolutionDoesNotBlockPaint(t *testing.T) {
	h := newConsoleStartupHarness(t, nil)
	backend := &stubBackend{name: "slow"}
	res := newBlockingResolver(readyResult("example-model", backend), nil)
	consoleSetupBackendFn = res.fn
	done := h.launch(t, defaultStartupInputs())

	p := res.awaitCalled(t)
	if p.SelectModel || p.In != nil {
		t.Fatalf("background resolver must never open the stdin picker: SelectModel=%v In=%v", p.SelectModel, p.In)
	}
	h.prog.waitMsg(t, "model running", statusIs("model", console.StatusRunning, "finding a model…"))
	if h.prog.has(modelStateIs(console.ModelReady)) {
		t.Fatal("ModelReady sent before the resolver returned")
	}

	// The agent is bound while the resolver is still parked: a command runs.
	msg := runCmd(t, h.prog.opts.Submit(1, "/autonomy"))
	if done, ok := msg.(console.TurnDoneMsg); !ok || done.Err != nil {
		t.Fatalf("/autonomy before resolution: %+v", msg)
	}
	if h.prog.has(noticeContains("agent still starting")) {
		t.Fatal("agent was not bound before model resolution")
	}

	close(res.release)
	h.prog.waitMsg(t, "model ready", modelStateIs(console.ModelReady))
	h.prog.waitMsg(t, "model done", statusIs("model", console.StatusDone, "example-model ready"))
	h.prog.exit()
	out := awaitOutcome(t, done, 5*time.Second)
	if out.err != nil {
		t.Fatalf("err = %v", out.err)
	}
	if out.agent.Backend() != backend || out.agent.Model() != "example-model" {
		t.Fatalf("resolved backend not applied: model=%q", out.agent.Model())
	}
	if n := countMsgs(h.prog.all(), func(m tea.Msg) bool { _, ok := m.(console.ModelStateMsg); return ok }); n != 1 {
		t.Fatalf("want exactly one ModelStateMsg, got %d", n)
	}
}

func TestConsoleStartup_NoModel(t *testing.T) {
	h := newConsoleStartupHarness(t, nil)
	consoleSetupBackendFn = func(agentStartupBackendParams) (agentStartupBackendResult, error) {
		return agentStartupBackendResult{}, ExitCodeError{Code: ExitErrConfigLoad, Message: "no models found (neither local Ollama models nor enabled cloud providers)"}
	}
	done := h.launch(t, defaultStartupInputs())

	h.prog.waitMsg(t, "model none", modelStateIs(console.ModelNone))
	h.prog.waitMsg(t, "model failed", statusIs("model", console.StatusFailed, "no model · /model or axis model start"))
	h.prog.waitMsg(t, "persisted error", persistContains(console.StatusFailed, "no models found"))
	h.prog.exit()
	out := awaitOutcome(t, done, 5*time.Second)
	if out.err != nil {
		t.Fatalf("no model must not fail the session: %v", out.err)
	}
	if out.agent == nil {
		t.Fatal("agent must still be bound without a model")
	}
}

func TestConsoleStartup_ResolverPanicBecomesNoModel(t *testing.T) {
	h := newConsoleStartupHarness(t, nil)
	consoleSetupBackendFn = func(agentStartupBackendParams) (agentStartupBackendResult, error) {
		panic("resolver exploded")
	}
	done := h.launch(t, defaultStartupInputs())

	h.prog.waitMsg(t, "model none", modelStateIs(console.ModelNone))
	h.prog.waitMsg(t, "panic persisted", persistContains(console.StatusFailed, "resolver exploded"))
	h.prog.waitMsg(t, "model failed", statusIs("model", console.StatusFailed, "no model · /model or axis model start"))
	h.prog.exit()
	out := awaitOutcome(t, done, 5*time.Second)
	if out.err != nil {
		t.Fatalf("a startup panic must not fail the session: %v", out.err)
	}
	if n := countMsgs(h.prog.all(), func(m tea.Msg) bool { _, ok := m.(console.ModelStateMsg); return ok }); n != 1 {
		t.Fatalf("want exactly one ModelStateMsg, got %d", n)
	}
}

func TestConsoleStartup_RuntimeLoadTimeout(t *testing.T) {
	h := newConsoleStartupHarness(t, func(ctx context.Context) (*runtimectx.Context, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	consoleRuntimeLoadWait = 50 * time.Millisecond
	consoleSetupBackendFn = func(agentStartupBackendParams) (agentStartupBackendResult, error) {
		return readyResult("example-model", &stubBackend{name: "b"}), nil
	}
	start := time.Now()
	done := h.launch(t, defaultStartupInputs())

	h.prog.waitMsg(t, "no snapshot", statusIs("snapshot", console.StatusInfo, "no snapshot"))
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("runtime load timeout took %s", elapsed)
	}
	h.prog.waitMsg(t, "load error persisted", persistContains(console.StatusInfo, "Could not load cluster context"))
	h.prog.waitMsg(t, "model ready", modelStateIs(console.ModelReady))
	h.prog.exit()
	if out := awaitOutcome(t, done, 5*time.Second); out.err != nil {
		t.Fatalf("err = %v", out.err)
	}
}

func mcpRuntime() *runtimectx.Context {
	return &runtimectx.Context{Config: &config.Config{MCPServers: map[string]config.MCPServerConfig{
		"srv-a": {Transport: "stdio", Command: []string{"true"}},
		"srv-b": {Transport: "stdio", Command: []string{"true"}},
	}}}
}

func TestConsoleStartup_MCPFailureVisible(t *testing.T) {
	h := newConsoleStartupHarness(t, func(context.Context) (*runtimectx.Context, error) { return mcpRuntime(), nil })
	consoleConnectMCPFn = func(_ context.Context, reg *mcpclient.Registry, _ *config.Config) {
		reg.Add(&mcpclient.ServerConnection{Name: "srv-a", InitResult: &mcp.InitializeResult{}})
		reg.Add(&mcpclient.ServerConnection{Name: "srv-b", Err: errors.New("handshake refused")})
	}
	consoleSetupBackendFn = func(agentStartupBackendParams) (agentStartupBackendResult, error) {
		return readyResult("example-model", &stubBackend{name: "b"}), nil
	}
	done := h.launch(t, defaultStartupInputs())

	h.prog.waitMsg(t, "mcp failure", statusIs("mcp", console.StatusFailed, "MCP: 1 of 2 failed (srv-b)"))
	h.prog.exit()
	if out := awaitOutcome(t, done, 5*time.Second); out.err != nil {
		t.Fatalf("err = %v", out.err)
	}
	if !h.mcpClosed.Load() {
		t.Fatal("MCP registry was not closed when the session ended")
	}
}

func TestConsoleStartup_MCPAllConnected(t *testing.T) {
	h := newConsoleStartupHarness(t, func(context.Context) (*runtimectx.Context, error) { return mcpRuntime(), nil })
	consoleConnectMCPFn = func(_ context.Context, reg *mcpclient.Registry, _ *config.Config) {
		reg.Add(&mcpclient.ServerConnection{Name: "srv-a", InitResult: &mcp.InitializeResult{}})
		reg.Add(&mcpclient.ServerConnection{Name: "srv-b", InitResult: &mcp.InitializeResult{}})
	}
	consoleSetupBackendFn = func(agentStartupBackendParams) (agentStartupBackendResult, error) {
		return readyResult("example-model", &stubBackend{name: "b"}), nil
	}
	done := h.launch(t, defaultStartupInputs())
	h.prog.waitMsg(t, "mcp done", statusIs("mcp", console.StatusDone, "2 MCP servers"))
	h.prog.exit()
	awaitOutcome(t, done, 5*time.Second)
}

func TestConsoleStartup_QuitWhileMCPHangs(t *testing.T) {
	h := newConsoleStartupHarness(t, func(context.Context) (*runtimectx.Context, error) { return mcpRuntime(), nil })
	consoleConnectMCPFn = func(ctx context.Context, _ *mcpclient.Registry, _ *config.Config) { <-ctx.Done() }
	resolverCalled := atomic.Bool{}
	consoleSetupBackendFn = func(agentStartupBackendParams) (agentStartupBackendResult, error) {
		resolverCalled.Store(true)
		return readyResult("example-model", &stubBackend{name: "b"}), nil
	}
	done := h.launch(t, defaultStartupInputs())
	h.prog.waitMsg(t, "mcp connecting", statusIs("mcp", console.StatusRunning, "connecting MCP…"))

	h.prog.exit()
	out := awaitOutcome(t, done, consoleStartupShutdownWait+time.Second)
	if out.err != nil {
		t.Fatalf("err = %v", out.err)
	}
	if !h.mcpClosed.Load() {
		t.Fatal("MCP registry was not closed after quitting mid-connect")
	}
	if strings.Contains(h.errW.String(), "startup still cancelling") {
		t.Fatalf("job should have finished inside the shutdown wait: %q", h.errW.String())
	}
	if resolverCalled.Load() {
		t.Fatal("startup kept resolving a model after the program exited")
	}
}

func TestConsoleStartup_ShutdownWaitIsBounded(t *testing.T) {
	h := newConsoleStartupHarness(t, func(context.Context) (*runtimectx.Context, error) { return mcpRuntime(), nil })
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	consoleConnectMCPFn = func(context.Context, *mcpclient.Registry, *config.Config) { <-stuck } // ignores ctx
	consoleStartupShutdownWaitFor = 100 * time.Millisecond
	done := h.launch(t, defaultStartupInputs())
	h.prog.waitMsg(t, "mcp connecting", statusIs("mcp", console.StatusRunning, "connecting MCP…"))

	h.prog.exit()
	out := awaitOutcome(t, done, 2*time.Second)
	if out.err != nil {
		t.Fatalf("err = %v", out.err)
	}
	if got := h.errW.String(); !strings.Contains(got, "startup still cancelling; exiting") {
		t.Fatalf("errW = %q, want the cancelling notice", got)
	}
}

func TestConsoleStartup_PickerBeatsResolution(t *testing.T) {
	hn := pickerTestSetup(t)
	rt := pickerTestRuntime(hn)
	h := newConsoleStartupHarness(t, func(context.Context) (*runtimectx.Context, error) { return rt, nil })
	late := &stubBackend{name: "late"}
	res := newBlockingResolver(readyResult("example-late", late), nil)
	consoleSetupBackendFn = res.fn
	done := h.launch(t, defaultStartupInputs())
	res.awaitCalled(t)

	// The operator picks a model while the startup resolver is still parked.
	msg := runCmd(t, h.prog.opts.Submit(1, "/model big-27b"))
	if d, ok := msg.(console.TurnDoneMsg); !ok || d.Err != nil {
		t.Fatalf("/model switch failed: %+v", msg)
	}
	h.prog.waitMsg(t, "ready after operator switch", modelStateIs(console.ModelReady))

	close(res.release)
	h.prog.waitMsg(t, "kept choice", statusIs("model", console.StatusInfo, "kept /model choice"))
	h.prog.exit()
	out := awaitOutcome(t, done, 5*time.Second)
	if out.err != nil {
		t.Fatalf("err = %v", out.err)
	}
	if out.agent.Backend() == late {
		t.Fatal("late startup resolution overwrote the operator's /model choice")
	}
	if got := out.agent.Model(); got != "big-27b" {
		t.Fatalf("model = %q, want big-27b", got)
	}
	if h.prog.has(statusIs("model", console.StatusDone, "example-late ready")) {
		t.Fatal("discarded startup result was reported as ready")
	}
}

func TestConsoleStartup_ExplicitModelUnavailableExits2(t *testing.T) {
	h := newConsoleStartupHarness(t, nil)
	consoleSetupBackendFn = func(agentStartupBackendParams) (agentStartupBackendResult, error) {
		return agentStartupBackendResult{}, ExitCodeError{Code: ExitErrConfigLoad, Message: `model "missing-model" is not in the model catalog`}
	}
	in := defaultStartupInputs()
	in.model = "missing-model"
	a, err := runAgentConsoleStartup(context.Background(), in, h.errW)
	var exitErr ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitErrConfigLoad {
		t.Fatalf("err = %v, want ExitCodeError{Code: %d}", err, ExitErrConfigLoad)
	}
	if a != nil {
		t.Fatal("no agent may be returned when startup fails before paint")
	}
	if n := h.programs.Load(); n != 0 {
		t.Fatalf("program constructed %d time(s) before an exit-2 failure", n)
	}
}

func TestConsoleStartup_ExplicitModelResolvedBeforePaint(t *testing.T) {
	h := newConsoleStartupHarness(t, nil)
	backend := &stubBackend{name: "pinned"}
	var calls atomic.Int32
	consoleSetupBackendFn = func(p agentStartupBackendParams) (agentStartupBackendResult, error) {
		calls.Add(1)
		if p.SelectModel || p.In != nil {
			t.Errorf("explicit prelude must not open the stdin picker: SelectModel=%v In=%v", p.SelectModel, p.In)
		}
		fmt.Fprintln(p.ErrOut, "Warning: model \"example-model\" is not answering (refused).")
		return readyResult("example-model", backend), nil
	}
	in := defaultStartupInputs()
	in.model = "example-model"
	done := h.launch(t, in)

	if n := h.errWAtPaint.Load(); n != 0 {
		t.Fatalf("errW had %d bytes before paint: %q", n, h.errW.String())
	}
	opts := h.prog.opts
	if opts.InitialModelState != console.ModelPending {
		t.Fatalf("InitialModelState = %v, want ModelPending until the agent is bound", opts.InitialModelState)
	}
	var note bool
	for _, l := range opts.InitialStatus {
		if l.Key == "model" && l.State == console.StatusRunning {
			t.Fatalf("pre-resolved model must not show finding a model: %+v", l)
		}
		if l.Persist && strings.Contains(l.Text, "is not answering") {
			note = true
		}
	}
	if !note {
		t.Fatalf("captured prelude output not persisted: %+v", opts.InitialStatus)
	}
	h.prog.waitMsg(t, "model ready", modelStateIs(console.ModelReady))
	h.prog.waitMsg(t, "model done", statusIs("model", console.StatusDone, "example-model ready"))
	h.prog.exit()
	out := awaitOutcome(t, done, 5*time.Second)
	if out.err != nil || out.agent.Backend() != backend {
		t.Fatalf("outcome err=%v, backend not the pre-resolved one", out.err)
	}
	if calls.Load() != 1 {
		t.Fatalf("resolver ran %d times, want once (before paint)", calls.Load())
	}
}

func TestConsoleStartup_SelectOpensPicker(t *testing.T) {
	h := newConsoleStartupHarness(t, nil)
	res := newBlockingResolver(readyResult("example-model", &stubBackend{name: "b"}), nil)
	consoleSetupBackendFn = res.fn
	in := defaultStartupInputs()
	in.selectModel = true
	done := h.launch(t, in)

	res.awaitCalled(t) // runs after bind
	h.prog.waitMsg(t, "picker command", func(m tea.Msg) bool {
		rc, ok := m.(console.RunCommandMsg)
		return ok && rc.Text == "/model"
	})
	close(res.release)
	h.prog.exit()
	awaitOutcome(t, done, 5*time.Second)
}

func TestConsoleStartup_CompactBeforeReadyFailsVisibly(t *testing.T) {
	h := newConsoleStartupHarness(t, nil)
	conv := chat.NewConversation(32768)
	for i := 0; i < 14; i++ {
		conv.Append(chat.Message{Role: chat.RoleUser, Content: fmt.Sprintf("question %d", i)})
		conv.Append(chat.Message{Role: chat.RoleAssistant, Content: fmt.Sprintf("answer %d", i)})
	}
	if err := conv.SaveToFile(chat.PersistPath("agent")); err != nil {
		t.Fatal(err)
	}
	res := newBlockingResolver(readyResult("example-model", &stubBackend{name: "b"}), nil)
	consoleSetupBackendFn = res.fn
	in := defaultStartupInputs()
	in.resume = true
	done := h.launch(t, in)
	res.awaitCalled(t)
	h.prog.waitMsg(t, "resume line", persistContains(console.StatusInfo, "Resumed 28 messages from previous session."))

	start := time.Now()
	runCmd(t, h.prog.opts.Submit(1, "/compact"))
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("/compact before a model took %s", elapsed)
	}
	h.prog.waitMsg(t, "visible compaction failure", noticeContains("no model is ready yet"))

	close(res.release)
	h.prog.exit()
	awaitOutcome(t, done, 5*time.Second)
}

func TestConsoleStartup_ExplicitPreludeErrorKeepsNotes(t *testing.T) {
	h := newConsoleStartupHarness(t, func(context.Context) (*runtimectx.Context, error) {
		return nil, errors.New("cache unreadable")
	})
	consoleSetupBackendFn = func(agentStartupBackendParams) (agentStartupBackendResult, error) {
		return agentStartupBackendResult{}, ExitCodeError{Code: ExitErrConfigLoad, Message: "no models found"}
	}
	in := defaultStartupInputs()
	in.provider = "local"
	_, err := runAgentConsoleStartup(context.Background(), in, h.errW)
	var exitErr ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitErrConfigLoad || exitErr.Message != "no models found" {
		t.Fatalf("err = %#v, want the resolver's ExitCodeError unchanged", err)
	}
	if got := h.errW.String(); !strings.Contains(got, "Could not load cluster context: cache unreadable") {
		t.Fatalf("errW = %q, want the load warning ahead of the error", got)
	}
}
