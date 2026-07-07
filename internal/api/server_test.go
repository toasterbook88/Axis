package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/mesh"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

type fakeCache struct {
	mu              sync.Mutex
	snap            *models.ClusterSnapshot
	meta            daemon.Metadata
	ledger          *reservation.Ledger
	meshInstance    *mesh.Mesh
	invalidated     bool
	refreshed       bool
	refreshTriggers []string
}

func (f *fakeCache) Snapshot() (*models.ClusterSnapshot, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snap == nil {
		return nil, false
	}
	return f.snap, true
}

func (f *fakeCache) Meta() daemon.Metadata {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.meta
}

func (f *fakeCache) Ledger() *reservation.Ledger {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ledger
}

func (f *fakeCache) Mesh() *mesh.Mesh {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.meshInstance
}

func (f *fakeCache) Invalidate() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidated = true
	f.snap = nil
	f.meta.Ready = false
}

func (f *fakeCache) RefreshNow(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshed = true
	f.snap = &models.ClusterSnapshot{Status: models.SnapshotHealthy}
	f.meta.Ready = true
	f.meta.LastRefreshTrigger = daemon.RefreshTriggerManual
	return nil
}

func (f *fakeCache) RefreshWithTrigger(_ context.Context, trigger string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshed = true
	f.snap = &models.ClusterSnapshot{Status: models.SnapshotHealthy}
	f.meta.Ready = true
	f.meta.LastRefreshTrigger = trigger
	f.refreshTriggers = append(f.refreshTriggers, trigger)
	return nil
}

func TestHealthEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, nil, "test-token")

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
	registerRoutes(mux, nil, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/mcp/tools", nil)
	req.Header.Set("Authorization", "Bearer test-token")
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



func TestV2ClusterEndpointSummarizesSnapshot(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, &fakeCache{
		snap: &models.ClusterSnapshot{
			Status: models.SnapshotDegraded,
			Nodes: []models.NodeFacts{
				{
					Name:   "alpha",
					Status: models.StatusComplete,
					OS:     "darwin",
					Arch:   "arm64",
					Resources: &models.Resources{
						RAMTotalMB: 32768,
						RAMFreeMB:  16384,
						Pressure:   "low",
						GPUs:       []models.GPUInfo{{Model: "Apple M3 Max"}},
					},
					Tools: []models.ToolInfo{{Name: "ollama"}, {Name: "git"}},
				},
				{
					Name:   "beta",
					Status: models.StatusPartial,
					OS:     "linux",
					Arch:   "amd64",
					Resources: &models.Resources{
						RAMTotalMB: 65536,
						RAMFreeMB:  8192,
						Pressure:   "high",
					},
					Tools: []models.ToolInfo{{Name: "docker"}},
				},
			},
		},
		meta: daemon.Metadata{
			Ready:       true,
			Version:     "test-build",
			CacheAgeSec: 17,
		},
	}, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/v2/cluster", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload V2ClusterResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload.Status != string(models.SnapshotDegraded) {
		t.Fatalf("expected degraded status, got %q", payload.Status)
	}
	if payload.Version != "test-build" {
		t.Fatalf("expected version test-build, got %q", payload.Version)
	}
	if payload.NodeCount != 2 || payload.HealthyNodes != 1 || payload.DegradedNodes != 1 {
		t.Fatalf("unexpected node counts: %+v", payload)
	}
	if payload.TotalRAMMB != 98304 || payload.FreeRAMMB != 24576 {
		t.Fatalf("unexpected RAM totals: %+v", payload)
	}
	if payload.GPUCount != 1 {
		t.Fatalf("expected 1 GPU, got %d", payload.GPUCount)
	}
	if payload.CacheAge != "17s" {
		t.Fatalf("expected cache age 17s, got %q", payload.CacheAge)
	}
}

func TestV2ClusterEndpointRequiresCache(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, nil, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/v2/cluster", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "snapshot cache unavailable") {
		t.Fatalf("expected cache-unavailable error, got %q", rec.Body.String())
	}
}

