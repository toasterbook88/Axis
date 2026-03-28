package axismcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	mcpproto "github.com/mark3labs/mcp-go/mcp"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/state"
)

func TestCurrentSnapshotUsesCacheWhenRequested(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/snapshot/meta":
			_ = json.NewEncoder(w).Encode(daemon.Metadata{
				Source:             "daemon-cache",
				Ready:              true,
				RefreshIntervalSec: 60,
			})
		case "/snapshot":
			_ = json.NewEncoder(w).Encode(models.ClusterSnapshot{
				Status: models.SnapshotHealthy,
				Summary: models.ClusterSummary{
					TotalNodes: 2,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	snap, err := currentSnapshot(context.Background(), true, server.URL)
	if err != nil {
		t.Fatalf("currentSnapshot: %v", err)
	}
	if snap.Summary.TotalNodes != 2 {
		t.Fatalf("expected cached snapshot total nodes 2, got %d", snap.Summary.TotalNodes)
	}
}

func TestCurrentSnapshotUsesLiveRuntimeWhenNotCached(t *testing.T) {
	restore := stubMCPRuntime(t, &runtimectx.Context{
		Snapshot: &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Summary: models.ClusterSummary{
				TotalNodes: 3,
			},
		},
	}, nil)
	defer restore()

	snap, err := currentSnapshot(context.Background(), false, "")
	if err != nil {
		t.Fatalf("currentSnapshot: %v", err)
	}
	if snap.Summary.TotalNodes != 3 {
		t.Fatalf("expected live snapshot total nodes 3, got %d", snap.Summary.TotalNodes)
	}
}

func TestPlacementDecisionToolUsesLiveRuntimeStateAndWarnings(t *testing.T) {
	restore := stubMCPRuntime(t, &runtimectx.Context{
		Snapshot: &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Nodes: []models.NodeFacts{
				mcpNode("alpha", "alpha.internal", 8192, 8192, "low", "git"),
				mcpNode("beta", "beta.internal", 6144, 6144, "low", "git"),
			},
			Warnings: []models.Warning{
				{Kind: "state", Message: "recovered local AXIS state"},
			},
		},
		State: &state.ClusterState{
			Nodes: map[string]state.NodeState{
				"alpha": {ReservedMB: 4096},
			},
		},
	}, nil)
	defer restore()

	result, err := placementDecisionTool(context.Background(), toolRequest(map[string]any{
		"description": "analyze a git repo",
	}), false, "")
	if err != nil {
		t.Fatalf("placementDecisionTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success result, got error: %s", toolResultText(t, result))
	}

	decision, ok := result.StructuredContent.(models.PlacementDecision)
	if !ok {
		t.Fatalf("expected placement decision structured content, got %#v", result.StructuredContent)
	}
	if decision.Node != "beta" {
		t.Fatalf("expected beta to win after state overlay, got %q", decision.Node)
	}
	if len(decision.Reasoning) == 0 || decision.Reasoning[0] != "warning: recovered local AXIS state" {
		t.Fatalf("expected warning reasoning prefix, got %#v", decision.Reasoning)
	}
}

func TestPlacementDecisionToolCachedPathAppendsRecoveredStateWarning(t *testing.T) {
	restoreFetch := stubCachedSnapshotFetcher(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Nodes: []models.NodeFacts{
				mcpNode("node-a", "node-a.internal", 8192, 4096, "low", "git"),
			},
		}, "daemon-cache", nil
	})
	defer restoreFetch()

	restoreState := stubMCPStateLoader(t, func() (*state.ClusterState, error) {
		return &state.ClusterState{Nodes: map[string]state.NodeState{}}, errors.New("recovered local AXIS state")
	})
	defer restoreState()

	result, err := placementDecisionTool(context.Background(), toolRequest(map[string]any{
		"description": "analyze a git repo",
	}), true, "ignored")
	if err != nil {
		t.Fatalf("placementDecisionTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success result, got error: %s", toolResultText(t, result))
	}

	decision, ok := result.StructuredContent.(models.PlacementDecision)
	if !ok {
		t.Fatalf("expected placement decision structured content, got %#v", result.StructuredContent)
	}
	if len(decision.Reasoning) == 0 || decision.Reasoning[0] != "warning: recovered local AXIS state" {
		t.Fatalf("expected warning reasoning prefix, got %#v", decision.Reasoning)
	}
}

func TestRunCommandReportsMissingExecutable(t *testing.T) {
	restoreLookPath := stubLookPath(t, func(string) (string, error) {
		return "", errors.New("missing executable")
	})
	defer restoreLookPath()

	result := runCommand(context.Background(), "tailscale", "status")
	if result.Available {
		t.Fatal("expected unavailable command")
	}
	if !strings.Contains(result.Error, "missing executable") {
		t.Fatalf("expected missing executable error, got %q", result.Error)
	}
}

func TestRunCommandReportsCommandFailure(t *testing.T) {
	restoreLookPath := stubLookPath(t, func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	})
	defer restoreLookPath()
	restoreExec := stubExecCommand(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "bash", "-lc", "printf boom && exit 5")
	})
	defer restoreExec()

	result := runCommand(context.Background(), "tailscale", "status")
	if !result.Available {
		t.Fatal("expected command to be available")
	}
	if result.Output != "boom" {
		t.Fatalf("expected output boom, got %q", result.Output)
	}
	if !strings.Contains(result.Error, "exit status 5") {
		t.Fatalf("expected exit status error, got %q", result.Error)
	}
}

