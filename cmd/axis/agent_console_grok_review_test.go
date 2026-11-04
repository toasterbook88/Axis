package main

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/console"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/publication"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

// --resume paints before the agent exists, so the up-arrow ring must be
// seeded from the saved conversation itself: one-shot and plain-REPL prompts
// are only in that file, never in the console's prompt history.
func TestConsoleStartup_ResumeSeedsUpArrowFromConversation(t *testing.T) {
	h := newConsoleStartupHarness(t, nil)
	conv := chat.NewConversation(32768)
	conv.Append(chat.Message{Role: chat.RoleUser, Content: "question from a one-shot run"})
	conv.Append(chat.Message{Role: chat.RoleAssistant, Content: "answer"})
	if err := conv.SaveToFile(chat.PersistPath("agent")); err != nil {
		t.Fatal(err)
	}
	res := newBlockingResolver(readyResult("example-model", &stubBackend{name: "b"}), nil)
	consoleSetupBackendFn = res.fn
	in := defaultStartupInputs()
	in.resume = true
	done := h.launch(t, in)
	found := false
	for _, p := range h.prog.opts.History {
		if p == "question from a one-shot run" {
			found = true
		}
	}
	if !found {
		t.Errorf("resumed prompt missing from the up-arrow ring: %q", h.prog.opts.History)
	}
	close(res.release)
	h.prog.exit()
	awaitOutcome(t, done, 5*time.Second)
}

func liveRuntime(id string, at time.Time) *runtimectx.Context {
	return &runtimectx.Context{Snapshot: &models.ClusterSnapshot{
		Timestamp:   at,
		Publication: &models.PublicationEnvelope{ID: id, Source: publication.SourceLiveRuntime, AssembledAt: at},
	}}
}

// A --live prime must not be replaced by a worse cached read: a failed
// load or the bootstrap skeleton would make the footer contradict the live
// status line. A newer published snapshot may still replace it.
func TestConsoleFleet_LivePrimeNotDowngraded(t *testing.T) {
	at := time.Now().Add(-time.Minute)
	f := newConsoleFleet(t.Context(), nil)
	f.prime(liveRuntime("pub-live", at), nil)

	f.mu.Lock()
	f.refreshLocked(nil, errors.New("load reservation ledger: corrupt"))
	f.mu.Unlock()
	if got := f.footerLabel(); got != "snap pub-live" {
		t.Fatalf("after failed refresh: %q, want the live prime kept", got)
	}

	boot := &runtimectx.Context{Snapshot: &models.ClusterSnapshot{
		Timestamp:   time.Now(),
		Publication: &models.PublicationEnvelope{ID: "pub-boot", Source: "bootstrap", AssembledAt: time.Now()},
	}}
	f.mu.Lock()
	f.refreshLocked(boot, nil)
	f.mu.Unlock()
	if got := f.footerLabel(); got != "snap pub-live" {
		t.Fatalf("after bootstrap refresh: %q, want the live prime kept", got)
	}

	newer := &runtimectx.Context{Snapshot: &models.ClusterSnapshot{
		Timestamp:   time.Now(),
		Publication: &models.PublicationEnvelope{ID: "pub-daemon", Source: publication.SourceDaemonCache, AssembledAt: time.Now()},
	}}
	f.mu.Lock()
	f.refreshLocked(newer, nil)
	f.mu.Unlock()
	if got := f.footerLabel(); got != "snap pub-daemon" {
		t.Fatalf("after newer daemon snapshot: %q, want it adopted", got)
	}
}

// Without a live prime, refresh keeps today's behaviour: a failure shows.
func TestConsoleFleet_CachedPrimeStillReportsFailure(t *testing.T) {
	f := newConsoleFleet(t.Context(), nil)
	f.prime(&runtimectx.Context{Snapshot: &models.ClusterSnapshot{
		Timestamp:   time.Now(),
		Publication: &models.PublicationEnvelope{ID: "pub-c", Source: publication.SourceDaemonCache, AssembledAt: time.Now()},
	}}, nil)
	f.mu.Lock()
	f.refreshLocked(nil, errors.New("boom"))
	f.mu.Unlock()
	if got := f.footerLabel(); got != "no snapshot" {
		t.Fatalf("cached prime after failure: %q, want no snapshot", got)
	}
}

// A startup model rejected as down must not keep the SSH tunnel its
// readiness check opened: nothing will ever use it.
func TestConsoleStartup_DownModelTunnelClosed(t *testing.T) {
	h := newConsoleStartupHarness(t, nil)
	t.Cleanup(func() { installModelTunnel(nil) })
	var closed atomic.Bool
	consoleSetupBackendFn = func(p agentStartupBackendParams) (agentStartupBackendResult, error) {
		startupTunnelInstaller(p.Ctx)(func() { closed.Store(true) })
		r := readyResult("example-model", &stubBackend{name: "dead"})
		r.Readiness = modelDown
		return r, nil
	}
	done := h.launch(t, defaultStartupInputs())
	h.prog.waitMsg(t, "no model", modelStateIs(console.ModelNone))
	if !closed.Load() {
		t.Error("tunnel for the rejected down model is still open")
	}
	h.prog.exit()
	awaitOutcome(t, done, 5*time.Second)
}

// With pinned flags, an operator /model that lands between bind and the
// prelude's settle wins: the splash must not then call the prelude model ready.
func TestConsoleStartup_PreludeSettleRespectsOperatorSwitch(t *testing.T) {
	hn := pickerTestSetup(t)
	rt := pickerTestRuntime(hn)
	h := newConsoleStartupHarness(t, func(context.Context) (*runtimectx.Context, error) { return rt, nil })
	consoleSetupBackendFn = func(agentStartupBackendParams) (agentStartupBackendResult, error) {
		return readyResult("example-late", &stubBackend{name: "pinned"}), nil
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
	in.model = "example-late"
	in.selectModel = true
	done := h.launch(t, in)

	h.prog.waitMsg(t, "kept choice", statusIs("model", console.StatusInfo, "kept /model choice"))
	h.prog.exit()
	out := awaitOutcome(t, done, 5*time.Second)
	if v := switchErr.Load(); v != nil {
		t.Fatalf("/model switch failed: %v", v)
	}
	if h.prog.has(statusIs("model", console.StatusDone, "example-late ready")) {
		t.Fatal("prelude model called ready after the operator switched")
	}
	if out.agent.Model() != "big-27b" {
		t.Fatalf("operator's model lost: %q", out.agent.Model())
	}
}
