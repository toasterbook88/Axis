package axismcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpproto "github.com/mark3labs/mcp-go/mcp"
	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/events"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/state"
)

func TestNewServer(t *testing.T) {
	s := NewServer(false, "", nil)
	if s == nil {
		t.Fatal("expected non-nil server")
	}

	sCached := NewServer(true, "http://localhost:8080", nil)
	if sCached == nil {
		t.Fatal("expected non-nil cached server")
	}
}

func TestToolResultJSON(t *testing.T) {
	result, err := toolResultJSON(map[string]string{"key": "value"})
	if err != nil {
		t.Fatalf("toolResultJSON: %v", err)
	}
	if result.IsError {
		t.Fatal("expected non-error result")
	}
	m, ok := result.StructuredContent.(map[string]string)
	if !ok {
		t.Fatalf("expected map, got %T", result.StructuredContent)
	}
	if m["key"] != "value" {
		t.Fatalf("unexpected value: %v", m["key"])
	}
}

func TestClusterSnapshotResource(t *testing.T) {
	restore := stubMCPRuntime(t, &runtimectx.Context{
		Snapshot: &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Summary: models.ClusterSummary{
				TotalNodes: 5,
			},
		},
	}, nil)
	defer restore()

	contents, err := clusterSnapshotResource(context.Background(), mcpproto.ReadResourceRequest{}, NewSessionCache(30*time.Second, false, ""))
	if err != nil {
		t.Fatalf("clusterSnapshotResource: %v", err)
	}
	if len(contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(contents))
	}
	text, ok := contents[0].(mcpproto.TextResourceContents)
	if !ok {
		t.Fatalf("expected TextResourceContents, got %T", contents[0])
	}
	if text.URI != clusterSnapshotURI {
		t.Fatalf("unexpected URI: %s", text.URI)
	}
	if text.MIMEType != "application/json" {
		t.Fatalf("unexpected MIME type: %s", text.MIMEType)
	}
	if !strings.Contains(text.Text, `"total_nodes": 5`) {
		t.Fatalf("expected snapshot content, got: %s", text.Text)
	}
}

func TestClusterSnapshotResourceError(t *testing.T) {
	restore := stubMCPRuntime(t, nil, errors.New("runtime down"))
	defer restore()

	_, err := clusterSnapshotResource(context.Background(), mcpproto.ReadResourceRequest{}, NewSessionCache(30*time.Second, false, ""))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestAxisToolsTool(t *testing.T) {
	result, err := axisToolsTool(context.Background(), toolRequest(nil))
	if err != nil {
		t.Fatalf("axisToolsTool: %v", err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}
	resp, ok := result.StructuredContent.(daemon.ToolsResponse)
	if !ok {
		t.Fatalf("expected ToolsResponse, got %T", result.StructuredContent)
	}
	if len(resp.Tools) == 0 {
		t.Fatal("expected non-empty tools list")
	}
}

func TestAxisHealthToolCached(t *testing.T) {
	t.Setenv(auth.TokenEnvVar, "tok")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/snapshot/meta" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(daemon.Metadata{
			Source:             "daemon-cache",
			Ready:              true,
			RefreshIntervalSec: 60,
			Version:            daemon.Version,
			CacheAgeSec:        12,
			Stale:              false,
		})
	}))
	defer server.Close()

	result, err := axisHealthTool(context.Background(), toolRequest(nil), NewSessionCache(30*time.Second, true, server.URL))
	if err != nil {
		t.Fatalf("axisHealthTool: %v", err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}
	payload, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", result.StructuredContent)
	}
	if payload["cache_ready"] != true {
		t.Fatalf("expected cache_ready true, got %v", payload["cache_ready"])
	}
}

func TestAxisHealthToolLive(t *testing.T) {
	restore := stubMCPRuntime(t, &runtimectx.Context{
		Snapshot: &models.ClusterSnapshot{
			Status:    models.SnapshotDegraded,
			Warnings:  []models.Warning{{Kind: "test", Message: "warn"}},
			Freshness: &models.DiscoveryFreshness{Source: "udp"},
		},
	}, nil)
	defer restore()

	result, err := axisHealthTool(context.Background(), toolRequest(nil), NewSessionCache(30*time.Second, false, ""))
	if err != nil {
		t.Fatalf("axisHealthTool: %v", err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}
	payload, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", result.StructuredContent)
	}
	if payload["snapshot_status"] != models.SnapshotDegraded {
		t.Fatalf("unexpected status: %v", payload["snapshot_status"])
	}
	if payload["warnings"] != 1 {
		t.Fatalf("unexpected warnings: %v", payload["warnings"])
	}
	if payload["discovery_freshness"] == nil {
		t.Fatal("expected freshness in payload")
	}
}

