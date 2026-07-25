package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
	"github.com/toasterbook88/axis/internal/models"
)

func TestAppendInferenceRouteHints(t *testing.T) {
	prevLoad := inferenceAILoadFn
	prevPath := inferenceAIPathFn
	prevResolve := inferenceResolveFn
	prevMap := inferenceRoleMapFn
	t.Cleanup(func() {
		inferenceAILoadFn = prevLoad
		inferenceAIPathFn = prevPath
		inferenceResolveFn = prevResolve
		inferenceRoleMapFn = prevMap
	})

	inferenceAIPathFn = func() string { return "test" }
	inferenceAILoadFn = func(string) (*config.AIConfig, error) {
		return &config.AIConfig{
			Backends: []config.AIBackendConfig{
				{Name: "hub", Kind: config.AIBackendOpenAICompatible, BaseURL: "http://127.0.0.1:4000/v1"},
			},
			Roles: map[string]config.AIRoleConfig{
				"default": {Prefer: []string{"hub"}, Model: "coder:latest"},
			},
		}, nil
	}
	inferenceRoleMapFn = func(string) string { return "default" }
	var sawSkipProbe bool
	inferenceResolveFn = func(_ context.Context, _ *config.AIConfig, opts llmrouter.ResolveRoleOptions) (llmrouter.RoleRouteDecision, error) {
		sawSkipProbe = opts.SkipProbe
		return llmrouter.RoleRouteDecision{
			Role:     "default",
			Backend:  "hub",
			Model:    "coder:latest",
			Endpoint: "http://127.0.0.1:4000/v1",
			Kind:     config.AIBackendOpenAICompatible,
			Probed:   false, // SkipProbe path
		}, nil
	}

	dec := &models.PlacementDecision{OK: true, Node: "node-a", Reasoning: []string{"base"}}
	appendInferenceRouteHints(context.Background(), dec, "run ollama inference")
	if !sawSkipProbe {
		t.Fatal("placement hints must SkipProbe")
	}
	joined := strings.Join(dec.Reasoning, " ")
	if !strings.Contains(joined, "inference_backend=hub") {
		t.Fatalf("reasoning=%v", dec.Reasoning)
	}
	if !strings.Contains(joined, "inference_probe=skipped") {
		t.Fatalf("expected probe=skipped, got %v", dec.Reasoning)
	}
	if strings.Contains(joined, "inference_healthy=") {
		t.Fatal("must not claim healthy=false when unprobed")
	}
	if strings.Contains(joined, "127.0.0.1") {
		t.Fatal("raw endpoint must not appear in placement reasoning")
	}
}

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

func TestModelChoicesFromAIConfig_SecurityAndProbe(t *testing.T) {
	prevLoad := inferenceAILoadFn
	prevResolve := inferenceResolveFn
	prevProbe := inferenceProbeFn
	t.Cleanup(func() {
		inferenceAILoadFn = prevLoad
		inferenceResolveFn = prevResolve
		inferenceProbeFn = prevProbe
	})
	inferenceAILoadFn = func(string) (*config.AIConfig, error) {
		return &config.AIConfig{
			Backends: []config.AIBackendConfig{
				{Name: "hub", Kind: config.AIBackendOpenAICompatible, BaseURL: "http://127.0.0.1:4000/v1"},
				{Name: "remote", Kind: config.AIBackendOpenAICompatible, BaseURL: "https://api.example.com/v1"},
			},
			Roles: map[string]config.AIRoleConfig{
				"default":  {Prefer: []string{"hub"}, Model: "coder:latest"},
				"cloudish": {Prefer: []string{"remote"}, Model: "gpt-x"},
			},
		}, nil
	}
	inferenceResolveFn = func(_ context.Context, _ *config.AIConfig, opts llmrouter.ResolveRoleOptions) (llmrouter.RoleRouteDecision, error) {
		if opts.Role == "cloudish" {
			return llmrouter.RoleRouteDecision{
				Role: "cloudish", Backend: "remote", Model: "gpt-x",
				Endpoint: "https://api.example.com/v1", Kind: config.AIBackendOpenAICompatible,
			}, nil
		}
		return llmrouter.RoleRouteDecision{
			Role: "default", Backend: "hub", Model: "coder:latest",
			Endpoint: "http://127.0.0.1:4000/v1", Kind: config.AIBackendOpenAICompatible,
		}, nil
	}
	inferenceProbeFn = func(url string) bool {
		return strings.Contains(url, "127.0.0.1")
	}

	choices := modelChoicesFromAIConfig()
	if len(choices) != 2 {
		t.Fatalf("got %d choices: %#v", len(choices), choices)
	}
	byID := map[string]ModelChoice{}
	for _, c := range choices {
		byID[c.ID] = c
	}
	local := byID["ai-role:default"]
	if local.SecurityClass != agent.BackendLocal {
		t.Fatalf("loopback security=%v", local.SecurityClass)
	}
	if local.Disabled {
		t.Fatal("loopback probe should pass")
	}
	remote := byID["ai-role:cloudish"]
	if remote.SecurityClass != agent.BackendRemote {
		t.Fatalf("public host security=%v", remote.SecurityClass)
	}
	if !remote.Disabled || remote.DisabledReason != "unreachable" {
		t.Fatalf("remote should be disabled unreachable: %+v", remote)
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

func TestModelChoicesFromAIConfig_Dedupe(t *testing.T) {
	prevLoad := inferenceAILoadFn
	prevResolve := inferenceResolveFn
	prevProbe := inferenceProbeFn
	t.Cleanup(func() {
		inferenceAILoadFn = prevLoad
		inferenceResolveFn = prevResolve
		inferenceProbeFn = prevProbe
	})
	inferenceAILoadFn = func(string) (*config.AIConfig, error) {
		return &config.AIConfig{
			Backends: []config.AIBackendConfig{
				{Name: "hub", Kind: config.AIBackendOpenAICompatible, BaseURL: "http://127.0.0.1:4000/v1"},
			},
			Roles: map[string]config.AIRoleConfig{
				"a": {Prefer: []string{"hub"}, Model: "same"},
				"b": {Prefer: []string{"hub"}, Model: "same"},
			},
		}, nil
	}
	inferenceResolveFn = func(context.Context, *config.AIConfig, llmrouter.ResolveRoleOptions) (llmrouter.RoleRouteDecision, error) {
		return llmrouter.RoleRouteDecision{
			Backend: "hub", Model: "same", Endpoint: "http://127.0.0.1:4000/v1",
			Kind: config.AIBackendOpenAICompatible,
		}, nil
	}
	inferenceProbeFn = func(string) bool { return true }

	choices := modelChoicesFromAIConfig()
	if len(choices) != 1 {
		t.Fatalf("expected dedupe to 1, got %d", len(choices))
	}
}
