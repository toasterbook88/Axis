package main

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

// A tunnel opened by a probe that finished after startup was cancelled is
// closed, never installed.
func TestStartupReady_CancelledAfterProbeClosesTunnel(t *testing.T) {
	prevReady, prevRoute := startupReadyFn, startupRouteFn
	t.Cleanup(func() { startupReadyFn, startupRouteFn = prevReady, prevRoute })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var closed, installed atomic.Bool
	startupRouteFn = func(_ context.Context, _ *runtimectx.Context, c ModelChoice) (ModelChoice, func(), error) {
		return c, func() { closed.Store(true) }, nil
	}
	startupReadyFn = func(context.Context, ModelChoice, agent.CloudBackendOptions) (modelReadiness, string) {
		cancel() // the operator quits while the probe is in flight
		return modelReady, ""
	}
	install := func(f func()) {
		if f != nil {
			installed.Store(true)
		}
	}
	_, _, _, err := resolveReadyStartupModelTarget(withStartupTunnelInstaller(ctx, install), "good:1b", "auto", "", nil, readyRuntime(), readyTestChoices(), false, &bytes.Buffer{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if installed.Load() || !closed.Load() {
		t.Fatalf("tunnel installed=%v closed=%v, want closed and not installed", installed.Load(), closed.Load())
	}
}

// The resolver reports the probe's verdict alongside the target.
func TestStartupReadyReportsVerdict(t *testing.T) {
	for _, c := range []struct {
		name    string
		verdict map[string]modelReadiness
		pinned  bool
		choices []ModelChoice
		model   string
		want    modelReadiness
	}{
		{"ready", nil, false, readyTestChoices(), "good:1b", modelReady},
		{"cold", map[string]modelReadiness{"dead-default": modelCold}, false, readyTestChoices(), "dead-default", modelCold},
		{"pinned down", map[string]modelReadiness{"dead-default": modelDown}, true, readyTestChoices(), "dead-default", modelDown},
		{"cloud proxy", nil, false, []ModelChoice{{ID: "w:ollama:big:cloud", Model: "big:cloud", Protocol: agent.ProtocolOllama, ProviderName: "ollama", ProviderKind: "local", CloudProxy: true, Endpoint: "http://localhost:11434"}}, "big:cloud", modelUnchecked},
	} {
		t.Run(c.name, func(t *testing.T) {
			stubStartupReady(t, c.verdict)
			_, _, got, err := resolveReadyStartupModelTarget(context.Background(), c.model, "auto", "", nil, readyRuntime(), c.choices, c.pinned, &bytes.Buffer{})
			if err != nil || got != c.want {
				t.Fatalf("verdict = %v err %v, want %v", got, err, c.want)
			}
		})
	}
	var zero agentStartupBackendResult
	if zero.Readiness != modelUnchecked {
		t.Fatal("an unset verdict must read as unchecked, never ready")
	}
}
