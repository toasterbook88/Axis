package axismcp

import (
	"context"
	"errors"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
)

func TestInferenceRouteExplain_MissingArgs(t *testing.T) {
	result, err := inferenceRouteExplainTool(context.Background(), toolRequest(nil))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error for missing role/model")
	}
}

func TestInferenceRouteExplain_NoBackends(t *testing.T) {
	prevLoad := loadAIConfigForMCP
	prevPath := aiConfigPathForMCP
	t.Cleanup(func() {
		loadAIConfigForMCP = prevLoad
		aiConfigPathForMCP = prevPath
	})
	aiConfigPathForMCP = func() string { return "/tmp/missing-ai.yaml" }
	loadAIConfigForMCP = func(string) (*config.AIConfig, error) {
		return &config.AIConfig{Roles: map[string]config.AIRoleConfig{}}, nil
	}

	result, err := inferenceRouteExplainTool(context.Background(), toolRequest(map[string]any{
		"role": "default",
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error for empty backends")
	}
}

func TestInferenceRouteExplain_Success(t *testing.T) {
	prevLoad := loadAIConfigForMCP
	prevPath := aiConfigPathForMCP
	prevResolve := resolveRoleForMCP
	t.Cleanup(func() {
		loadAIConfigForMCP = prevLoad
		aiConfigPathForMCP = prevPath
		resolveRoleForMCP = prevResolve
	})
	aiConfigPathForMCP = func() string { return "test-ai.yaml" }
	loadAIConfigForMCP = func(string) (*config.AIConfig, error) {
		return &config.AIConfig{
			Backends: []config.AIBackendConfig{
				{Name: "hub", Kind: config.AIBackendOpenAICompatible, BaseURL: "http://127.0.0.1:4000/v1"},
			},
			Roles: map[string]config.AIRoleConfig{
				"default": {Prefer: []string{"hub"}, Model: "coder:latest"},
			},
		}, nil
	}
	resolveRoleForMCP = func(ctx context.Context, cfg *config.AIConfig, opts llmrouter.ResolveRoleOptions) (llmrouter.RoleRouteDecision, error) {
		if opts.Role != "default" {
			t.Fatalf("role=%q", opts.Role)
		}
		if !opts.RequireModelListed {
			t.Fatal("expected RequireModelListed default true")
		}
		return llmrouter.RoleRouteDecision{
			Role:         "default",
			Backend:      "hub",
			Model:        "coder:latest",
			Endpoint:     "http://127.0.0.1:4000/v1",
			Kind:         config.AIBackendOpenAICompatible,
			Healthy:      true,
			ModelPresent: true,
			Confidence:   0.95,
			Reasoning:    []string{"ok"},
		}, nil
	}

	result, err := inferenceRouteExplainTool(context.Background(), toolRequest(map[string]any{
		"role": "default",
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", toolResultText(t, result))
	}
	dec, ok := result.StructuredContent.(llmrouter.RoleRouteDecision)
	if !ok {
		t.Fatalf("structured type %T", result.StructuredContent)
	}
	if dec.Backend != "hub" || !dec.ModelPresent {
		t.Fatalf("got %+v", dec)
	}
}

func TestInferenceRouteExplain_ModelUnlistedIsError(t *testing.T) {
	prevLoad := loadAIConfigForMCP
	prevResolve := resolveRoleForMCP
	t.Cleanup(func() {
		loadAIConfigForMCP = prevLoad
		resolveRoleForMCP = prevResolve
	})
	loadAIConfigForMCP = func(string) (*config.AIConfig, error) {
		return &config.AIConfig{
			Backends: []config.AIBackendConfig{{Name: "hub", Kind: "openai-compatible", BaseURL: "http://127.0.0.1:1"}},
		}, nil
	}
	resolveRoleForMCP = func(context.Context, *config.AIConfig, llmrouter.ResolveRoleOptions) (llmrouter.RoleRouteDecision, error) {
		return llmrouter.RoleRouteDecision{
			Model:     "missing",
			Reasoning: []string{"skip"},
		}, errors.Join(llmrouter.ErrModelUnlisted, errors.New("missing"))
	}

	result, err := inferenceRouteExplainTool(context.Background(), toolRequest(map[string]any{
		"model": "missing",
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError for unlisted model")
	}
	if _, ok := result.StructuredContent.(llmrouter.RoleRouteDecision); !ok {
		t.Fatalf("expected partial decision, got %T", result.StructuredContent)
	}
}

func TestInferenceRouteExplain_AllowUnlistedDisablesStrict(t *testing.T) {
	prevLoad := loadAIConfigForMCP
	prevResolve := resolveRoleForMCP
	t.Cleanup(func() {
		loadAIConfigForMCP = prevLoad
		resolveRoleForMCP = prevResolve
	})
	loadAIConfigForMCP = func(string) (*config.AIConfig, error) {
		return &config.AIConfig{
			Backends: []config.AIBackendConfig{{Name: "hub", Kind: "openai-compatible", BaseURL: "http://127.0.0.1:1"}},
		}, nil
	}
	var sawRequire bool
	resolveRoleForMCP = func(_ context.Context, _ *config.AIConfig, opts llmrouter.ResolveRoleOptions) (llmrouter.RoleRouteDecision, error) {
		sawRequire = opts.RequireModelListed
		return llmrouter.RoleRouteDecision{Backend: "hub", Model: "x", Healthy: true}, nil
	}

	_, err := inferenceRouteExplainTool(context.Background(), toolRequest(map[string]any{
		"model":          "x",
		"allow_unlisted": true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if sawRequire {
		t.Fatal("RequireModelListed should be false when allow_unlisted=true")
	}
}
