package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
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

func TestDefaultAddr(t *testing.T) {
	addr := DefaultAddr()
	if !strings.HasSuffix(addr, filepath.Join(".axis", "axis.sock")) {
		t.Errorf("unexpected DefaultAddr %q", addr)
	}
}

func TestExitCode(t *testing.T) {
	if got := exitCode(nil); got != 0 {
		t.Errorf("exitCode(nil) = %d, want 0", got)
	}
	if got := exitCode(errors.New("generic")); got != 1 {
		t.Errorf("exitCode(generic) = %d, want 1", got)
	}
	cmd := exec.Command("false")
	_ = cmd.Run()
	if err := cmd.Run(); err != nil {
		if got := exitCode(err); got == 0 {
			t.Errorf("exitCode(ExitError) = 0, want non-zero")
		}
	}
}

func TestFindNodeConfig(t *testing.T) {
	cfg := &config.Config{
		Nodes: []config.NodeConfig{
			{Name: "alpha"},
			{Name: "beta"},
		},
	}
	if n, ok := findNodeConfig(cfg, "alpha"); !ok || n.Name != "alpha" {
		t.Errorf("expected to find alpha, got ok=%v name=%q", ok, n.Name)
	}
	if n, ok := findNodeConfig(cfg, "ALPHA"); !ok || n.Name != "alpha" {
		t.Errorf("expected case-insensitive match, got ok=%v name=%q", ok, n.Name)
	}
	if _, ok := findNodeConfig(cfg, "gamma"); ok {
		t.Error("expected gamma not found")
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

func TestRunTaskReturnsNoSuitableNode(t *testing.T) {
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
}

func TestRecordSuccess(t *testing.T) {
	store := &skills.Store{}
	rc := &runnerContext{
		cfg:        &config.Config{},
		snap:       &models.ClusterSnapshot{},
		st:         nil,
		skillStore: store,
	}
	// recordSuccess ignores Save errors; should not panic
	recordSuccess(rc, "test task", "echo hello", "mac")
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