func TestV2NodesEndpointsReturnStructuredNodeData(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, &fakeCache{
		snap: &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Nodes: []models.NodeFacts{
				{
					Name:   "beta",
					Status: models.StatusPartial,
					OS:     "linux",
					Arch:   "amd64",
					Resources: &models.Resources{
						RAMTotalMB: 65536,
						RAMFreeMB:  8192,
						Pressure:   "high",
					},
					Tools: []models.ToolInfo{{Name: "docker"}},
				},
				{
					Name:   "alpha",
					Status: models.StatusComplete,
					OS:     "darwin",
					Arch:   "arm64",
					Resources: &models.Resources{
						RAMTotalMB: 32768,
						RAMFreeMB:  16384,
						Pressure:   "low",
						GPUs:       []models.GPUInfo{{Model: "Apple M3 Max"}},
					},
					Tools: []models.ToolInfo{{Name: "ollama"}, {Name: "git"}},
				},
			},
		},
		meta: daemon.Metadata{Ready: true},
	}, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/v2/nodes", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload struct {
		Nodes []V2NodeResponse `json:"nodes"`
		Count int              `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload.Count != 2 || len(payload.Nodes) != 2 {
		t.Fatalf("unexpected node list payload: %+v", payload)
	}
	if payload.Nodes[0].Name != "alpha" || payload.Nodes[1].Name != "beta" {
		t.Fatalf("expected alphabetical node order, got %+v", payload.Nodes)
	}
	if payload.Nodes[0].RAMTotalMB != 32768 || payload.Nodes[0].RAMFreeMB != 16384 {
		t.Fatalf("unexpected alpha RAM values: %+v", payload.Nodes[0])
	}
	if len(payload.Nodes[0].GPUs) != 1 || payload.Nodes[0].GPUs[0] != "Apple M3 Max" {
		t.Fatalf("unexpected GPU list: %+v", payload.Nodes[0].GPUs)
	}
	if got := strings.Join(payload.Nodes[0].Tools, ","); got != "ollama,git" {
		t.Fatalf("unexpected tool list: %q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/v2/nodes/alpha", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from single-node endpoint, got %d", rec.Code)
	}

	var node models.NodeFacts
	if err := json.Unmarshal(rec.Body.Bytes(), &node); err != nil {
		t.Fatalf("unmarshal single-node response: %v", err)
	}
	if node.Name != "alpha" || node.Status != models.StatusComplete {
		t.Fatalf("unexpected single-node response: %+v", node)
	}

	req = httptest.NewRequest(http.MethodGet, "/v2/nodes/missing", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing node, got %d", rec.Code)
	}
	var missingPayload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &missingPayload); err != nil {
		t.Fatalf("unmarshal missing-node response: %v", err)
	}
	if got, _ := missingPayload["error"].(string); got != `node "missing" not found` {
		t.Fatalf("expected missing-node error, got %q", got)
	}
}

func TestV2StubEndpointsStayNon2XX(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, &fakeCache{
		snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy},
		meta: daemon.Metadata{Ready: true},
	}, "test-token")

	tests := []struct {
		name           string
		method         string
		path           string
		body           string
		expectedStatus int
		errorContains  string
	}{
		{
			name:           "mesh method",
			method:         http.MethodDelete,
			path:           "/v2/mesh",
			expectedStatus: http.StatusMethodNotAllowed,
			errorContains:  "method not allowed",
		},
		{
			name:           "history get",
			method:         http.MethodGet,
			path:           "/v2/history",
			expectedStatus: http.StatusNotImplemented,
			errorContains:  "execution history wiring pending",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer test-token")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Fatalf("expected %d, got %d", tt.expectedStatus, rec.Code)
			}

			var payload map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if payload["ok"] != false {
				t.Fatalf("expected ok=false, got %#v", payload["ok"])
			}
			if got, _ := payload["error"].(string); !strings.Contains(got, tt.errorContains) {
				t.Fatalf("expected error containing %q, got %q", tt.errorContains, got)
			}
		})
	}
}

func TestV2EndpointsReturnSuccess(t *testing.T) {
	ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
	m := mesh.New(mesh.Peer{Name: "self"}, mesh.DefaultConfig(), nil)
	cache := &fakeCache{
		meta:         daemon.Metadata{Ready: true},
		ledger:       ledger,
		meshInstance: m,
	}
	mux := http.NewServeMux()
	registerRoutes(mux, cache, "test-token")

	tests := []struct {
		name string
		path string
	}{
		{"reservations", "/v2/reservations"},
		{"mesh", "/v2/mesh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("Authorization", "Bearer test-token")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestV2ReservationsCRUD(t *testing.T) {
	ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
	ledger.SetNodeCapacity("node-a", 16384)
	cache := &fakeCache{
		meta:   daemon.Metadata{Ready: true},
		ledger: ledger,
	}
	mux := http.NewServeMux()
	registerRoutes(mux, cache, "test-token")

	// 1. Create a reservation
	createBody := `{"id":"r1","node":"node-a","ram_mb":4096,"description":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/v2/reservations", strings.NewReader(createBody))
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created reservation.Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal created: %v", err)
	}
	if created.ID != "r1" {
		t.Fatalf("expected id=r1, got %q", created.ID)
	}
	if created.Node != "node-a" {
		t.Fatalf("expected node=node-a, got %q", created.Node)
	}

	// 2. List reservations
	req = httptest.NewRequest(http.MethodGet, "/v2/reservations", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on list, got %d: %s", rec.Code, rec.Body.String())
	}
	var listResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	resList, _ := listResp["reservations"].([]any)
	if len(resList) != 1 {
		t.Fatalf("expected 1 reservation in list, got %d", len(resList))
	}

	// 3. Get single reservation
	req = httptest.NewRequest(http.MethodGet, "/v2/reservations/r1", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on get, got %d: %s", rec.Code, rec.Body.String())
	}
	var gotEntry reservation.Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &gotEntry); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if gotEntry.ID != "r1" {
		t.Fatalf("expected id=r1 on get, got %q", gotEntry.ID)
	}

	// 4. Heartbeat
	req = httptest.NewRequest(http.MethodPost, "/v2/reservations/r1/heartbeat", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on heartbeat, got %d: %s", rec.Code, rec.Body.String())
	}
	var hbResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &hbResp); err != nil {
		t.Fatalf("unmarshal heartbeat: %v", err)
	}
	if hbResp["ok"] != true {
		t.Fatalf("expected ok=true on heartbeat, got %#v", hbResp["ok"])
	}

	// 5. Delete reservation
	req = httptest.NewRequest(http.MethodDelete, "/v2/reservations/r1", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on delete, got %d: %s", rec.Code, rec.Body.String())
	}
	var delResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &delResp); err != nil {
		t.Fatalf("unmarshal delete: %v", err)
	}
	if delResp["ok"] != true {
		t.Fatalf("expected ok=true on delete, got %#v", delResp["ok"])
	}

	// 6. Verify deletion
	req = httptest.NewRequest(http.MethodGet, "/v2/reservations/r1", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", rec.Code)
	}
}