func TestAxisHealthToolLiveNilSnapshot(t *testing.T) {
	restore := stubMCPRuntime(t, &runtimectx.Context{
		Snapshot: nil,
	}, nil)
	defer restore()

	result, err := axisHealthTool(context.Background(), toolRequest(nil), NewSessionCache(30*time.Second, false, ""))
	if err != nil {
		t.Fatalf("axisHealthTool: %v", err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}
	payload, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", result.StructuredContent)
	}
	if payload["snapshot_status"] != nil {
		t.Fatalf("expected no snapshot_status, got %v", payload["snapshot_status"])
	}
}

func TestIPAddrToolSuccess(t *testing.T) {
	restoreLookPath := stubLookPath(t, func(name string) (string, error) {
		if name == "ip" {
			return "/sbin/ip", nil
		}
		return "", errors.New("missing")
	})
	defer restoreLookPath()
	restoreExec := stubExecCommand(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "bash", "-lc", "printf ip-ok")
	})
	defer restoreExec()

	result, err := ipAddrTool(context.Background(), toolRequest(nil))
	if err != nil {
		t.Fatalf("ipAddrTool: %v", err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}
	data, ok := result.StructuredContent.(commandResult)
	if !ok {
		t.Fatalf("expected commandResult, got %T", result.StructuredContent)
	}
	if data.Output != "ip-ok" {
		t.Fatalf("unexpected output: %q", data.Output)
	}
}

func TestIPAddrToolFallback(t *testing.T) {
	restoreLookPath := stubLookPath(t, func(name string) (string, error) {
		if name == "ifconfig" {
			return "/sbin/ifconfig", nil
		}
		return "", errors.New("missing")
	})
	defer restoreLookPath()
	restoreExec := stubExecCommand(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "bash", "-lc", "printf ifconfig-ok")
	})
	defer restoreExec()

	result, err := ipAddrTool(context.Background(), toolRequest(nil))
	if err != nil {
		t.Fatalf("ipAddrTool: %v", err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}
	data, ok := result.StructuredContent.(commandResult)
	if !ok {
		t.Fatalf("expected commandResult, got %T", result.StructuredContent)
	}
	if data.Output != "ifconfig-ok" {
		t.Fatalf("unexpected output: %q", data.Output)
	}
}

func TestIPAddrToolNoCommandFound(t *testing.T) {
	restoreLookPath := stubLookPath(t, func(string) (string, error) {
		return "", errors.New("missing")
	})
	defer restoreLookPath()

	result, err := ipAddrTool(context.Background(), toolRequest(nil))
	if err != nil {
		t.Fatalf("ipAddrTool: %v", err)
	}
	if result.IsError {
		t.Fatal("expected non-error tool result")
	}
	data, ok := result.StructuredContent.(commandResult)
	if !ok {
		t.Fatalf("expected commandResult, got %T", result.StructuredContent)
	}
	if data.Available {
		t.Fatal("expected unavailable")
	}
	if !strings.Contains(data.Error, "no supported network interface command found") {
		t.Fatalf("unexpected error: %q", data.Error)
	}
}

func TestTailscaleStatusTool(t *testing.T) {
	restoreLookPath := stubLookPath(t, func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	})
	defer restoreLookPath()
	restoreExec := stubExecCommand(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "bash", "-lc", "printf tailscale-ok")
	})
	defer restoreExec()

	result, err := tailscaleStatusTool(context.Background(), toolRequest(nil))
	if err != nil {
		t.Fatalf("tailscaleStatusTool: %v", err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}
	data, ok := result.StructuredContent.(commandResult)
	if !ok {
		t.Fatalf("expected commandResult, got %T", result.StructuredContent)
	}
	if data.Output != "tailscale-ok" {
		t.Fatalf("unexpected output: %q", data.Output)
	}
}

func TestTailscalePingToolMissingPeer(t *testing.T) {
	result, err := tailscalePingTool(context.Background(), toolRequest(nil))
	if err != nil {
		t.Fatalf("tailscalePingTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error")
	}
}

