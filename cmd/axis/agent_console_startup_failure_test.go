package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/console"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func TestConsoleStartup_BranchLookupOffCriticalPath(t *testing.T) {
	h := newConsoleStartupHarness(t, nil)
	lookupStarted := make(chan struct{})
	var lookupReturned atomic.Bool
	consoleGitBranchFn = func(ctx context.Context) (string, error) {
		close(lookupStarted)
		<-ctx.Done() // a git that never answers
		lookupReturned.Store(true)
		return "", ctx.Err()
	}
	res := newBlockingResolver(readyResult("example-model", &stubBackend{name: "b"}), nil)
	consoleSetupBackendFn = res.fn
	done := h.launch(t, defaultStartupInputs())

	select {
	case <-lookupStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("branch lookup never started")
	}
	res.awaitCalled(t) // model resolution runs after bind
	if lookupReturned.Load() {
		t.Fatal("lookup returned early; the test did not hold it blocked")
	}
	msg := runCmd(t, h.prog.opts.Submit(1, "/autonomy"))
	if d, ok := msg.(console.TurnDoneMsg); !ok || d.Err != nil || h.prog.has(noticeContains("agent still starting")) {
		t.Fatalf("agent not bound while the branch lookup was blocked: %+v", msg)
	}

	close(res.release)
	h.prog.exit()
	out := awaitOutcome(t, done, consoleStartupShutdownWait+time.Second)
	if out.err != nil {
		t.Fatalf("err = %v", out.err)
	}
	if strings.Contains(h.errW.String(), "startup still cancelling") {
		t.Fatalf("a blocked branch lookup held shutdown: %q", h.errW.String())
	}
	if !lookupReturned.Load() {
		t.Fatal("job exited without waiting for its branch lookup")
	}
}

func TestConsoleStartup_BranchAddedToCwdLine(t *testing.T) {
	h := newConsoleStartupHarness(t, nil)
	consoleGitBranchFn = func(context.Context) (string, error) { return "feature-x", nil }
	consoleSetupBackendFn = func(agentStartupBackendParams) (agentStartupBackendResult, error) {
		return readyResult("example-model", &stubBackend{name: "b"}), nil
	}
	done := h.launch(t, defaultStartupInputs())
	h.prog.waitMsg(t, "cwd with branch", func(m tea.Msg) bool {
		s, ok := m.(console.StartupStatusMsg)
		return ok && s.Line.Key == "cwd" && s.Line.State == console.StatusInfo && strings.HasSuffix(s.Line.Text, " (feature-x)")
	})
	h.prog.exit()
	awaitOutcome(t, done, 5*time.Second)
}

func TestConsoleStartup_PanicBeforeBindFailsHonestly(t *testing.T) {
	h := newConsoleStartupHarness(t, func(context.Context) (*runtimectx.Context, error) { return mcpRuntime(), nil })
	consoleConnectMCPFn = func(context.Context, *mcpclient.Registry, *config.Config) { panic("mcp exploded") }
	done := h.launch(t, defaultStartupInputs())

	h.prog.waitMsg(t, "model none", modelStateIs(console.ModelNone))
	h.prog.waitMsg(t, "failure persisted", persistContains(console.StatusFailed, "mcp exploded"))
	const want = "agent failed to start: mcp exploded — restart axis agent"
	for i, cmd := range []string{"/status", "/model"} {
		turn := console.TurnID(10 + i)
		start := time.Now()
		msg := runCmd(t, h.prog.opts.Submit(turn, cmd))
		if d, ok := msg.(console.TurnDoneMsg); !ok || d.Turn != turn {
			t.Fatalf("%s: want TurnDoneMsg for turn %d, got %+v", cmd, turn, msg)
		}
		if time.Since(start) > time.Second {
			t.Fatalf("%s took %s", cmd, time.Since(start))
		}
		found := false
		for _, m := range h.prog.all() {
			if e, ok := m.(console.EntryMsg); ok && e.Turn == turn {
				if n, ok := e.Entry.(*console.NoticeEntry); ok && n.Text == want {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("%s: missing failed-start notice: %s", cmd, describeMsgs(h.prog.all()))
		}
	}
	if h.prog.has(noticeContains("agent still starting")) {
		t.Fatal("a failed start must not claim the agent is still starting")
	}
	if n := countMsgs(h.prog.all(), func(m tea.Msg) bool { _, ok := m.(console.ModelStateMsg); return ok }); n != 1 {
		t.Fatalf("want exactly one ModelStateMsg, got %d", n)
	}
	h.prog.exit()
	if out := awaitOutcome(t, done, 5*time.Second); out.err != nil || out.agent != nil {
		t.Fatalf("outcome = %+v, want nil agent and nil error", out)
	}
}
