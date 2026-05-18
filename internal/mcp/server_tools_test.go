package axismcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	mcpproto "github.com/mark3labs/mcp-go/mcp"
	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/state"
)

func TestNewServer(t *testing.T) {
	s := NewServer(false, "")
	if s == nil {
		t.Fatal("expected non-nil server")
	}

	sCached := NewServer(true, "http://localhost:8080")
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

	contents, err := clusterSnapshotResource(context.Background(), mcpproto.ReadResourceRequest{}, false, "")
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

	_, err := clusterSnapshotResource(context.Background(), mcpproto.ReadResourceRequest{}, false, "")
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

	result, err := axisHealthTool(context.Background(), toolRequest(nil), true, server.URL)
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

	result, err := axisHealthTool(context.Background(), toolRequest(nil), false, "")
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

	result, err := axisHealthTool(context.Background(), toolRequest(nil), false, "")
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