func TestTailscalePingToolSuccess(t *testing.T) {
	restoreLookPath := stubLookPath(t, func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	})
	defer restoreLookPath()
	restoreExec := stubExecCommand(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "bash", "-lc", "printf pong")
	})
	defer restoreExec()

	result, err := tailscalePingTool(context.Background(), toolRequest(map[string]any{
		"peer": "peer-1",
	}))
	if err != nil {
		t.Fatalf("tailscalePingTool: %v", err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}
	data, ok := result.StructuredContent.(commandResult)
	if !ok {
		t.Fatalf("expected commandResult, got %T", result.StructuredContent)
	}
	if data.Output != "pong" {
		t.Fatalf("unexpected output: %q", data.Output)
	}
}

func TestWireguardStatusTool(t *testing.T) {
	restoreLookPath := stubLookPath(t, func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	})
	defer restoreLookPath()
	restoreExec := stubExecCommand(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "bash", "-lc", "printf wg-ok")
	})
	defer restoreExec()

	result, err := wireguardStatusTool(context.Background(), toolRequest(nil))
	if err != nil {
		t.Fatalf("wireguardStatusTool: %v", err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}
	data, ok := result.StructuredContent.(commandResult)
	if !ok {
		t.Fatalf("expected commandResult, got %T", result.StructuredContent)
	}
	if data.Output != "wg-ok" {
		t.Fatalf("unexpected output: %q", data.Output)
	}
}

func TestDockerPSTool(t *testing.T) {
	restoreLookPath := stubLookPath(t, func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	})
	defer restoreLookPath()
	restoreExec := stubExecCommand(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "bash", "-lc", "printf docker-ok")
	})
	defer restoreExec()

	result, err := dockerPSTool(context.Background(), toolRequest(nil))
	if err != nil {
		t.Fatalf("dockerPSTool: %v", err)
	}
	if result.IsError {
		t.Fatal("expected success")
	}
	data, ok := result.StructuredContent.(commandResult)
	if !ok {
		t.Fatalf("expected commandResult, got %T", result.StructuredContent)
	}
	if data.Output != "docker-ok" {
		t.Fatalf("unexpected output: %q", data.Output)
	}
}

func TestRunFirstAvailableCommandEmptyCandidates(t *testing.T) {
	result := runFirstAvailableCommand(context.Background())
	if result.Available {
		t.Fatal("expected unavailable")
	}
}

func TestCurrentSnapshotCacheError(t *testing.T) {
	restoreFetch := stubCachedSnapshotFetcher(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return nil, "", errors.New("cache down")
	})
	defer restoreFetch()

	_, err := currentSnapshot(context.Background(), true, "http://bad")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCurrentPlacementInputsCacheError(t *testing.T) {
	restoreFetch := stubCachedSnapshotFetcher(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return nil, "", errors.New("cache down")
	})
	defer restoreFetch()

	_, _, err := currentPlacementInputs(context.Background(), true, "http://bad")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCurrentPlacementInputsStateError(t *testing.T) {
	restoreFetch := stubCachedSnapshotFetcher(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{Status: models.SnapshotHealthy}, "cache", nil
	})
	defer restoreFetch()
	restoreState := stubMCPStateLoader(t, func() (*state.ClusterState, error) {
		return nil, errors.New("state broken")
	})
	defer restoreState()

	_, _, err := currentPlacementInputs(context.Background(), true, "http://bad")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGitStatusTool(t *testing.T) {
	s := NewServer(false, "", nil)
	tool, ok := s.ListTools()["git_status"]
	if !ok {
		t.Fatal("expected git_status tool to be registered")
	}
	res, err := tool.Handler(context.Background(), toolRequest(nil))
	if err != nil {
		t.Fatalf("git_status handler: %v", err)
	}
	if res.IsError {
		t.Fatal("expected success")
	}
}

