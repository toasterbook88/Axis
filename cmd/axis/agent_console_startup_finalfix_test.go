package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/console"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

// verdictResult is a resolver result carrying a readiness verdict and the
// warning the real resolver prints for a model that is not answering.
func verdictResult(v modelReadiness, b agent.ChatBackend) func(agentStartupBackendParams) (agentStartupBackendResult, error) {
	return func(p agentStartupBackendParams) (agentStartupBackendResult, error) {
		if v == modelDown {
			fmt.Fprintln(p.ErrOut, `Warning: model "example-model" is not answering (connection refused).`)
		}
		r := readyResult("example-model", b)
		r.Readiness = v
		return r, nil
	}
}

func modelStateMsgs(msgs []tea.Msg) []console.ModelState {
	var out []console.ModelState
	for _, m := range msgs {
		if s, ok := m.(console.ModelStateMsg); ok {
			out = append(out, s.State)
		}
	}
	return out
}

// No explicit model flags and nothing answers: the dead fallback is not
// bound, and the console shows the no-model state the spec promises.
func TestConsoleStartup_ImplicitDownIsNoModel(t *testing.T) {
	h := newConsoleStartupHarness(t, nil)
	backend := &stubBackend{name: "dead"}
	consoleSetupBackendFn = verdictResult(modelDown, backend)
	done := h.launch(t, defaultStartupInputs())

	h.prog.waitMsg(t, "no model status", statusIs("model", console.StatusFailed, "no model · /model or axis model start"))
	h.prog.waitMsg(t, "warning persisted as failure", persistContains(console.StatusFailed, `model "example-model" is not answering`))
	h.prog.exit()
	out := awaitOutcome(t, done, 5*time.Second)
	if out.err != nil {
		t.Fatalf("err = %v", out.err)
	}
	if got := modelStateMsgs(h.prog.all()); len(got) != 1 || got[0] != console.ModelNone {
		t.Fatalf("model states = %v, want exactly [ModelNone]", got)
	}
	if out.agent.Backend() == backend || out.agent.Model() == "example-model" {
		t.Fatal("a model that is not answering was bound on the implicit path")
	}
	if h.prog.has(func(m tea.Msg) bool {
		s, ok := m.(console.StartupStatusMsg)
		return ok && s.Line.Key == "model" && s.Line.State == console.StatusDone
	}) {
		t.Fatal("a dead model was reported as ready")
	}
}

// --model pins the model: it is bound even when down, but never called ready.
func TestConsoleStartup_ExplicitDownSaysNotAnswering(t *testing.T) {
	h := newConsoleStartupHarness(t, nil)
	backend := &stubBackend{name: "pinned"}
	consoleSetupBackendFn = verdictResult(modelDown, backend)
	in := defaultStartupInputs()
	in.model = "example-model"
	done := h.launch(t, in)

	h.prog.waitMsg(t, "not answering", statusIs("model", console.StatusFailed, "example-model selected — not answering"))
	h.prog.waitMsg(t, "model ready", modelStateIs(console.ModelReady))
	h.prog.exit()
	out := awaitOutcome(t, done, 5*time.Second)
	if out.err != nil || out.agent.Backend() != backend {
		t.Fatalf("pinned model not bound: err=%v", out.err)
	}
	if h.prog.has(statusIs("model", console.StatusDone, "example-model ready")) {
		t.Fatal("a model that is not answering was reported as ready")
	}
}

// Cold and unchecked models are bound, with a status that says what is known.
func TestConsoleStartup_ColdAndUncheckedStatus(t *testing.T) {
	for _, c := range []struct {
		v    modelReadiness
		want string
	}{
		{modelCold, "example-model selected — may still be loading"},
		{modelUnchecked, "example-model selected (not checked)"},
	} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/explicit=%v", c.v, explicit), func(t *testing.T) {
				h := newConsoleStartupHarness(t, nil)
				backend := &stubBackend{name: "b"}
				consoleSetupBackendFn = verdictResult(c.v, backend)
				in := defaultStartupInputs()
				if explicit {
					in.model = "example-model"
				}
				done := h.launch(t, in)
				h.prog.waitMsg(t, "status", statusIs("model", console.StatusInfo, c.want))
				h.prog.waitMsg(t, "model ready", modelStateIs(console.ModelReady))
				h.prog.exit()
				out := awaitOutcome(t, done, 5*time.Second)
				if out.err != nil || out.agent.Backend() != backend {
					t.Fatalf("model not bound: err=%v", out.err)
				}
				if h.prog.has(statusIs("model", console.StatusDone, "example-model ready")) {
					t.Fatal("an unproven model was reported as ready")
				}
			})
		}
	}
}

// A prompt typed with --model before the agent is bound is held, then sent
// exactly once when the model is ready, instead of being refused.
func TestConsoleStartup_PinnedPromptHeldUntilBind(t *testing.T) {
	h := newConsoleStartupHarness(t, nil)
	consoleSetupBackendFn = verdictResult(modelReady, &stubBackend{name: "pinned"})
	in := defaultStartupInputs()
	in.model = "example-model"
	done := h.launch(t, in)

	// Drive a real console built from the options the startup path chose.
	opts := h.prog.opts
	var submits atomic.Int32
	opts.Submit = func(console.TurnID, string) tea.Cmd { submits.Add(1); return nil }
	var model tea.Model = console.NewModel(opts)
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if n := submits.Load(); n != 0 {
		t.Fatalf("prompt sent %d time(s) before the agent was bound", n)
	}
	h.prog.waitMsg(t, "model ready", modelStateIs(console.ModelReady))
	for _, msg := range h.prog.all() {
		if s, ok := msg.(console.ModelStateMsg); ok {
			model, _ = model.Update(s)
		}
	}
	if n := submits.Load(); n != 1 {
		t.Fatalf("held prompt sent %d time(s) after bind, want 1", n)
	}
	h.prog.exit()
	awaitOutcome(t, done, 5*time.Second)
}

