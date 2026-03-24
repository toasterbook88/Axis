package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/skills"
)

type fakeCache struct {
	snap        *models.ClusterSnapshot
	meta        daemon.Metadata
	invalidated bool
	refreshed   bool
}

func (f *fakeCache) Snapshot() (*models.ClusterSnapshot, bool) {
	if f.snap == nil {
		return nil, false
	}
	return f.snap, true
}

func (f *fakeCache) Meta() daemon.Metadata {
	return f.meta
}

func (f *fakeCache) Invalidate() {
	f.invalidated = true
	f.snap = nil
	f.meta.Ready = false
}

func (f *fakeCache) RefreshNow(context.Context) error {
	f.refreshed = true
	f.snap = &models.ClusterSnapshot{Status: models.SnapshotHealthy}
	f.meta.Ready = true
	return nil
}

func TestHealthEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, nil)

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
	registerRoutes(mux, nil)

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

func TestSnapshotEndpointReturnsCachedSnapshot(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, &fakeCache{
		snap: &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Summary: models.ClusterSummary{
				TotalNodes: 1,
			},
		},
		meta: daemon.Metadata{
			Source:             "daemon-cache",
			Ready:              true,
			RefreshIntervalSec: 60,
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/snapshot", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload models.ClusterSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload.Summary.TotalNodes != 1 {
		t.Fatalf("expected total nodes 1, got %d", payload.Summary.TotalNodes)
	}
}

func TestSnapshotMetaEndpointReturnsCacheMetadata(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, &fakeCache{
		meta: daemon.Metadata{
			Source:             "daemon-cache",
			Ready:              true,
			RefreshIntervalSec: 60,
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/snapshot/meta", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload daemon.Metadata
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload.Source != "daemon-cache" {
		t.Fatalf("expected source daemon-cache, got %q", payload.Source)
	}
	if !payload.Ready {
		t.Fatal("expected ready=true")
	}
	if payload.RefreshIntervalSec != 60 {
		t.Fatalf("expected refresh interval sec 60, got %d", payload.RefreshIntervalSec)
	}
}

func TestToolsEndpointAliasReturnsSamePayload(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, nil)

	req := httptest.NewRequest(http.MethodGet, "/tools", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload ToolsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(payload.Tools) == 0 {
		t.Fatal("expected tools payload")
	}
}

func TestInvalidateEndpointCallsCacheInvalidate(t *testing.T) {
	cache := &fakeCache{
		snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy},
		meta: daemon.Metadata{Ready: true},
	}
	mux := http.NewServeMux()
	registerRoutes(mux, cache)

	req := httptest.NewRequest(http.MethodPost, "/invalidate", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if !cache.invalidated {
		t.Fatal("expected cache invalidation to be triggered")
	}
}

func TestRefreshEndpointCallsCacheRefresh(t *testing.T) {
	cache := &fakeCache{
		meta: daemon.Metadata{Ready: false},
	}
	mux := http.NewServeMux()
	registerRoutes(mux, cache)

	req := httptest.NewRequest(http.MethodPost, "/refresh", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if !cache.refreshed {
		t.Fatal("expected cache refresh to be triggered")
	}
}

func TestResolveIntentMatchesNaturalLanguageScript(t *testing.T) {
	intent, err := resolveIntent("run a small local model with ollama inference", "script", &skills.Store{})
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

func TestRunEndpointRequiresExplicitMode(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, nil)

	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"description":"git status"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "mode is required") {
		t.Fatalf("expected mode-required error, got %q", rec.Body.String())
	}
}
