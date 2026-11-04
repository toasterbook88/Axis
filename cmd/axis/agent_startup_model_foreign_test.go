package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func stubForeignStartup(t *testing.T, role string) {
	t.Helper()
	prevHas := ollamaHasModelFn
	prevRole := modelFromAIRoleFn
	ollamaHasModelFn = func(string) bool { return false }
	modelFromAIRoleFn = func(string) string { return role }
	t.Cleanup(func() {
		ollamaHasModelFn = prevHas
		modelFromAIRoleFn = prevRole
	})
}

func foreignOllamaChoice() ModelChoice {
	return ModelChoice{
		ID:           "peer:ollama:sentinel:latest",
		Model:        "sentinel:latest",
		Protocol:     agent.ProtocolOllama,
		ProviderName: "ollama",
		ProviderKind: "local",
		Node:         "peer",
		Endpoint:     "http://peer.example:11434",
	}
}

func hubChoice(model string, disabled bool) ModelChoice {
	c := ModelChoice{
		ID:           "ai-backend:local-hub:" + model,
		Model:        model,
		Protocol:     agent.ProtocolOpenAI,
		ProviderName: "ai-backend:local-hub",
		ProviderKind: "local",
		Endpoint:     "http://hub.example/v1",
	}
	if disabled {
		c.Disabled = true
		c.DisabledReason = "not answering"
	}
	return c
}

// A startup name that is not a usable catalog choice must not borrow another
// node's Ollama when an enabled hub role already probed clean.
func TestStartupDoesNotBorrowForeignOllamaWhenHubRoleProbedClean(t *testing.T) {
	stubForeignStartup(t, "other-9b")
	choices := []ModelChoice{
		foreignOllamaChoice(),
		hubChoice("served-9b", false),
		hubChoice("other-9b", false),
	}

	got, _, err := resolveStartupModelTarget("Weight-File-Q4", "", "", nil, &runtimectx.Context{}, choices)
	if err != nil {
		t.Fatalf("resolveStartupModelTarget: %v", err)
	}
	if got.Model != "other-9b" || got.Protocol != agent.ProtocolOpenAI || got.Node != "" {
		t.Fatalf("got model %q protocol %q node %q, want enabled hub role other-9b", got.Model, got.Protocol, got.Node)
	}
}

// Same-node Ollama stays ahead of the hub even when a foreign Ollama sorts
// earlier by model name. The guard is only the foreign borrow.
func TestStartupSameNodeOllamaStillPrecedesHub(t *testing.T) {
	stubForeignStartup(t, "served-9b")
	foreignAlpha := foreignOllamaChoice()
	foreignAlpha.ID = "peer:ollama:alpha:7b"
	foreignAlpha.Model = "alpha:7b"
	local := ModelChoice{
		ID:           "here:ollama:mid-9b",
		Model:        "mid-9b",
		Protocol:     agent.ProtocolOllama,
		ProviderName: "ollama",
		ProviderKind: "local",
		Endpoint:     "http://127.0.0.1:11434",
	}
	got, _, err := resolveStartupModelTarget("Weight-File-Q4", "", "", nil, &runtimectx.Context{},
		[]ModelChoice{foreignAlpha, local, hubChoice("served-9b", false)})
	if err != nil {
		t.Fatalf("resolveStartupModelTarget: %v", err)
	}
	if got.Model != "mid-9b" || got.Node != "" {
		t.Fatalf("got model %q node %q, want same-node Ollama mid-9b", got.Model, got.Node)
	}
}

// Naming the foreign model is operator intent. A clean hub must not override it.
func TestStartupNamedForeignOllamaStaysWhenRequested(t *testing.T) {
	stubForeignStartup(t, "served-9b")
	got, _, err := resolveStartupModelTarget("sentinel:latest", "", "", nil, &runtimectx.Context{}, []ModelChoice{foreignOllamaChoice(), hubChoice("served-9b", false)})
	if err != nil {
		t.Fatalf("resolveStartupModelTarget: %v", err)
	}
	if got.Model != "sentinel:latest" || got.Node != "peer" {
		t.Fatalf("got model %q node %q, want the named foreign Ollama", got.Model, got.Node)
	}
}

