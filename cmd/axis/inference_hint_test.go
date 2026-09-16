package main

import (
	"context"
	"os"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
	"github.com/toasterbook88/axis/internal/models"
)

func TestAppendInferenceRouteHints_UnconfiguredRoleSilent(t *testing.T) {
	prevLoad := inferenceAILoadFn
	prevMap := inferenceRoleMapFn
	prevResolve := inferenceResolveFn
	t.Cleanup(func() {
		inferenceAILoadFn = prevLoad
		inferenceRoleMapFn = prevMap
		inferenceResolveFn = prevResolve
	})
	inferenceAILoadFn = func(string) (*config.AIConfig, error) {
		return &config.AIConfig{
			Backends: []config.AIBackendConfig{{Name: "hub", Kind: "openai-compatible", BaseURL: "http://127.0.0.1:1"}},
			Roles:    map[string]config.AIRoleConfig{"default": {Prefer: []string{"hub"}, Model: "m"}},
		}, nil
	}
	inferenceRoleMapFn = func(string) string { return "long" } // not in config
	called := false
	inferenceResolveFn = func(context.Context, *config.AIConfig, llmrouter.ResolveRoleOptions) (llmrouter.RoleRouteDecision, error) {
		called = true
		return llmrouter.RoleRouteDecision{}, nil
	}

	dec := &models.PlacementDecision{Reasoning: []string{"only"}}
	appendInferenceRouteHints(context.Background(), dec, "long context")
	if called {
		t.Fatal("must not resolve unconfigured role")
	}
	if len(dec.Reasoning) != 1 || dec.Reasoning[0] != "only" {
		t.Fatalf("got %v", dec.Reasoning)
	}
}

func TestAppendInferenceRouteHints_NoRoleNoOp(t *testing.T) {
	prevLoad := inferenceAILoadFn
	prevMap := inferenceRoleMapFn
	t.Cleanup(func() {
		inferenceAILoadFn = prevLoad
		inferenceRoleMapFn = prevMap
	})
	inferenceAILoadFn = func(string) (*config.AIConfig, error) {
		return &config.AIConfig{
			Backends: []config.AIBackendConfig{{Name: "hub", Kind: "openai-compatible", BaseURL: "http://127.0.0.1:1"}},
			Roles:    map[string]config.AIRoleConfig{"default": {Prefer: []string{"hub"}, Model: "m"}},
		}, nil
	}
	inferenceRoleMapFn = func(string) string { return "" }

	dec := &models.PlacementDecision{Reasoning: []string{"only"}}
	appendInferenceRouteHints(context.Background(), dec, "go build")
	if len(dec.Reasoning) != 1 || dec.Reasoning[0] != "only" {
		t.Fatalf("got %v", dec.Reasoning)
	}
}

func TestModelFromAIRole(t *testing.T) {
	prevLoad := inferenceAILoadFn
	t.Cleanup(func() { inferenceAILoadFn = prevLoad })
	inferenceAILoadFn = func(string) (*config.AIConfig, error) {
		return &config.AIConfig{
			Roles: map[string]config.AIRoleConfig{
				"default": {Model: "coder:latest", Prefer: []string{"hub"}},
			},
			Backends: []config.AIBackendConfig{{Name: "hub", Kind: "ollama", BaseURL: "http://127.0.0.1:11434"}},
		}, nil
	}
	if got := modelFromAIRole("default"); got != "coder:latest" {
		t.Fatalf("got %q", got)
	}
	if got := modelFromAIRole("nope"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestAPIKeyForAIBackend_NameAuthoritative(t *testing.T) {
	prevLoad := inferenceAILoadFn
	t.Cleanup(func() { inferenceAILoadFn = prevLoad })

	// Two backends share the same base URL with different keys.
	// Name must win over endpoint match order.
	b1KeyFile := t.TempDir() + "/k1"
	b2KeyFile := t.TempDir() + "/k2"
	if err := writeKeyFile(b1KeyFile, "key-for-b1"); err != nil {
		t.Fatal(err)
	}
	if err := writeKeyFile(b2KeyFile, "key-for-b2"); err != nil {
		t.Fatal(err)
	}
	sharedURL := "http://127.0.0.1:4000/v1"
	inferenceAILoadFn = func(string) (*config.AIConfig, error) {
		return &config.AIConfig{
			Backends: []config.AIBackendConfig{
				{Name: "b1", Kind: config.AIBackendOpenAICompatible, BaseURL: sharedURL, APIKeyFile: b1KeyFile},
				{Name: "b2", Kind: config.AIBackendOpenAICompatible, BaseURL: sharedURL, APIKeyFile: b2KeyFile},
			},
		}, nil
	}

	if got := apiKeyForAIBackend("ai-backend:b2", sharedURL); got != "key-for-b2" {
		t.Fatalf("named b2: got %q want key-for-b2", got)
	}
	if got := apiKeyForAIBackend("ai-backend:b1", sharedURL); got != "key-for-b1" {
		t.Fatalf("named b1: got %q want key-for-b1", got)
	}
	// Endpoint-only fallback still works (first matching URL).
	if got := apiKeyForAIBackend("", sharedURL); got != "key-for-b1" {
		t.Fatalf("endpoint fallback: got %q want key-for-b1", got)
	}
}

func writeKeyFile(path, key string) error {
	return os.WriteFile(path, []byte(key), 0o600)
}