func TestLifecycleEventTools(t *testing.T) {
	tempDir := t.TempDir()
	events.SetLogPath(filepath.Join(tempDir, "events.jsonl"))
	defer events.SetLogPath("")
	defer events.FlushEvents(1 * time.Second)

	s := NewServer(false, "", nil)

	// Test list_lifecycle_events
	listTool, ok := s.ListTools()["list_lifecycle_events"]
	if !ok {
		t.Fatal("expected list_lifecycle_events tool to be registered")
	}
	res, err := listTool.Handler(context.Background(), toolRequest(nil))
	if err != nil {
		t.Fatalf("list_lifecycle_events handler: %v", err)
	}
	if res.IsError {
		t.Fatal("expected success")
	}

	// Test register_event_interest
	registerTool, ok := s.ListTools()["register_event_interest"]
	if !ok {
		t.Fatal("expected register_event_interest tool to be registered")
	}
	resReg, err := registerTool.Handler(context.Background(), toolRequest(map[string]any{
		"events": "task.placement.requested,reservation.granted",
	}))
	if err != nil {
		t.Fatalf("register_event_interest handler: %v", err)
	}
	if resReg.IsError {
		t.Fatal("expected success")
	}

	// Verify interest was registered
	resListAfter, err := listTool.Handler(context.Background(), toolRequest(nil))
	if err != nil {
		t.Fatalf("list_lifecycle_events handler: %v", err)
	}
	var listResp struct {
		Interests map[string][]string `json:"interests"`
	}
	contentJSON, err := json.Marshal(resListAfter.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contentJSON, &listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Interests["task.placement.requested"]) == 0 {
		t.Error("expected registered interest for task.placement.requested")
	}

	// Test get_recent_events
	events.SetEventBufferSize(10)
	events.EmitToBuffer(nil, "task.placement.requested", map[string]any{"task_id": "test-task"})
	events.FlushEvents(1 * time.Second)

	getTool, ok := s.ListTools()["get_recent_events"]
	if !ok {
		t.Fatal("expected get_recent_events tool to be registered")
	}
	resGet, err := getTool.Handler(context.Background(), toolRequest(map[string]any{
		"limit": 5,
	}))
	if err != nil {
		t.Fatalf("get_recent_events handler: %v", err)
	}
	if resGet.IsError {
		t.Fatal("expected success")
	}

	var getResp struct {
		Events []events.Event `json:"events"`
		Count  int            `json:"count"`
	}
	contentGetJSON, err := json.Marshal(resGet.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contentGetJSON, &getResp); err != nil {
		t.Fatal(err)
	}
	if getResp.Count < 1 {
		t.Errorf("expected at least 1 event, got %d", getResp.Count)
	}
}

type mockClientSession struct {
	id          string
	initialized bool
	ch          chan mcpproto.JSONRPCNotification
}

func (m *mockClientSession) SessionID() string {
	return m.id
}

func (m *mockClientSession) Initialize() {
	m.initialized = true
}

func (m *mockClientSession) Initialized() bool {
	return m.initialized
}

func (m *mockClientSession) NotificationChannel() chan<- mcpproto.JSONRPCNotification {
	return m.ch
}

func TestMCPPushNotifications(t *testing.T) {
	tempDir := t.TempDir()
	events.SetLogPath(filepath.Join(tempDir, "events.jsonl"))
	defer events.SetLogPath("")
	defer events.FlushEvents(1 * time.Second)
	events.SetEventBufferSize(10)
	events.SetCortexClient(nil)

	s := NewServer(false, "", nil)

	ch := make(chan mcpproto.JSONRPCNotification, 5)
	session := &mockClientSession{
		id: "test-session-123",
		ch: ch,
	}
	session.Initialize()

	if err := s.RegisterSession(context.Background(), session); err != nil {
		t.Fatalf("failed to register session: %v", err)
	}
	defer s.UnregisterSession(context.Background(), "test-session-123")

	// Emit an event
	events.EmitToBuffer(nil, "task.placement.requested", map[string]any{"task_id": "test-push-task"})

	// Verify notification is pushed
	select {
	case notif := <-ch:
		if notif.Method != "notifications/resources/updated" {
			t.Errorf("expected method notifications/resources/updated, got %s", notif.Method)
		}
		// Extract URI and event details
		params := notif.Params.AdditionalFields
		if params["uri"] != "cluster://snapshot" {
			t.Errorf("expected uri cluster://snapshot, got %v", params["uri"])
		}
		eventVal, ok := params["event"]
		if !ok {
			t.Fatal("expected event in additional fields")
		}
		evt, ok := eventVal.(map[string]any)
		if !ok {
			t.Fatal("expected event to be map[string]any")
		}
		if evt["name"] != "task.placement.requested" {
			t.Errorf("expected event name task.placement.requested, got %v", evt["name"])
		}
		if evt["id"] == nil || evt["id"] == "" {
			t.Error("expected non-empty event id")
		}
		if evt["sequence"] == nil {
			t.Error("expected event sequence")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for MCP notification")
	}
}
