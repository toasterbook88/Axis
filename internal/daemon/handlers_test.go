package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

// mockCache is a minimal SnapshotCache for handler tests.
type mockCache struct {
	snap       *models.ClusterSnapshot
	meta       Metadata
	refreshErr error
}

func (m *mockCache) Snapshot() (*models.ClusterSnapshot, bool) {
	if m.snap == nil {
		return nil, false
	}
	return m.snap, true
}

func (m *mockCache) Meta() Metadata { return m.meta }

func (m *mockCache) Invalidate() { m.snap = nil }

func (m *mockCache) RefreshNow(_ context.Context) error { return m.refreshErr }

// newRecordedRequest builds a request and response recorder for a handler test.
func newRecordedRequest(method, path string, body string) (*httptest.ResponseRecorder, *http.Request) {
	var reqBody *strings.Reader
	if body != "" {
		reqBody = strings.NewReader(body)
	} else {
		reqBody = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reqBody)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return httptest.NewRecorder(), req
}

// --- healthHandler ---

func TestHealthHandlerReturnsOK(t *testing.T) {
	cache := &mockCache{
		snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy},
		meta: Metadata{Ready: true, CacheAgeSec: 5},
	}
	h := healthHandler(cache)
	rec, req := newRecordedRequest(http.MethodGet, "/health", "")
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode health payload: %v", err)
	}
	if payload["status"] != "ok" {
		t.Errorf("expected status ok, got %v", payload["status"])
	}
	if payload["name"] != "axis" {
		t.Errorf("expected name axis, got %v", payload["name"])
	}
}

func TestHealthHandlerNilCacheOmitsCacheFields(t *testing.T) {
	h := healthHandler(nil)
	rec, req := newRecordedRequest(http.MethodGet, "/health", "")
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := payload["cache_ready"]; ok {
		t.Error("expected cache_ready absent when cache is nil")
	}
}