func TestV2ReservationsCreateValidation(t *testing.T) {
	ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
	ledger.SetNodeCapacity("node-a", 16384)
	cache := &fakeCache{
		meta:   daemon.Metadata{Ready: true},
		ledger: ledger,
	}
	mux := http.NewServeMux()
	registerRoutes(mux, cache, "test-token")

	tests := []struct {
		name         string
		body         string
		expectedCode int
		errContains  string
	}{
		{
			name:         "missing node",
			body:         `{"ram_mb":1024}`,
			expectedCode: http.StatusBadRequest,
			errContains:  "node is required",
		},
		{
			name:         "missing ram_mb",
			body:         `{"node":"node-a"}`,
			expectedCode: http.StatusBadRequest,
			errContains:  "ram_mb must be > 0",
		},
		{
			name:         "unknown node capacity",
			body:         `{"node":"unknown","ram_mb":1024}`,
			expectedCode: http.StatusBadRequest,
			errContains:  "capacity unknown",
		},
		{
			name:         "duplicate id",
			body:         `{"id":"dup","node":"node-a","ram_mb":1024}`,
			expectedCode: http.StatusConflict,
			errContains:  "duplicate ID",
		},
	}

	// Prime the duplicate case
	ledger.Reserve(reservation.Entry{ID: "dup", Node: "node-a", RAMMB: 512})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v2/reservations", strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer test-token")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tt.expectedCode {
				t.Fatalf("expected %d, got %d: %s", tt.expectedCode, rec.Code, rec.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if payload["ok"] != false {
				t.Fatalf("expected ok=false, got %#v", payload["ok"])
			}
			if got, _ := payload["error"].(string); !strings.Contains(got, tt.errContains) {
				t.Fatalf("expected error containing %q, got %q", tt.errContains, got)
			}
		})
	}
}

func TestV2ReservationsDetailNotFound(t *testing.T) {
	ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
	cache := &fakeCache{
		meta:   daemon.Metadata{Ready: true},
		ledger: ledger,
	}
	mux := http.NewServeMux()
	registerRoutes(mux, cache, "test-token")

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"get missing", http.MethodGet, "/v2/reservations/missing"},
		{"delete missing", http.MethodDelete, "/v2/reservations/missing"},
		{"heartbeat missing", http.MethodPost, "/v2/reservations/missing/heartbeat"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Authorization", "Bearer test-token")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestV2ReservationsLedgerUnavailable(t *testing.T) {
	cache := &fakeCache{
		meta:   daemon.Metadata{Ready: true},
		ledger: nil,
	}
	mux := http.NewServeMux()
	registerRoutes(mux, cache, "test-token")

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"list", http.MethodGet, "/v2/reservations"},
		{"create", http.MethodPost, "/v2/reservations"},
		{"get", http.MethodGet, "/v2/reservations/x"},
		{"delete", http.MethodDelete, "/v2/reservations/x"},
		{"heartbeat", http.MethodPost, "/v2/reservations/x/heartbeat"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body string
			if tt.method == http.MethodPost && tt.path == "/v2/reservations" {
				body = `{"node":"a","ram_mb":1}`
			}
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer test-token")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestV2ReservationsCreateAutoID(t *testing.T) {
	ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
	ledger.SetNodeCapacity("node-a", 16384)
	cache := &fakeCache{
		meta:   daemon.Metadata{Ready: true},
		ledger: ledger,
	}
	mux := http.NewServeMux()
	registerRoutes(mux, cache, "test-token")

	body := `{"node":"node-a","ram_mb":1024}`
	req := httptest.NewRequest(http.MethodPost, "/v2/reservations", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created reservation.Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected auto-generated id, got empty")
	}
	if !strings.HasPrefix(created.ID, "http-") {
		t.Fatalf("expected prefix 'http-', got %q", created.ID)
	}
	hexPart := strings.TrimPrefix(created.ID, "http-")
	if len(hexPart) != 16 {
		t.Fatalf("expected 16 hex chars after prefix, got %d: %q", len(hexPart), hexPart)
	}
}

func TestV2ReservationsCreateOversizedBody(t *testing.T) {
	ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
	ledger.SetNodeCapacity("node-a", 16384)
	cache := &fakeCache{
		meta:   daemon.Metadata{Ready: true},
		ledger: ledger,
	}
	mux := http.NewServeMux()
	registerRoutes(mux, cache, "test-token")

	// Create a JSON body slightly over the 1 MB limit
	huge := `{"key":"` + strings.Repeat("a", 1<<20+100) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v2/reservations", strings.NewReader(huge))
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["ok"] != false {
		t.Fatalf("expected ok=false, got %#v", payload["ok"])
	}
	got, _ := payload["error"].(string)
	if !strings.Contains(got, "too large") {
		t.Fatalf("expected error containing 'too large', got %q", got)
	}
}

