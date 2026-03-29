package axismcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/state"
)

var mcpTurboTimePattern = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z`)

func TestClusterSnapshotToolTurboQuantGolden(t *testing.T) {
	restore := stubMCPRuntime(t, &runtimectx.Context{
		Snapshot: &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Nodes: []models.NodeFacts{
				mcpTurboNode("mlx-node", "mlx.internal", true),
			},
			Summary: models.ClusterSummary{
				TotalNodes:         1,
				ReachableNodes:     1,
				TotalRAMMB:         16384,
				TotalFreeRAMMB:     8192,
				TotalAllocatableMB: 8192,
			},
		},
		State: &state.ClusterState{Nodes: map[string]state.NodeState{}},
	}, nil)
	defer restore()

	result, err := clusterSnapshotTool(context.Background(), toolRequest(nil), false, "")
	if err != nil {
		t.Fatalf("clusterSnapshotTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success result, got error: %s", toolResultText(t, result))
	}

	data, err := json.MarshalIndent(result.StructuredContent, "", "  ")
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}

	assertMCPTurboGoldenText(t,
		filepath.Join("testdata", "cluster_snapshot_turboquant.golden"),
		normalizeMCPTurboOutput(string(data)),
	)
}

func TestPlacementDecisionToolTurboQuantGolden(t *testing.T) {
	restore := stubMCPRuntime(t, &runtimectx.Context{
		Snapshot: &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Nodes: []models.NodeFacts{
				mcpTurboNode("mlx-node", "mlx.internal", true),
				mcpNode("plain-node", "plain.internal", 16384, 8192, "none"),
			},
		},
		State: &state.ClusterState{Nodes: map[string]state.NodeState{}},
	}, nil)
	defer restore()

	result, err := placementDecisionTool(context.Background(), toolRequest(map[string]any{
		"description": "book-length model analysis 128k",
	}), false, "")
	if err != nil {
		t.Fatalf("placementDecisionTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success result, got error: %s", toolResultText(t, result))
	}

	data, err := json.MarshalIndent(result.StructuredContent, "", "  ")
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}

	assertMCPTurboGoldenText(t,
		filepath.Join("testdata", "placement_decision_turboquant.golden"),
		normalizeMCPTurboOutput(string(data)),
	)
}

func mcpTurboNode(name, hostname string, verified bool) models.NodeFacts {
	node := mcpNode(name, hostname, 16384, 8192, "none")
	node.Tools = append(node.Tools, models.ToolInfo{Name: "llama-server", Version: "b123"})
	node.TurboQuant = &models.TurboQuantInfo{
		Supported:    true,
		Verified:     verified,
		Backends:     []string{"llama.cpp"},
		Capabilities: []string{"backend-probed", "ctx-size-flag", "flash-attn-flag", "llama.cpp-runtime"},
	}
	return node
}

func normalizeMCPTurboOutput(s string) string {
	s = mcpTurboTimePattern.ReplaceAllString(s, "<TIME>")
	return strings.TrimSpace(s) + "\n"
}

func assertMCPTurboGoldenText(t *testing.T, path string, actual string) {
	t.Helper()
	expectedBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if actual != string(expectedBytes) {
		t.Fatalf("golden mismatch for %s\nEXPECTED:\n%s\nACTUAL:\n%s", path, string(expectedBytes), actual)
	}
}
