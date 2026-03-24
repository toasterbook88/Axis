package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/toasterbook88/axis/internal/skills"
)

func TestHealthEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload["status"] != "ok" {
		t.Fatalf("expected status=ok, got %#v", payload["status"])
	}
	if payload["name"] != "axis" {
		t.Fatalf("expected name=axis, got %#v", payload["name"])
	}
}

func TestToolsEndpointIncludesExecutionSurface(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/mcp/tools", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload ToolsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	var sawExecute, sawKnowledge bool
	for _, tool := range payload.Tools {
		switch tool.Name {
		case "axis_execute":
			sawExecute = true
		case "axis_knowledge":
			sawKnowledge = true
		}
	}

	if !sawExecute {
		t.Fatal("expected axis_execute tool in /mcp/tools")
	}
	if !sawKnowledge {
		t.Fatal("expected axis_knowledge tool in /mcp/tools")
	}
}

func TestResolveIntentMatchesNaturalLanguageScript(t *testing.T) {
	intent, err := resolveIntent("run a small local model with ollama inference", "auto", &skills.Store{})
	if err != nil {
		t.Fatalf("expected natural-language script match, got %v", err)
	}
	if intent.matchedScript == nil {
		t.Fatal("expected a matched script")
	}
	if intent.matchedScript.Name != "ollama-run-smart" {
		t.Fatalf("expected ollama-run-smart, got %q", intent.matchedScript.Name)
	}
}