func TestV2MethodValidationAndCacheState(t *testing.T) {
	tests := []struct {
		name           string
		cache          snapshotCache
		method         string
		path           string
		expectedStatus int
		errorContains  string
	}{
		{
			name: "cluster wrong method",
			cache: &fakeCache{
				snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy},
				meta: daemon.Metadata{Ready: true},
			},
			method:         http.MethodPost,
			path:           "/v2/cluster",
			expectedStatus: http.StatusMethodNotAllowed,
			errorContains:  "method not allowed",
		},
		{
			name: "cluster cache not ready",
			cache: &fakeCache{
				meta: daemon.Metadata{Ready: false},
			},
			method:         http.MethodGet,
			path:           "/v2/cluster",
			expectedStatus: http.StatusServiceUnavailable,
			errorContains:  "snapshot cache not ready",
		},
		{
			name: "nodes wrong method",
			cache: &fakeCache{
				snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy},
				meta: daemon.Metadata{Ready: true},
			},
			method:         http.MethodPost,
			path:           "/v2/nodes",
			expectedStatus: http.StatusMethodNotAllowed,
			errorContains:  "method not allowed",
		},
		{
			name: "nodes cache not ready",
			cache: &fakeCache{
				meta: daemon.Metadata{Ready: false},
			},
			method:         http.MethodGet,
			path:           "/v2/nodes",
			expectedStatus: http.StatusServiceUnavailable,
			errorContains:  "snapshot cache not ready",
		},
		{
			name: "metrics wrong method",
			cache: &fakeCache{
				meta: daemon.Metadata{Ready: true},
			},
			method:         http.MethodPost,
			path:           "/v2/metrics",
			expectedStatus: http.StatusMethodNotAllowed,
			errorContains:  "method not allowed",
		},
		{
			name:           "metrics require cache",
			cache:          nil,
			method:         http.MethodGet,
			path:           "/v2/metrics",
			expectedStatus: http.StatusServiceUnavailable,
			errorContains:  "snapshot cache unavailable",
		},
		{
			name: "doctor wrong method",
			cache: &fakeCache{
				meta: daemon.Metadata{Ready: true},
			},
			method:         http.MethodPost,
			path:           "/v2/doctor",
			expectedStatus: http.StatusMethodNotAllowed,
			errorContains:  "method not allowed",
		},
		{
			name:           "doctor require cache",
			cache:          nil,
			method:         http.MethodGet,
			path:           "/v2/doctor",
			expectedStatus: http.StatusServiceUnavailable,
			errorContains:  "snapshot cache unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			registerRoutes(mux, tt.cache, "test-token")

			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.path != "/v2/metrics" || tt.cache != nil {
				req.Header.Set("Authorization", "Bearer test-token")
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Fatalf("expected %d, got %d", tt.expectedStatus, rec.Code)
			}

			var payload map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if payload["ok"] != false {
				t.Fatalf("expected ok=false, got %#v", payload["ok"])
			}
			if got, _ := payload["error"].(string); !strings.Contains(got, tt.errorContains) {
				t.Fatalf("expected error containing %q, got %q", tt.errorContains, got)
			}
		})
	}
}

func TestV2MetricsEndpointExportsCacheAndNodeCounts(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, &fakeCache{
		snap: &models.ClusterSnapshot{
			Status: models.SnapshotDegraded,
			Nodes: []models.NodeFacts{
				{Name: "alpha", Status: models.StatusComplete},
				{Name: "beta", Status: models.StatusPartial},
			},
		},
		meta: daemon.Metadata{
			CacheAgeSec:   15,
			RefreshCount:  3,
			LastRefreshMs: 42,
			Stale:         true,
			Ready:         true,
		},
	}, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/v2/metrics", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/plain") {
		t.Fatalf("expected text/plain content type, got %q", got)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"axis_cache_age_seconds 15",
		"axis_cache_refresh_total 3",
		"axis_cache_refresh_duration_ms 42",
		"axis_cache_stale 1",
		"axis_cache_ready 1",
		"axis_nodes_total 2",
		"axis_nodes_healthy 1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected metrics body to contain %q, got %q", want, body)
		}
	}
}

