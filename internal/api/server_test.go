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
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

type fakeCache struct {
	mu              sync.Mutex
	snap            *models.ClusterSnapshot
	meta            daemon.Metadata
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
		errChan <- Serve(socketPath, cache, token)
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
		errChan <- ServeWithContext(ctx, socketPath, cache, "")
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

func TestRunReturnsNoSuitableNode(t *testing.T) {
	rt := testRuntimeContext(
		[]models.NodeFacts{testNode("mac", "mac.local", 1024, 512, "critical")},
		nil, nil, &skills.Store{}, nil,
	)
	restore := stubLiveRuntime(t, rt, nil)
	defer restore()

	mux := http.NewServeMux()
	registerRoutes(mux, nil, "tok")

	// Request 100GB RAM — no node will qualify
	body := `{"description":"run 100GB inference","mode":"exec","confirm":"YES"}`
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
	if resp.Description != "run 100GB inference" {
		t.Errorf("expected description echoed, got %q", resp.Description)
	}
	if resp.OK {
		t.Error("expected ok=false when no node is suitable")
	}
	if !strings.Contains(resp.Error, "too small for heavy model") {
		t.Errorf("expected heavy-model safety detail, got %q", resp.Error)
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

func testRuntimeContext(nodes []models.NodeFacts, cfgNodes []config.NodeConfig, st *state.ClusterState, store *skills.Store, warnings []models.Warning) *runtimectx.Context {
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
		summary.TotalReservedMB += node.Resources.RAMReservedMB
		summary.TotalAllocatableMB += node.Resources.RAMAllocatableMB
	}
	return summary
}

func testNode(name, hostname string, totalRAM, freeRAM int64, pressure string, tools ...string) models.NodeFacts {
	node := models.NodeFacts{
		Name:     name,
		Hostname: hostname,
		Status:   models.StatusComplete,
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
