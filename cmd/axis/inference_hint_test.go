package main

import (
	"context"
	"strings"
	"testing"

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
	inferenceResolveFn = func(context.Context, *config.AIConfig, llmrouter.ResolveRoleOptions) (llmrouter.RoleRouteDecision, error) {
		return llmrouter.RoleRouteDecision{
			Role:         "default",
			Backend:      "hub",
			Model:        "coder:latest",
			Endpoint:     "http://127.0.0.1:4000/v1",
			Kind:         config.AIBackendOpenAICompatible,
			Healthy:      true,
			ModelPresent: true,
		}, nil
	}

	dec := &models.PlacementDecision{OK: true, Node: "node-a", Reasoning: []string{"base"}}
	appendInferenceRouteHints(dec, "run ollama inference", "")
	joined := strings.Join(dec.Reasoning, " ")
	if !strings.Contains(joined, "inference_backend=hub") {
		t.Fatalf("reasoning=%v", dec.Reasoning)
	}
	if !strings.Contains(joined, "base") {
		t.Fatal("expected original reasoning preserved")
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
	appendInferenceRouteHints(dec, "go build", "")
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

func TestModelChoicesFromAIConfig(t *testing.T) {
	prevLoad := inferenceAILoadFn
	prevResolve := inferenceResolveFn
	t.Cleanup(func() {
		inferenceAILoadFn = prevLoad
		inferenceResolveFn = prevResolve
	})
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
	inferenceResolveFn = func(context.Context, *config.AIConfig, llmrouter.ResolveRoleOptions) (llmrouter.RoleRouteDecision, error) {
		return llmrouter.RoleRouteDecision{
			Role:     "default",
			Backend:  "hub",
			Model:    "coder:latest",
			Endpoint: "http://127.0.0.1:4000/v1",
			Kind:     config.AIBackendOpenAICompatible,
		}, nil
	}
	choices := modelChoicesFromAIConfig()
	if len(choices) != 1 || choices[0].ID != "ai-role:default" {
		t.Fatalf("got %#v", choices)
	}
	if choices[0].Protocol != "openai" {
		t.Fatalf("protocol=%s", choices[0].Protocol)
	}
}