func TestV2DoctorEndpointReportsCurrentHealth(t *testing.T) {
	tests := []struct {
		name              string
		cache             *fakeCache
		expectedOverall   string
		expectedStatuses  []string
		expectedSubstring string
	}{
		{
			name: "cache not ready",
			cache: &fakeCache{
				meta: daemon.Metadata{Ready: false},
			},
			expectedOverall:   "unhealthy",
			expectedStatuses:  []string{"fail"},
			expectedSubstring: "snapshot cache is not ready",
		},
		{
			name: "stale cache and degraded nodes",
			cache: &fakeCache{
				snap: &models.ClusterSnapshot{
					Nodes: []models.NodeFacts{
						{Name: "alpha", Status: models.StatusComplete},
						{Name: "beta", Status: models.StatusPartial},
					},
				},
				meta: daemon.Metadata{
					Ready:             true,
					Stale:             true,
					CacheAgeSec:       90,
					StaleThresholdSec: 30,
				},
			},
			expectedOverall:   "degraded",
			expectedStatuses:  []string{"warn", "warn"},
			expectedSubstring: "1 of 2 nodes are degraded",
		},
		{
			name: "fresh cache and healthy nodes",
			cache: &fakeCache{
				snap: &models.ClusterSnapshot{
					Nodes: []models.NodeFacts{
						{Name: "alpha", Status: models.StatusComplete},
						{Name: "beta", Status: models.StatusComplete},
					},
				},
				meta: daemon.Metadata{
					Ready:       true,
					CacheAgeSec: 12,
				},
			},
			expectedOverall:   "healthy",
			expectedStatuses:  []string{"pass", "pass"},
			expectedSubstring: "all 2 nodes healthy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			registerRoutes(mux, tt.cache, "test-token")

			req := httptest.NewRequest(http.MethodGet, "/v2/doctor", nil)
			req.Header.Set("Authorization", "Bearer test-token")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}

			var payload struct {
				Overall string              `json:"overall"`
				Checks  []V2DiagnosticCheck `json:"checks"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if payload.Overall != tt.expectedOverall {
				t.Fatalf("expected overall=%q, got %q", tt.expectedOverall, payload.Overall)
			}
			if len(payload.Checks) != len(tt.expectedStatuses) {
				t.Fatalf("expected %d checks, got %d", len(tt.expectedStatuses), len(payload.Checks))
			}
			for i, wantStatus := range tt.expectedStatuses {
				if payload.Checks[i].Status != wantStatus {
					t.Fatalf("check %d status = %q, want %q", i, payload.Checks[i].Status, wantStatus)
				}
			}
			if joined := rec.Body.String(); !strings.Contains(joined, tt.expectedSubstring) {
				t.Fatalf("expected response to contain %q, got %q", tt.expectedSubstring, joined)
			}
		})
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
	}, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/snapshot", nil)
	req.Header.Set("Authorization", "Bearer test-token")
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
	}, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/snapshot/meta", nil)
	req.Header.Set("Authorization", "Bearer test-token")
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
	registerRoutes(mux, nil, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/tools", nil)
	req.Header.Set("Authorization", "Bearer test-token")
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
	registerRoutes(mux, cache, "test-token")

	req := httptest.NewRequest(http.MethodPost, "/invalidate", nil)
	req.Header.Set("Authorization", "Bearer test-token")
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
	registerRoutes(mux, cache, "test-token")

	req := httptest.NewRequest(http.MethodPost, "/refresh", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if !cache.refreshed {
		t.Fatal("expected cache refresh to be triggered")
	}
}

func TestRefreshEndpointUsesExplicitTrigger(t *testing.T) {
	cache := &fakeCache{
		meta: daemon.Metadata{Ready: false},
	}
	mux := http.NewServeMux()
	registerRoutes(mux, cache, "test-token")

	req := httptest.NewRequest(http.MethodPost, "/refresh?trigger="+execution.StateChangeExecutionFinished, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if got := cache.Meta().LastRefreshTrigger; got != execution.StateChangeExecutionFinished {
		t.Fatalf("expected execution trigger, got %q", got)
	}
}

func TestRefreshEndpointRejectsUnsupportedTrigger(t *testing.T) {
	cache := &fakeCache{
		meta: daemon.Metadata{Ready: false},
	}
	mux := http.NewServeMux()
	registerRoutes(mux, cache, "test-token")

	req := httptest.NewRequest(http.MethodPost, "/refresh?trigger=totally-unknown", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestRunEndpointRequiresExplicitMode(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, nil, "test-token")

	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"description":"git status"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "mode is required") {
		t.Fatalf("expected mode-required error, got %q", rec.Body.String())
	}
}

func TestRunEndpointRequiresExplicitConfirmation(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, nil, "test-token")

	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"description":"git status","mode":"exec"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "confirm must be YES") {
		t.Fatalf("expected confirm-required error, got %q", rec.Body.String())
	}
}

func TestAuthentication(t *testing.T) {
	token := "secret-token"
	mux := http.NewServeMux()
	registerRoutes(mux, &fakeCache{
		snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy},
		meta: daemon.Metadata{Ready: true},
	}, token)

	tests := []struct {
		name           string
		method         string
		path           string
		authHeader     string
		expectedStatus int
	}{
		{"snapshot-no-auth", http.MethodGet, "/snapshot", "", http.StatusUnauthorized},
		{"snapshot-invalid-auth", http.MethodGet, "/snapshot", "Bearer wrong", http.StatusUnauthorized},
		{"snapshot-valid-auth", http.MethodGet, "/snapshot", "Bearer " + token, http.StatusOK},
		{"health-no-auth", http.MethodGet, "/health", "", http.StatusOK},
		{"run-no-auth", http.MethodPost, "/run", "", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rec.Code)
			}
		})
	}
}

func TestServeUDS(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")
	token := "uds-token"
	cache := &fakeCache{
		snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy},
	}

	errChan := make(chan error, 1)
	go func() {
		errChan <- Serve(socketPath, cache, token, false)
	}()

	// Wait for socket to appear
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}

	req, _ := http.NewRequest(http.MethodGet, "http://localhost/health", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("failed to connect to UDS: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Verify permissions
	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("socket file not found: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected 0600 permissions, got %o", info.Mode().Perm())
	}
}

func TestServeWithContextGracefulShutdown(t *testing.T) {
	// macOS limits Unix socket paths to 104 chars — use os.MkdirTemp with a short prefix.
	tmpDir, err := os.MkdirTemp("", "ax")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	socketPath := filepath.Join(tmpDir, "s.sock")
	cache := &fakeCache{
		snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy},
	}

	ctx, cancel := context.WithCancel(context.Background())
	errChan := make(chan error, 1)
	go func() {
		errChan <- ServeWithContext(ctx, socketPath, cache, "", false)
	}()

	// Wait for socket
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify it's serving
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}
	req, _ := http.NewRequest(http.MethodGet, "http://localhost/health", nil)
	if _, err := client.Do(req); err != nil {
		t.Fatalf("server not serving before cancel: %v", err)
	}

	// Cancel triggers graceful shutdown
	cancel()

	select {
	case err := <-errChan:
		if err != nil {
			t.Errorf("ServeWithContext returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeWithContext did not shut down within 5s after context cancel")
	}
}

func TestDefaultAddr(t *testing.T) {
	addr := DefaultAddr()
	if !strings.HasSuffix(addr, filepath.Join(".axis", "axis.sock")) {
		t.Errorf("unexpected DefaultAddr %q", addr)
	}
}

func TestPprofEndpointsEnabled(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "pprof.sock")
	cache := &fakeCache{
		snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- ServeWithContext(ctx, socketPath, cache, "", true)
	}()

	// Wait for socket to appear
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}

	paths := []string{"http://localhost/debug/pprof/", "http://localhost/debug/pprof/cmdline", "http://localhost/debug/pprof/symbol"}
	for _, p := range paths {
		req, _ := http.NewRequest(http.MethodGet, p, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", p, resp.StatusCode)
		}
	}
}

func TestPprofEndpointsDisabledByDefault(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "noprof.sock")
	cache := &fakeCache{
		snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- ServeWithContext(ctx, socketPath, cache, "", false)
	}()

	// Wait for socket to appear
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}

	req, _ := http.NewRequest(http.MethodGet, "http://localhost/debug/pprof/", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /debug/pprof/: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /debug/pprof/ = %d, want 404", resp.StatusCode)
	}
}

func TestRunTaskReturnsErrorWhenRuntimeFails(t *testing.T) {
	restore := stubLiveRuntime(t, nil, errors.New("node discovery failed"))
	defer restore()

	mux := http.NewServeMux()
	registerRoutes(mux, nil, "tok")

	body := `{"description":"run ollama","mode":"exec","confirm":"YES"}`
	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp RunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.OK {
		t.Error("expected ok=false when runtime fails")
	}
	if !strings.Contains(resp.Error, "node discovery failed") {
		t.Errorf("expected error message, got %q", resp.Error)
	}
}

func TestRunTaskStreamReturnsFinalResultWhenRuntimeFails(t *testing.T) {
	restore := stubLiveRuntime(t, nil, errors.New("node discovery failed"))
	defer restore()

	mux := http.NewServeMux()
	registerRoutes(mux, nil, "tok")

	body := `{"description":"run ollama","mode":"exec","confirm":"YES"}`
	req := httptest.NewRequest(http.MethodPost, "/run?stream=1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", daemon.RunStreamContentType)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != daemon.RunStreamContentType {
		t.Fatalf("Content-Type = %q, want %q", got, daemon.RunStreamContentType)
	}

	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 stream event, got %d: %q", len(lines), rec.Body.String())
	}

	var event daemon.RunStreamEvent
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("unmarshal stream event: %v", err)
	}
	if event.Type != daemon.RunStreamEventResult || event.Result == nil {
		t.Fatalf("unexpected stream event: %+v", event)
	}
	if event.Result.OK {
		t.Fatal("expected ok=false when runtime fails")
	}
	if !strings.Contains(event.Result.Error, "node discovery failed") {
		t.Fatalf("expected runtime failure in final stream result, got %q", event.Result.Error)
	}
}

func TestRunReturnsErrorForUnconfiguredRemoteNode(t *testing.T) {
	rt := testRuntimeContext(
		[]models.NodeFacts{testNode("mac", "mac.local", 1024, 512, "critical")},
		nil, nil, &skills.Store{}, nil, nil,
	)
	restore := stubLiveRuntime(t, rt, nil)
	defer restore()

	mux := http.NewServeMux()
	registerRoutes(mux, nil, "tok")

	body := `{"description":"echo ok","mode":"exec","confirm":"YES"}`
	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp RunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Description != "echo ok" {
		t.Errorf("expected description echoed, got %q", resp.Description)
	}
	if resp.OK {
		t.Error("expected ok=false when node config is missing")
	}
	if !strings.Contains(resp.Error, `node "mac" not found in config`) {
		t.Errorf("expected config error, got %q", resp.Error)
	}
}

func TestRunEndpointRefreshesCacheOnExecutionStateChanges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	rt := testRuntimeContext(
		[]models.NodeFacts{testNode("local", "localhost", 8192, 4096, "low")},
		nil,
		&state.ClusterState{Nodes: map[string]state.NodeState{}},
		&skills.Store{},
		nil,
		func() *reservation.Ledger {
			l := reservation.NewLedger(reservation.DefaultLimits(), nil)
			l.SetNodeCapacity("local", 8192)
			return l
		}(),
	)
	restoreRuntime := stubLiveRuntime(t, rt, nil)
	defer restoreRuntime()

	prevShell := execution.RunLocalShell
	execution.RunLocalShell = func(context.Context, string, []string) ([]byte, int64, error) {
		return []byte("ok\n"), 0, nil
	}
	defer func() { execution.RunLocalShell = prevShell }()

	cache := &fakeCache{}
	mux := http.NewServeMux()
	registerRoutes(mux, cache, "tok")

	body := `{"description":"echo ok","mode":"exec","confirm":"YES"}`
	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cache.mu.Lock()
		got := append([]string(nil), cache.refreshTriggers...)
		cache.mu.Unlock()
		if len(got) >= 2 {
			if got[0] != execution.StateChangeExecutionReserved || got[1] != execution.StateChangeExecutionFinished {
				t.Fatalf("unexpected refresh trigger sequence: %v", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()
	t.Fatalf("expected execution-triggered cache refreshes, got %v", cache.refreshTriggers)
}

func TestRunEndpointAddsOwnerProvenanceToGuardedRequest(t *testing.T) {
	restoreRuntime := stubLiveRuntime(t, &runtimectx.Context{}, nil)
	defer restoreRuntime()

	prev := runLiveGuarded
	t.Cleanup(func() { runLiveGuarded = prev })
	runLiveGuarded = func(_ context.Context, gotRT *runtimectx.Context, req execution.GuardedExecutionRequest) (execution.GuardedExecutionResult, error) {
		if gotRT == nil {
			t.Fatal("expected runtime context")
		}
		if req.OwnerSurface != execution.OwnerSurfaceHTTPRun {
			t.Fatalf("OwnerSurface = %q, want %q", req.OwnerSurface, execution.OwnerSurfaceHTTPRun)
		}
		if req.OwnerLabel != "203.0.113.9" {
			t.Fatalf("OwnerLabel = %q, want 203.0.113.9", req.OwnerLabel)
		}
		return execution.GuardedExecutionResult{OK: true, Node: "alpha"}, nil
	}

	mux := http.NewServeMux()
	registerRoutes(mux, nil, "tok")

	body := `{"description":"echo ok","mode":"exec","confirm":"YES"}`
	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	req.RemoteAddr = "203.0.113.9:4567"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRunEndpointAcceptsSignedForwardedExecutionOrigin(t *testing.T) {
	restoreRuntime := stubLiveRuntime(t, &runtimectx.Context{}, nil)
	defer restoreRuntime()

	want := models.NewExecutionOrigin("upstream-node", "upstream.local", "abc-123")
	prev := runLiveGuarded
	t.Cleanup(func() { runLiveGuarded = prev })
	runLiveGuarded = func(_ context.Context, _ *runtimectx.Context, req execution.GuardedExecutionRequest) (execution.GuardedExecutionResult, error) {
		if req.OriginOverride != want {
			t.Fatalf("OriginOverride = %+v, want %+v", req.OriginOverride, want)
		}
		return execution.GuardedExecutionResult{OK: true, Node: "alpha"}, nil
	}

	mux := http.NewServeMux()
	registerRoutes(mux, nil, "tok")

	body := `{"description":"echo ok","mode":"exec","confirm":"YES"}`
	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	if err := auth.SetForwardedExecutionOriginHeaders(req.Header, want, "tok", time.Now()); err != nil {
		t.Fatalf("SetForwardedExecutionOriginHeaders: %v", err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestRunEndpointStreamEmitsReadyOutputAndResult(t *testing.T) {
	restoreRuntime := stubLiveRuntime(t, &runtimectx.Context{}, nil)
	defer restoreRuntime()

	prev := runLiveGuarded
	t.Cleanup(func() { runLiveGuarded = prev })
	runLiveGuarded = func(_ context.Context, _ *runtimectx.Context, req execution.GuardedExecutionRequest) (execution.GuardedExecutionResult, error) {
		if req.OnReady == nil || req.Stdout == nil || req.Stderr == nil {
			t.Fatalf("expected stream callbacks/writers to be wired")
		}
		ready := execution.GuardedExecutionResult{Node: "alpha", FitScore: 88, Command: "echo ok"}
		req.OnReady(ready)
		req.OnStateChange(context.Background(), execution.StateChangeExecutionReserved, execution.GuardedExecutionResult{Node: "alpha"})
		if _, err := req.Stdout.Write([]byte("hello\n")); err != nil {
			t.Fatalf("stdout write: %v", err)
		}
		if _, err := req.Stderr.Write([]byte("warn\n")); err != nil {
			t.Fatalf("stderr write: %v", err)
		}
		req.OnStateChange(context.Background(), execution.StateChangeExecutionFinished, execution.GuardedExecutionResult{Node: "alpha", OK: true})
		return execution.GuardedExecutionResult{
			OK:      true,
			Node:    "alpha",
			Command: "echo ok",
			Output:  "hello\nwarn",
		}, nil
	}

	mux := http.NewServeMux()
	registerRoutes(mux, nil, "tok")

	body := `{"description":"echo ok","mode":"exec","confirm":"YES"}`
	req := httptest.NewRequest(http.MethodPost, "/run?stream=1", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Accept", daemon.RunStreamContentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != daemon.RunStreamContentType {
		t.Fatalf("Content-Type = %q, want %q", got, daemon.RunStreamContentType)
	}

	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 6 {
		t.Fatalf("expected 6 stream events, got %d: %q", len(lines), rec.Body.String())
	}

	var events []daemon.RunStreamEvent
	for _, line := range lines {
		var event daemon.RunStreamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("unmarshal stream event %q: %v", line, err)
		}
		events = append(events, event)
	}

	if events[0].Type != daemon.RunStreamEventReady || events[0].Result == nil || events[0].Result.Node != "alpha" {
		t.Fatalf("unexpected ready event: %+v", events[0])
	}
	if events[1].Type != daemon.RunStreamEventStateChange || events[1].Trigger != execution.StateChangeExecutionReserved {
		t.Fatalf("unexpected state-change event: %+v", events[1])
	}
	if events[2].Type != daemon.RunStreamEventStdout || events[2].Text != "hello\n" {
		t.Fatalf("unexpected stdout event: %+v", events[2])
	}
	if events[3].Type != daemon.RunStreamEventStderr || events[3].Text != "warn\n" {
		t.Fatalf("unexpected stderr event: %+v", events[3])
	}
	if events[4].Type != daemon.RunStreamEventStateChange || events[4].Trigger != execution.StateChangeExecutionFinished {
		t.Fatalf("unexpected finished state-change event: %+v", events[4])
	}
	if events[5].Type != daemon.RunStreamEventResult || events[5].Result == nil || !events[5].Result.OK {
		t.Fatalf("unexpected final event: %+v", events[5])
	}
}

func TestRunEndpointRejectsInvalidSignedForwardedExecutionOrigin(t *testing.T) {
	restoreRuntime := stubLiveRuntime(t, &runtimectx.Context{}, nil)
	defer restoreRuntime()

	mux := http.NewServeMux()
	registerRoutes(mux, nil, "tok")

	body := `{"description":"echo ok","mode":"exec","confirm":"YES"}`
	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set(auth.ForwardedOriginNodeHeader, "upstream-node")
	req.Header.Set(auth.ForwardedOriginTimeHeader, time.Now().UTC().Format(time.RFC3339Nano))
	req.Header.Set(auth.ForwardedOriginSignatureHeader, "deadbeef")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func stubLiveRuntime(t *testing.T, rt *runtimectx.Context, err error) func() {
	t.Helper()
	prev := loadLiveRuntime
	loadLiveRuntime = func(context.Context) (*runtimectx.Context, error) {
		return rt, err
	}
	return func() {
		loadLiveRuntime = prev
	}
}

func testRuntimeContext(nodes []models.NodeFacts, cfgNodes []config.NodeConfig, st *state.ClusterState, store *skills.Store, warnings []models.Warning, ledger *reservation.Ledger) *runtimectx.Context {
	return &runtimectx.Context{
		Config: &config.Config{Nodes: cfgNodes},
		Snapshot: &models.ClusterSnapshot{
			Status:   models.SnapshotHealthy,
			Nodes:    nodes,
			Summary:  summarizeNodes(nodes),
			Warnings: warnings,
		},
		State:  st,
		Skills: store,
		Ledger: ledger,
	}
}

func summarizeNodes(nodes []models.NodeFacts) models.ClusterSummary {
	summary := models.ClusterSummary{TotalNodes: len(nodes)}
	for _, node := range nodes {
		if node.Status == models.StatusComplete || node.Status == models.StatusPartial {
			summary.ReachableNodes++
		}
		if node.Resources == nil {
			continue
		}
		summary.TotalRAMMB += node.Resources.RAMTotalMB
		summary.TotalFreeRAMMB += node.Resources.RAMFreeMB
		summary.TotalReservableMB += node.ReservableRAM()
		summary.TotalReservedMB += node.RAMReservedMB
		summary.TotalAllocatableMB += node.RAMAllocatableMB
	}
	return summary
}

func testNode(name, hostname string, totalRAM, freeRAM int64, pressure string, tools ...string) models.NodeFacts {
	node := models.NodeFacts{
		Name:             name,
		Hostname:         hostname,
		Status:           models.StatusComplete,
		RAMAllocatableMB: models.ReservableRAMMB(totalRAM, freeRAM),
		RAMReservedMB:    0,
		Resources: &models.Resources{
			RAMTotalMB: totalRAM,
			RAMFreeMB:  freeRAM,
			Pressure:   pressure,
			CPUCores:   8,
		},
	}
	for _, tool := range tools {
		node.Tools = append(node.Tools, models.ToolInfo{Name: tool, Version: "test"})
	}
	return node
}

func testTurboNode(name, hostname string, verified bool) models.NodeFacts {
	node := testNode(name, hostname, 16384, 8192, "low", "llama-server")
	node.RAMAllocatableMB = models.ReservableRAMMB(16384, 8192)
	node.Resources.PressureSource = "linux-psi"
	node.Resources.PressureStall10 = 6.5
	node.TurboQuant = &models.TurboQuantInfo{
		Supported:    true,
		Verified:     verified,
		Backends:     []string{"llama.cpp"},
		Capabilities: []string{"backend-probed", "ctx-size-flag", "flash-attn-flag", "llama.cpp-runtime"},
	}
	return node
}

func TestV2PlacementDryRun(t *testing.T) {
	prev := loadLiveRuntime
	loadLiveRuntime = func(context.Context) (*runtimectx.Context, error) {
		return &runtimectx.Context{State: &state.ClusterState{}}, nil
	}
	defer func() { loadLiveRuntime = prev }()

	cache := &fakeCache{
		meta: daemon.Metadata{Ready: true},
		snap: &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Nodes: []models.NodeFacts{
				{
					Name: "node-a",
					Status: models.StatusComplete,
					Resources: &models.Resources{
						RAMTotalMB: 16384,
						RAMAllocatableMB: 12000,
						RAMFreeMB: 8000,
					},
				},
			},
		},
	}
	mux := http.NewServeMux()
	registerRoutes(mux, cache, "test-token")

	// Get logic
	req := httptest.NewRequest(http.MethodGet, "/v2/placement/dry-run?description=run+llama3", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Post logic
	body := `{"description":"run llama3"}`
	req = httptest.NewRequest(http.MethodPost, "/v2/placement/dry-run", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestV2BatchPlace(t *testing.T) {
	prev := loadLiveRuntime
	loadLiveRuntime = func(context.Context) (*runtimectx.Context, error) {
		return &runtimectx.Context{State: &state.ClusterState{}}, nil
	}
	defer func() { loadLiveRuntime = prev }()

	cache := &fakeCache{
		meta: daemon.Metadata{Ready: true},
		snap: &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Nodes: []models.NodeFacts{
				{
					Name: "node-a",
					Status: models.StatusComplete,
					Resources: &models.Resources{
						RAMTotalMB: 16384,
						RAMAllocatableMB: 12000,
						RAMFreeMB: 8000,
					},
				},
			},
		},
	}
	mux := http.NewServeMux()
	registerRoutes(mux, cache, "test-token")

	body := `{"tasks":[{"id":"1","description":"small task"},{"id":"2","description":"another task"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v2/batch/place", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}