// sendHookProgram runs hook (once) when a matching message is sent, from the
// sender's goroutine, before Send returns.
type sendHookProgram struct {
	*fakeConsoleProgram
	match func(tea.Msg) bool
	hook  func()
	fired atomic.Bool
}

func (p *sendHookProgram) Send(m tea.Msg) {
	p.fakeConsoleProgram.Send(m)
	if p.match(m) && p.fired.CompareAndSwap(false, true) {
		p.hook()
	}
}

// An operator /model that lands after bind but before startup resolution
// begins must win: startup may not capture the operator's generation.
func TestConsoleStartup_SwitchBetweenBindAndResolveWins(t *testing.T) {
	hn := pickerTestSetup(t)
	rt := pickerTestRuntime(hn)
	h := newConsoleStartupHarness(t, func(context.Context) (*runtimectx.Context, error) { return rt, nil })
	late := &stubBackend{name: "late"}
	consoleSetupBackendFn = func(agentStartupBackendParams) (agentStartupBackendResult, error) {
		return readyResult("example-late", late), nil
	}
	var switchErr atomic.Value
	wrapped := &sendHookProgram{fakeConsoleProgram: h.prog}
	wrapped.match = func(m tea.Msg) bool { rc, ok := m.(console.RunCommandMsg); return ok && rc.Text == "/model" }
	wrapped.hook = func() {
		msg := wrapped.opts.Submit(1, "/model big-27b")()
		if d, ok := msg.(console.TurnDoneMsg); !ok || d.Err != nil {
			switchErr.Store(fmt.Sprintf("%+v", msg))
		}
	}
	newConsoleProgramFn = func(_ context.Context, opts console.Options) consoleProgram {
		h.programs.Add(1)
		h.prog.opts = opts
		return wrapped
	}
	in := defaultStartupInputs()
	in.selectModel = true
	done := h.launch(t, in)

	h.prog.waitMsg(t, "kept choice", statusIs("model", console.StatusInfo, "kept /model choice"))
	h.prog.exit()
	out := awaitOutcome(t, done, 5*time.Second)
	if v := switchErr.Load(); v != nil {
		t.Fatalf("/model switch failed: %v", v)
	}
	if out.agent.Backend() == late || out.agent.Model() != "big-27b" {
		t.Fatalf("startup overwrote the operator's model: %q", out.agent.Model())
	}
}

// Quitting while model resolution is stuck still closes MCP inside the
// shutdown bound: the stdio servers must not outlive axis.
func TestConsoleStartup_QuitDuringResolutionClosesMCP(t *testing.T) {
	h := newConsoleStartupHarness(t, func(context.Context) (*runtimectx.Context, error) { return mcpRuntime(), nil })
	consoleConnectMCPFn = func(_ context.Context, reg *mcpclient.Registry, _ *config.Config) {
		reg.Add(&mcpclient.ServerConnection{Name: "srv-a", InitResult: &mcp.InitializeResult{}})
	}
	res := newBlockingResolver(readyResult("example-model", &stubBackend{name: "b"}), nil)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(res.release) }) }
	t.Cleanup(release)
	consoleSetupBackendFn = res.fn
	done := h.launch(t, defaultStartupInputs())
	res.awaitCalled(t)

	h.prog.exit()
	deadline := time.Now().Add(consoleStartupShutdownWait)
	for !h.mcpClosed.Load() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if !h.mcpClosed.Load() {
		t.Fatal("MCP registry not closed within the shutdown bound while resolution was stuck")
	}
	// Let the job finish before the harness restores the package seams.
	release()
	awaitOutcome(t, done, consoleStartupShutdownWait+time.Second)
	if h.prog.has(modelStateIs(console.ModelReady)) {
		t.Fatal("a model was bound after the program exited")
	}
}

// A panic in MCP connect after a server was started still closes it.
func TestConsoleStartup_ConnectPanicStillClosesMCP(t *testing.T) {
	h := newConsoleStartupHarness(t, func(context.Context) (*runtimectx.Context, error) { return mcpRuntime(), nil })
	var closes atomic.Int32
	consoleCloseMCPFn = func(*mcpclient.Registry) { closes.Add(1); h.mcpClosed.Store(true) }
	consoleConnectMCPFn = func(_ context.Context, reg *mcpclient.Registry, _ *config.Config) {
		reg.Add(&mcpclient.ServerConnection{Name: "srv-a", InitResult: &mcp.InitializeResult{}})
		panic("mcp exploded mid-connect")
	}
	done := h.launch(t, defaultStartupInputs())
	h.prog.waitMsg(t, "failure persisted", persistContains(console.StatusFailed, "mcp exploded mid-connect"))
	h.prog.exit()
	awaitOutcome(t, done, consoleStartupShutdownWait+time.Second)
	if n := closes.Load(); n != 1 {
		t.Fatalf("MCP registry closed %d times after a connect panic, want 1", n)
	}
}
