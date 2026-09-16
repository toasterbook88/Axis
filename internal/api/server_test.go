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

func TestV2MetricsRequiresAuth(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, &fakeCache{
		snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy},
		meta: daemon.Metadata{Ready: true},
	}, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/v2/metrics", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /v2/metrics: expected 401, got %d", rec.Code)
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

func TestPprofEndpointsRequireAuthWhenTokenSet(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "pprofauth.sock")
	cache := &fakeCache{
		snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy},
	}

	const token = "test-token-value"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- ServeWithContext(ctx, socketPath, cache, token, true)
	}()

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

	// Without the bearer token, the profiling endpoint must be rejected.
	req, _ := http.NewRequest(http.MethodGet, "http://localhost/debug/pprof/cmdline", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /debug/pprof/cmdline (no auth): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /debug/pprof/cmdline (no auth) = %d, want 401", resp.StatusCode)
	}

	// With the bearer token, the endpoint is reachable.
	req, _ = http.NewRequest(http.MethodGet, "http://localhost/debug/pprof/cmdline", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET /debug/pprof/cmdline (auth): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /debug/pprof/cmdline (auth) = %d, want 200", resp.StatusCode)
	}
}

func TestPprofEndpointsRejectEmptyToken(t *testing.T) {
	mux := http.NewServeMux()
	registerPprofRoutes(mux, "")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("empty-token pprof status = %d, want %d", rec.Code, http.StatusUnauthorized)
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
