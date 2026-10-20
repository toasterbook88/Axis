package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/console"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func TestLauncher_CommandBeforeBind(t *testing.T) {
	for _, text := range []string{"/status", "!echo hi", "/model", "hello"} {
		t.Run(text, func(t *testing.T) {
			l := newStartupConsoleLauncher(time.Minute, consoleClock)
			rec := &capture{}
			l.prog = rec
			msg := runCmd(t, l.submit(context.Background())(7, text))
			done, ok := msg.(console.TurnDoneMsg)
			if !ok || done.Turn != 7 || done.Err != nil {
				t.Fatalf("want clean TurnDoneMsg for turn 7, got %+v", msg)
			}
			found := false
			for _, m := range rec.all() {
				if e, ok := m.(console.EntryMsg); ok {
					if n, ok := e.Entry.(*console.NoticeEntry); ok && n.Text == "agent still starting — try again in a moment" {
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("missing not-ready notice: %s", describeMsgs(rec.all()))
			}
		})
	}
}

func TestLauncher_BindIsRaceFree(t *testing.T) {
	l := newStartupConsoleLauncher(time.Minute, consoleClock)
	l.prog = &capture{}
	l.loader = func(context.Context) (*runtimectx.Context, error) {
		return &runtimectx.Context{Config: &config.Config{}}, nil
	}
	a := agent.New(agent.Config{Model: "example-model", MaxTokens: 4096, Backend: &stubBackend{name: "b"}})
	submit := l.submit(context.Background())

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				msg := submit(console.TurnID(g*100+i), "/autonomy")()
				if _, ok := msg.(console.TurnDoneMsg); !ok {
					t.Errorf("want TurnDoneMsg, got %T", msg)
				}
				_ = l.headerModel()
			}
		}(g)
	}
	l.bind(a, nil, ModelChoice{Model: "example-model"})
	wg.Wait()
	if got := l.boundAgent(); got != a {
		t.Fatal("bind did not record the agent")
	}
	if got := l.headerModel(); got != "example-model" {
		t.Fatalf("headerModel = %q", got)
	}
}

func TestLauncher_ApplyStartupModelGeneration(t *testing.T) {
	l := newStartupConsoleLauncher(time.Minute, consoleClock)
	rec := &capture{}
	l.prog = rec

	gen := l.currentModelGen()
	applied, err := l.applyStartupModel(gen, func() error { return nil })
	if !applied || err != nil {
		t.Fatalf("matching generation: applied=%v err=%v", applied, err)
	}
	if _, err := l.applyStartupModel(gen, func() error { return errors.New("build failed") }); err == nil {
		t.Fatal("apply error was swallowed")
	}

	if err := l.commitOperatorModel(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !rec.has(modelStateIs(console.ModelReady)) {
		t.Fatal("operator switch did not mark the model ready")
	}
	called := false
	applied, err = l.applyStartupModel(gen, func() error { called = true; return nil })
	if applied || err != nil || called {
		t.Fatalf("stale generation must be discarded: applied=%v err=%v called=%v", applied, err, called)
	}

	// A failed operator switch changes nothing, so startup may still apply.
	gen = l.currentModelGen()
	if err := l.commitOperatorModel(func() error { return errors.New("unreachable") }); err == nil {
		t.Fatal("operator switch error was swallowed")
	}
	if applied, _ := l.applyStartupModel(gen, func() error { return nil }); !applied {
		t.Fatal("a failed operator switch must not retire the startup result")
	}
}

func TestLauncher_StartupTunnelYieldsToOperator(t *testing.T) {
	t.Cleanup(func() { installModelTunnel(nil) })
	l := newStartupConsoleLauncher(time.Minute, consoleClock)
	l.prog = &capture{}

	var operatorClosed, startupClosed atomic.Bool
	gen := l.currentModelGen()
	if err := l.commitOperatorModel(func() error {
		installModelTunnel(func() { operatorClosed.Store(true) })
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	install := startupTunnelInstaller(withStartupTunnelInstaller(context.Background(), l.startupTunnelInstall(gen)))
	install(func() { startupClosed.Store(true) })
	if !startupClosed.Load() {
		t.Fatal("a stale startup tunnel must be closed")
	}
	if operatorClosed.Load() {
		t.Fatal("a stale startup tunnel closed the operator's tunnel")
	}

	// Without an installer in the context the resolver keeps today's behavior.
	var plainClosed atomic.Bool
	startupTunnelInstaller(context.Background())(func() { plainClosed.Store(true) })
	if !operatorClosed.Load() {
		t.Fatal("default installer must replace the active tunnel")
	}
	installModelTunnel(nil)
	if !plainClosed.Load() {
		t.Fatal("default installer did not install the tunnel")
	}
}

func TestConsoleFleet_FooterNeverWaitsOnLoader(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var calls atomic.Int32
	f := newConsoleFleet(context.Background(), func(context.Context) (*runtimectx.Context, error) {
		calls.Add(1)
		<-release
		return nil, errors.New("released")
	})
	if got := f.footerLabel(); got != "" {
		t.Fatalf("unprimed footer label = %q, want empty", got)
	}
	f.prime(nil, errors.New("no cache"))
	f.mu.Lock()
	f.checkedAt = time.Now().Add(-time.Minute) // stale: the next read refreshes
	f.mu.Unlock()

	start := time.Now()
	for i := 0; i < 5; i++ {
		if got := f.footerLabel(); got != "no snapshot" {
			t.Fatalf("footer label = %q, want no snapshot", got)
		}
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("footer waited on the loader for %s", elapsed)
	}
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("loader called %d times, want one background refresh", n)
	}
}