// When no enabled hub role answered, the foreign borrow may still happen, and
// the notice has to say that. It must not call the borrow a casual substitution.
func TestStartupForeignOllamaBorrowNamesTheBorrow(t *testing.T) {
	stubForeignStartup(t, "")
	prevReady := startupReadyFn
	startupReadyFn = func(context.Context, ModelChoice, agent.CloudBackendOptions) (modelReadiness, string) {
		return modelReady, ""
	}
	t.Cleanup(func() { startupReadyFn = prevReady })

	var notes bytes.Buffer
	got, _, _, err := resolveReadyStartupModelTarget(
		context.Background(), "Weight-File-Q4", "auto", "", nil, readyRuntime(),
		[]ModelChoice{foreignOllamaChoice(), hubChoice("served-9b", true)},
		false, &notes,
	)
	if err != nil {
		t.Fatalf("resolveReadyStartupModelTarget: %v", err)
	}
	if got.Model != "sentinel:latest" || got.Node != "peer" {
		t.Fatalf("got model %q node %q, want the foreign Ollama when the hub role is down", got.Model, got.Node)
	}
	text := notes.String()
	if !strings.Contains(text, "Weight-File-Q4") || !strings.Contains(text, "another node's Ollama") || !strings.Contains(text, "sentinel:latest") {
		t.Fatalf("notice %q, want the startup name, the borrowed model, and another node's Ollama", text)
	}
	if strings.Contains(text, "Using model") {
		t.Fatalf("notice %q describes the borrow as a casual substitution", text)
	}
}

// An explicit interactive pick of another node's Ollama is operator intent, not a borrow.
func TestStartupExplicitForeignOllamaDoesNotWarnAsBorrow(t *testing.T) {
	stubForeignStartup(t, "")
	prevReady := startupReadyFn
	startupReadyFn = func(context.Context, ModelChoice, agent.CloudBackendOptions) (modelReadiness, string) {
		return modelReady, ""
	}
	t.Cleanup(func() { startupReadyFn = prevReady })

	explicit := foreignOllamaChoice()
	var notes bytes.Buffer
	got, _, _, err := resolveReadyStartupModelTarget(
		context.Background(), "", "local", "", &explicit, readyRuntime(),
		[]ModelChoice{explicit},
		false, &notes,
	)
	if err != nil {
		t.Fatalf("resolveReadyStartupModelTarget: %v", err)
	}
	if got.Model != "sentinel:latest" || got.Node != "peer" {
		t.Fatalf("got model %q node %q, want explicit foreign Ollama", got.Model, got.Node)
	}
	if notes.Len() != 0 {
		t.Fatalf("notice %q, want no borrow warning for explicit choice", notes.String())
	}
}

// When a catalog default fails its readiness probe and falls back to foreign Ollama,
// the notice must not claim the dead default was absent from the catalog.
func TestStartupForeignOllamaAfterFailedProbeDoesNotClaimCatalogMiss(t *testing.T) {
	stubForeignStartup(t, "served-9b")
	stubStartupReady(t, map[string]modelReadiness{"served-9b": modelDown})

	var notes bytes.Buffer
	got, _, _, err := resolveReadyStartupModelTarget(
		context.Background(), "served-9b", "auto", "", nil, readyRuntime(),
		[]ModelChoice{foreignOllamaChoice(), hubChoice("served-9b", false)},
		false, &notes,
	)
	if err != nil {
		t.Fatalf("resolveReadyStartupModelTarget: %v", err)
	}
	if got.Model != "sentinel:latest" || got.Node != "peer" {
		t.Fatalf("got model %q node %q, want foreign Ollama after hub probe failed", got.Model, got.Node)
	}
	text := notes.String()
	if !strings.Contains(text, `Default model "served-9b" is not answering`) || !strings.Contains(text, "another node's Ollama") {
		t.Fatalf("notice %q, want dead-default probe message and foreign borrow warning", text)
	}
	if strings.Contains(text, "not a usable catalog choice") || strings.Contains(text, "Using model") {
		t.Fatalf("notice %q falsely claims catalog miss or casual substitution", text)
	}
}