func TestRunFirstAvailableCommandFallsBackToSecondCandidate(t *testing.T) {
	restoreLookPath := stubLookPath(t, func(name string) (string, error) {
		if name == "ip" {
			return "", errors.New("ip missing")
		}
		return "/sbin/" + name, nil
	})
	defer restoreLookPath()
	restoreExec := stubExecCommand(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "bash", "-lc", "printf ifconfig-ok")
	})
	defer restoreExec()

	result := runFirstAvailableCommand(context.Background(), []string{"ip", "addr"}, []string{"ifconfig"})
	if !result.Available {
		t.Fatal("expected fallback command to run")
	}
	if result.Command != "ifconfig" {
		t.Fatalf("expected fallback command ifconfig, got %q", result.Command)
	}
	if result.Output != "ifconfig-ok" {
		t.Fatalf("expected fallback output, got %q", result.Output)
	}
}

func TestSSHConnectivityTestToolReportsConfigError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	result, err := sshConnectivityTestTool(context.Background(), toolRequest(map[string]any{
		"node": "node-a",
	}))
	if err != nil {
		t.Fatalf("sshConnectivityTestTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected tool error result")
	}
	if !strings.Contains(toolResultText(t, result), "reading config") {
		t.Fatalf("expected config load error, got %q", toolResultText(t, result))
	}
}

func TestSSHConnectivityTestToolReportsMissingNode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeMCPConfig(t, home, "nodes:\n  - name: other\n    hostname: other.internal\n    ssh_user: me\n")

	result, err := sshConnectivityTestTool(context.Background(), toolRequest(map[string]any{
		"node": "node-a",
	}))
	if err != nil {
		t.Fatalf("sshConnectivityTestTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected tool error result")
	}
	if !strings.Contains(toolResultText(t, result), `node "node-a" not found`) {
		t.Fatalf("expected missing node error, got %q", toolResultText(t, result))
	}
}

func TestSSHConnectivityTestToolSuccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeMCPConfig(t, home, "nodes:\n  - name: node-a\n    hostname: node-a.internal\n    ssh_user: me\n")

	fakeExec := &fakeMCPRemoteExec{output: "axis-ok"}
	restoreRemote := stubMCPRemoteFactory(t, func(config.NodeConfig) mcpRemoteExecutor {
		return fakeExec
	})
	defer restoreRemote()

	result, err := sshConnectivityTestTool(context.Background(), toolRequest(map[string]any{
		"node": "node-a",
	}))
	if err != nil {
		t.Fatalf("sshConnectivityTestTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success result, got error: %s", toolResultText(t, result))
	}

	payload, ok := result.StructuredContent.(sshConnectivityResult)
	if !ok {
		t.Fatalf("expected ssh connectivity result, got %#v", result.StructuredContent)
	}
	if !payload.OK {
		t.Fatalf("expected connectivity success, got %#v", payload)
	}
	if payload.Node != "node-a" || payload.Host != "node-a.internal" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if !fakeExec.closed {
		t.Fatal("expected executor close")
	}
}

type fakeMCPRemoteExec struct {
	output string
	err    error
	closed bool
}

func (f *fakeMCPRemoteExec) Run(context.Context, string) (string, error) {
	return f.output, f.err
}

func (f *fakeMCPRemoteExec) Close() error {
	f.closed = true
	return nil
}

func stubMCPRuntime(t *testing.T, rt *runtimectx.Context, err error) func() {
	t.Helper()
	prev := loadMCPRuntime
	loadMCPRuntime = func(context.Context) (*runtimectx.Context, error) {
		return rt, err
	}
	return func() {
		loadMCPRuntime = prev
	}
}

func stubMCPStateLoader(t *testing.T, fn func() (*state.ClusterState, error)) func() {
	t.Helper()
	prev := loadMCPState
	loadMCPState = fn
	return func() {
		loadMCPState = prev
	}
}

func stubCachedSnapshotFetcher(t *testing.T, fn func(context.Context, string) (*models.ClusterSnapshot, string, error)) func() {
	t.Helper()
	prev := fetchCachedSnapshot
	fetchCachedSnapshot = fn
	return func() {
		fetchCachedSnapshot = prev
	}
}

func stubLookPath(t *testing.T, fn func(string) (string, error)) func() {
	t.Helper()
	prev := lookPath
	lookPath = fn
	return func() {
		lookPath = prev
	}
}

func stubExecCommand(t *testing.T, fn func(context.Context, string, ...string) *exec.Cmd) func() {
	t.Helper()
	prev := execCommand
	execCommand = fn
	return func() {
		execCommand = prev
	}
}

func stubMCPRemoteFactory(t *testing.T, fn func(config.NodeConfig) mcpRemoteExecutor) func() {
	t.Helper()
	prev := newMCPRemoteExecutor
	newMCPRemoteExecutor = fn
	return func() {
		newMCPRemoteExecutor = prev
	}
}

func toolRequest(args map[string]any) mcpproto.CallToolRequest {
	return mcpproto.CallToolRequest{
		Params: mcpproto.CallToolParams{
			Arguments: args,
		},
	}
}

func toolResultText(t *testing.T, result *mcpproto.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		return ""
	}
	text, ok := mcpproto.AsTextContent(result.Content[0])
	if !ok {
		t.Fatalf("expected text content, got %#v", result.Content[0])
	}
	return text.Text
}

func mcpNode(name, hostname string, totalRAM, freeRAM int64, pressure string, tools ...string) models.NodeFacts {
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

func writeMCPConfig(t *testing.T, home string, content string) {
	t.Helper()
	cfgPath := filepath.Join(home, ".axis", "nodes.yaml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
