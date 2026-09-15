package main

import (
	"testing"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

// Regression for the v0.18.1 fleet incident: nodes.yaml default_model named a model
// that was not on any disk. resolveStartupModelTarget (auto mode) synthesized a local
// Ollama target for it without consulting the daemon, producing a guaranteed-failing
// session — while the live catalog held perfectly usable models. The startup path
// must check Ollama /api/tags before honoring a synthetic target, and fall through
// to the live catalog when the configured default is absent from disk.
func TestStartupModelStaleDefaultFallsBackToLiveCatalog(t *testing.T) {
	origProbe := probeEndpointFn
	origHas := ollamaHasModelFn
	probeEndpointFn = func(string) bool { return true }
	ollamaHasModelFn = func(name string) bool { return name == "qwen3.8-9b" }
	defer func() {
		probeEndpointFn = origProbe
		ollamaHasModelFn = origHas
	}()

	rt := &runtimectx.Context{}
	// Catalog holds one usable local model and no cloud fallbacks.
	choices := []ModelChoice{{
		ID:           "cranium:ollama:qwen3.8-9b",
		Model:        "qwen3.8-9b",
		Protocol:     agent.ProtocolOllama,
		ProviderName: "ollama",
		ProviderKind: "local",
	}}

	got, _, err := resolveStartupModelTarget("swiftsmith-v8-phase6-q4:latest", "", "", nil, rt, choices)
	if err != nil {
		t.Fatalf("resolveStartupModelTarget: %v", err)
	}
	if got.Model != "qwen3.8-9b" {
		t.Fatalf("stale default_model %q was honored; want live-catalog fallback to qwen3.8-9b, got %q", "swiftsmith-v8-phase6-q4:latest", got.Model)
	}
}

// The explicit-operator-name path must still honor a name that IS on disk even when
// the snapshot catalog is stale (that is the whole point of the synthetic target).
func TestStartupModelExplicitNameOnDiskStillHonored(t *testing.T) {
	origHas := ollamaHasModelFn
	ollamaHasModelFn = func(name string) bool { return name == "fresh-pull:latest" }
	defer func() { ollamaHasModelFn = origHas }()

	rt := &runtimectx.Context{}
	choices := []ModelChoice{} // catalog knows nothing

	got, _, err := resolveStartupModelTarget("fresh-pull:latest", "local", "", nil, rt, choices)
	if err != nil {
		t.Fatalf("resolveStartupModelTarget: %v", err)
	}
	if got.Model != "fresh-pull:latest" {
		t.Fatalf("explicit on-disk model not honored: got %q", got.Model)
	}
}
