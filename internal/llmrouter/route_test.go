package llmrouter_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
)

func TestResolveRole_SkipProbe(t *testing.T) {
	cfg := &config.AIConfig{
		Backends: []config.AIBackendConfig{
			{Name: "local-ollama", Kind: config.AIBackendOllama, BaseURL: "http://127.0.0.1:11434"},
		},
		Roles: map[string]config.AIRoleConfig{
			"default": {Prefer: []string{"local-ollama"}, Model: "m"},
		},
	}
	dec, err := llmrouter.ResolveRole(context.Background(), cfg, llmrouter.ResolveRoleOptions{
		Role:      "default",
		SkipProbe: true,
	})
	if err != nil {
		t.Fatalf("ResolveRole: %v", err)
	}
	if dec.Backend != "local-ollama" || dec.Healthy {
		t.Fatalf("got %+v", dec)
	}
}

func TestResolveRole_UnknownRole(t *testing.T) {
	cfg := &config.AIConfig{Roles: map[string]config.AIRoleConfig{}}
	_, err := llmrouter.ResolveRole(context.Background(), cfg, llmrouter.ResolveRoleOptions{Role: "nope"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestResolveRole_ModelOverride(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"other:latest"}]}`))
	}))
	t.Cleanup(up.Close)

	cfg := &config.AIConfig{
		Backends: []config.AIBackendConfig{
			{Name: "ollama", Kind: config.AIBackendOllama, BaseURL: up.URL},
		},
		Roles: map[string]config.AIRoleConfig{
			"default": {Prefer: []string{"ollama"}, Model: "coder:latest"},
		},
	}
	dec, err := llmrouter.ResolveRole(context.Background(), cfg, llmrouter.ResolveRoleOptions{
		Role:  "default",
		Model: "other:latest",
	})
	if err != nil {
		t.Fatalf("ResolveRole: %v", err)
	}
	if dec.Model != "other:latest" || !dec.ModelPresent {
		t.Fatalf("got %+v", dec)
	}
}

func TestResolveRole_RequireModelListed_ErrorsWhenMissing(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"other"}]}`))
	}))
	t.Cleanup(hub.Close)

	cfg := &config.AIConfig{
		Backends: []config.AIBackendConfig{
			{Name: "hub", Kind: config.AIBackendOpenAICompatible, BaseURL: hub.URL + "/v1"},
		},
		Roles: map[string]config.AIRoleConfig{
			"default": {Prefer: []string{"hub"}, Model: "coder:latest"},
		},
	}
	_, err := llmrouter.ResolveRole(context.Background(), cfg, llmrouter.ResolveRoleOptions{
		Role:               "default",
		RequireModelListed: true,
	})
	if err == nil || !strings.Contains(err.Error(), "not listed") {
		t.Fatalf("expected ErrModelUnlisted, got %v", err)
	}
}
