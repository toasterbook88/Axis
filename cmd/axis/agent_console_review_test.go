package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/console"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func cheapRoutingRuntime(t *testing.T) *runtimectx.Context {
	t.Helper()
	t.Setenv("AXIS_TEST_CLOUD_ROUTING", "test-key")
	return &runtimectx.Context{Config: &config.Config{AIProviders: map[string]config.AIProviderConfig{
		"anthropic": {
			Enabled: true, Type: "cloud", Kind: "anthropic", Priority: 50,
			APIKeyEnv: "AXIS_TEST_CLOUD_ROUTING",
			Models: []config.AIModelConfig{
				{Name: "claude-3-opus", CostPer1K: 0.015},
				{Name: "claude-3-haiku", CostPer1K: 0.00025},
			},
		},
	}}}
}

// routeConcurrently drives two ChatStream calls at once, as parallel subagent
// calls do. The context is already cancelled, so no request leaves the host;
// the routing trace is written before the backend call.
func routeConcurrently(t *testing.T, hide bool) string {
	t.Helper()
	var errW bytes.Buffer
	res, err := setupAgentStartupBackend(agentStartupBackendParams{
		Provider:         "cloud",
		CloudModel:       "claude-3-opus",
		CheapModel:       "claude-3-haiku",
		Verbose:          true,
		RT:               cheapRoutingRuntime(t),
		ErrOut:           &errW,
		HideRoutingTrace: hide,
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	startup := errW.String()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 1
	if hide {
		calls = 2 // only safe concurrently when nothing writes the shared buffer
	}
	var wg sync.WaitGroup
	for range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = res.Backend.ChatStream(ctx, []chat.Message{{Role: "user", Content: "status"}}, nil, nil)
		}()
	}
	wg.Wait()
	return startup + "\n--after--\n" + strings.TrimPrefix(errW.String(), startup)
}

// In the console the startup notes buffer is read once; routing traces must
// not keep writing into it for the session (a data race under parallel
// subagent calls, and output nobody sees). The operator is told instead.
func TestConsoleSetup_RoutingTraceNotWrittenToStartupNotes(t *testing.T) {
	out := routeConcurrently(t, true)
	before, after, _ := strings.Cut(out, "--after--")
	if !strings.Contains(before, "Routing traces are not shown in the console; use --plain to see them.") {
		t.Errorf("missing console routing-trace notice: %q", before)
	}
	if strings.TrimSpace(after) != "" {
		t.Errorf("routing trace written after startup: %q", after)
	}
}

// The plain REPL keeps today's behavior: traces go to ErrOut.
func TestPlainSetup_RoutingTraceStillWritten(t *testing.T) {
	out := routeConcurrently(t, false)
	_, after, _ := strings.Cut(out, "--after--")
	if !strings.Contains(after, "routing") {
		t.Errorf("plain path lost its routing trace: %q", after)
	}
}

// Opening the console and quitting without a turn must not overwrite the
// saved agent history with an empty conversation (bare `axis` is now the
// front door, so look-and-quit is common). The plain REPL has the same guard.
func TestConsoleStartup_QuitWithoutTurnKeepsHistory(t *testing.T) {
	h := newConsoleStartupHarness(t, nil)
	conv := chat.NewConversation(32768)
	conv.Append(chat.Message{Role: chat.RoleUser, Content: "earlier question"})
	conv.Append(chat.Message{Role: chat.RoleAssistant, Content: "earlier answer"})
	path := chat.PersistPath("agent")
	if err := conv.SaveToFile(path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	consoleSetupBackendFn = func(agentStartupBackendParams) (agentStartupBackendResult, error) {
		return readyResult("example-model", &stubBackend{name: "b"}), nil
	}
	done := h.launch(t, defaultStartupInputs())
	h.prog.waitMsg(t, "model ready", modelStateIs(console.ModelReady))
	h.prog.exit()
	if out := awaitOutcome(t, done, 5*time.Second); out.err != nil || out.agent == nil {
		t.Fatalf("startup outcome: agent=%v err=%v", out.agent, out.err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("history overwritten by an unused session:\nbefore %s\nafter  %s", before, after)
	}
}

// --live is an SSH sweep with a per-node timeout of its own; it must not be
// cut off by the cached-snapshot bound.
func TestConsoleRuntimeSource_LiveGetsLiveBound(t *testing.T) {
	if _, wait := consoleRuntimeSource(false); wait != consoleRuntimeLoadWait {
		t.Errorf("cached wait = %s, want %s", wait, consoleRuntimeLoadWait)
	}
	_, wait := consoleRuntimeSource(true)
	if wait != consoleLiveRuntimeLoadWait || wait < 2*defaultNodeTimeoutForTest {
		t.Errorf("live wait = %s, want %s and at least twice the default node timeout", wait, consoleLiveRuntimeLoadWait)
	}
}

const defaultNodeTimeoutForTest = 10 * time.Second

// A loader that honours its context and returns what it reached just after
// the deadline (LoadLive does) hands back that partial result.
func TestLoadRuntimeBounded_KeepsPartialResultAfterDeadline(t *testing.T) {
	partial := &runtimectx.Context{Config: &config.Config{}}
	loader := func(ctx context.Context) (*runtimectx.Context, error) {
		<-ctx.Done()
		time.Sleep(10 * time.Millisecond) // finishing the nodes it reached
		return partial, nil
	}
	rt, err := loadRuntimeBounded(context.Background(), loader, 20*time.Millisecond)
	if err != nil || rt != partial {
		t.Fatalf("got rt=%v err=%v, want the partial context", rt, err)
	}
}

// A loader that ignores its context is still cut off at wait plus the grace.
func TestLoadRuntimeBounded_StuckLoaderStillBounded(t *testing.T) {
	prev := consoleRuntimePartialGrace
	consoleRuntimePartialGrace = 20 * time.Millisecond
	t.Cleanup(func() { consoleRuntimePartialGrace = prev })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	loader := func(context.Context) (*runtimectx.Context, error) {
		<-release
		return nil, nil
	}
	start := time.Now()
	rt, err := loadRuntimeBounded(context.Background(), loader, 20*time.Millisecond)
	if rt != nil || err == nil || !strings.Contains(err.Error(), "timed out after") {
		t.Fatalf("got rt=%v err=%v, want a timeout", rt, err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("stuck loader held the caller %s", elapsed)
	}
}

// Quitting during the grace returns at once with the cancellation.
func TestLoadRuntimeBounded_CancelDuringGrace(t *testing.T) {
	prev := consoleRuntimePartialGrace
	consoleRuntimePartialGrace = time.Minute
	t.Cleanup(func() { consoleRuntimePartialGrace = prev })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(context.Background())
	loader := func(context.Context) (*runtimectx.Context, error) {
		<-release
		return nil, nil
	}
	go func() {
		time.Sleep(60 * time.Millisecond) // past the 20ms deadline, inside the grace
		cancel()
	}()
	start := time.Now()
	if _, err := loadRuntimeBounded(ctx, loader, 20*time.Millisecond); err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancel during grace took %s", elapsed)
	}
}