func TestHealthHandlerRejectsNonGET(t *testing.T) {
	h := healthHandler(nil)
	rec, req := newRecordedRequest(http.MethodPost, "/health", "")
	h(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// --- snapshotHandler ---

func TestSnapshotHandlerReturnsSnapshot(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Status:  models.SnapshotHealthy,
		Summary: models.ClusterSummary{TotalNodes: 2},
	}
	h := snapshotHandler(&mockCache{snap: snap})
	rec, req := newRecordedRequest(http.MethodGet, "/snapshot", "")
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got models.ClusterSnapshot
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if got.Summary.TotalNodes != 2 {
		t.Errorf("expected total nodes 2, got %d", got.Summary.TotalNodes)
	}
}

func TestSnapshotHandlerNotReadyReturns503(t *testing.T) {
	h := snapshotHandler(&mockCache{snap: nil})
	rec, req := newRecordedRequest(http.MethodGet, "/snapshot", "")
	h(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestSnapshotHandlerNilCacheReturns503(t *testing.T) {
	h := snapshotHandler(nil)
	rec, req := newRecordedRequest(http.MethodGet, "/snapshot", "")
	h(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestSnapshotHandlerRejectsNonGET(t *testing.T) {
	h := snapshotHandler(&mockCache{snap: &models.ClusterSnapshot{}})
	rec, req := newRecordedRequest(http.MethodPost, "/snapshot", "")
	h(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// --- snapshotMetaHandler ---

func TestSnapshotMetaHandlerReturnsMeta(t *testing.T) {
	cache := &mockCache{meta: Metadata{Ready: true, RefreshIntervalSec: 60, Version: Version}}
	h := snapshotMetaHandler(cache)
	rec, req := newRecordedRequest(http.MethodGet, "/snapshot/meta", "")
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var meta Metadata
	if err := json.NewDecoder(rec.Body).Decode(&meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if !meta.Ready {
		t.Error("expected ready=true")
	}
	if meta.Version != Version {
		t.Errorf("expected version %q, got %q", Version, meta.Version)
	}
}

func TestSnapshotMetaHandlerNilCacheReturns503(t *testing.T) {
	h := snapshotMetaHandler(nil)
	rec, req := newRecordedRequest(http.MethodGet, "/snapshot/meta", "")
	h(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestSnapshotMetaHandlerRejectsNonGET(t *testing.T) {
	h := snapshotMetaHandler(&mockCache{})
	rec, req := newRecordedRequest(http.MethodDelete, "/snapshot/meta", "")
	h(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// --- invalidateHandler ---

func TestInvalidateHandlerClearsCache(t *testing.T) {
	cache := &mockCache{snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy}}
	h := invalidateHandler(cache)
	rec, req := newRecordedRequest(http.MethodPost, "/invalidate", "")
	h(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if _, ok := cache.Snapshot(); ok {
		t.Error("expected snapshot to be cleared after invalidate")
	}
}

func TestInvalidateHandlerRejectsGET(t *testing.T) {
	h := invalidateHandler(&mockCache{})
	rec, req := newRecordedRequest(http.MethodGet, "/invalidate", "")
	h(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestInvalidateHandlerNilCacheReturns503(t *testing.T) {
	h := invalidateHandler(nil)
	rec, req := newRecordedRequest(http.MethodPost, "/invalidate", "")
	h(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

// --- refreshHandler ---

func TestRefreshHandlerTriggersRefresh(t *testing.T) {
	cache := &mockCache{refreshErr: nil}
	h := refreshHandler(cache)
	rec, req := newRecordedRequest(http.MethodPost, "/refresh", "")
	h(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
}

func TestRefreshHandlerPropagatesError(t *testing.T) {
	cache := &mockCache{refreshErr: context.DeadlineExceeded}
	h := refreshHandler(cache)
	rec, req := newRecordedRequest(http.MethodPost, "/refresh", "")
	h(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestRefreshHandlerRejectsGET(t *testing.T) {
	h := refreshHandler(&mockCache{})
	rec, req := newRecordedRequest(http.MethodGet, "/refresh", "")
	h(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestRefreshHandlerNilCacheReturns503(t *testing.T) {
	h := refreshHandler(nil)
	rec, req := newRecordedRequest(http.MethodPost, "/refresh", "")
	h(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

// --- toolsHandler ---

func TestToolsHandlerReturnsTools(t *testing.T) {
	h := toolsHandler()
	rec, req := newRecordedRequest(http.MethodGet, "/tools", "")
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp ToolsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	if len(resp.Tools) == 0 {
		t.Error("expected at least one tool definition")
	}
}

func TestToolsHandlerRejectsNonGET(t *testing.T) {
	h := toolsHandler()
	rec, req := newRecordedRequest(http.MethodPost, "/tools", "")
	h(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// --- permanentRedirect ---

func TestPermanentRedirectSendsCorrectStatus(t *testing.T) {
	h := permanentRedirect("/health")
	rec, req := newRecordedRequest(http.MethodGet, "/healthz", "")
	h(rec, req)
	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("expected 308, got %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/health" {
		t.Errorf("expected Location /health, got %q", got)
	}
}

// --- HealthPayload ---

func TestHealthPayloadNilMeta(t *testing.T) {
	p := HealthPayload(nil)
	if p["status"] != "ok" {
		t.Errorf("expected status ok, got %v", p["status"])
	}
	if p["name"] != "axis" {
		t.Errorf("expected name axis, got %v", p["name"])
	}
	if _, ok := p["cache_ready"]; ok {
		t.Error("expected cache_ready absent for nil meta")
	}
}

func TestHealthPayloadWithMeta(t *testing.T) {
	meta := &Metadata{
		Ready:       true,
		CacheAgeSec: 10,
		LastError:   "some error",
	}
	p := HealthPayload(meta)
	if p["cache_ready"] != true {
		t.Errorf("expected cache_ready true, got %v", p["cache_ready"])
	}
	if p["cache_age_sec"] != 10 {
		t.Errorf("expected cache_age_sec 10, got %v", p["cache_age_sec"])
	}
	if p["cache_last_error"] != "some error" {
		t.Errorf("expected cache_last_error 'some error', got %v", p["cache_last_error"])
	}
}

func TestHealthPayloadWithMetaNoError(t *testing.T) {
	meta := &Metadata{Ready: false}
	p := HealthPayload(meta)
	if _, ok := p["cache_last_error"]; ok {
		t.Error("expected cache_last_error absent when LastError is empty")
	}
}

// --- ToolDefinitions ---

func TestToolDefinitionsReturnsTwoKnownTools(t *testing.T) {
	defs := ToolDefinitions()
	if len(defs) < 2 {
		t.Fatalf("expected at least 2 tool definitions, got %d", len(defs))
	}
	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
		if d.Description == "" {
			t.Errorf("tool %q has empty description", d.Name)
		}
		if d.InputSchema == nil {
			t.Errorf("tool %q has nil InputSchema", d.Name)
		}
	}
	if !names["axis_execute"] {
		t.Error("expected axis_execute tool")
	}
	if !names["axis_knowledge"] {
		t.Error("expected axis_knowledge tool")
	}
}

// --- NormalizeAddr ---

func TestNormalizeAddrPrependsHTTP(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"127.0.0.1:42425", "http://127.0.0.1:42425"},
		{"  127.0.0.1:42425/  ", "http://127.0.0.1:42425"},
		{"http://127.0.0.1:42425", "http://127.0.0.1:42425"},
		{"https://remote:8080", "https://remote:8080"},
	}
	for _, tc := range cases {
		got := NormalizeAddr(tc.in)
		if got != tc.want {
			t.Errorf("NormalizeAddr(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- RegisterRoutes wires up expected paths ---

func TestRegisterRoutesExposesExpectedPaths(t *testing.T) {
	cache := &mockCache{
		snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy},
		meta: Metadata{Ready: true},
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, cache)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	paths := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/health", http.StatusOK},
		{http.MethodGet, "/snapshot", http.StatusOK},
		{http.MethodGet, "/snapshot/meta", http.StatusOK},
		{http.MethodGet, "/tools", http.StatusOK},
	}

	for _, tc := range paths {
		req, _ := http.NewRequestWithContext(context.Background(), tc.method, srv.URL+tc.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Errorf("%s %s: request error: %v", tc.method, tc.path, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s: got %d, want %d", tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}
}

// --- New / DefaultSnapshotPath ---

func TestNewWithZeroIntervalUsesDefault(t *testing.T) {
	d := New(0, func(_ context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{}, nil
	})
	if d.interval != defaultRefreshInterval {
		t.Errorf("expected default interval %v, got %v", defaultRefreshInterval, d.interval)
	}
}

func TestNewWithNilCollectorFallsBackToDefault(t *testing.T) {
	d := New(0, nil)
	if d.collector == nil {
		t.Fatal("expected non-nil collector after New with nil")
	}
}

func TestDefaultSnapshotPathContainsDotAxis(t *testing.T) {
	p := DefaultSnapshotPath()
	if !strings.Contains(p, ".axis") {
		t.Errorf("expected .axis in default snapshot path, got %q", p)
	}
	if !strings.HasSuffix(p, "snapshot.json") {
		t.Errorf("expected snapshot.json suffix, got %q", p)
	}
}

// --- CloneSnapshot ---

func TestCloneSnapshotProducesIndependentCopy(t *testing.T) {
	orig := &models.ClusterSnapshot{
		Status: models.SnapshotHealthy,
		Nodes:  []models.NodeFacts{{Name: "alpha"}},
	}
	clone := CloneSnapshot(orig)
	if clone == orig {
		t.Fatal("expected new pointer")
	}
	clone.Nodes[0].Name = "mutated"
	if orig.Nodes[0].Name != "alpha" {
		t.Error("mutating clone changed original")
	}
}

func TestCloneSnapshotNilReturnsNil(t *testing.T) {
	if got := CloneSnapshot(nil); got != nil {
		t.Fatal("expected nil")
	}
}
