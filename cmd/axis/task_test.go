package main

import (
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestBuildContextBlockPrefersNodeWithResources(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name:   "m1",
				Status: models.StatusUnreachable,
			},
			{
				Name: "m3",
				Tools: []models.ToolInfo{
					{Name: "git"},
				},
				Status: models.StatusComplete,
				Resources: &models.Resources{
					RAMFreeMB: 833,
					Pressure:  "low",
				},
			},
		},
		Summary: models.ClusterSummary{
			TotalNodes:     2,
			TotalFreeRAMMB: 833,
		},
	}

	out := buildContextBlock(snap, models.TaskRequirements{MinFreeRAMMB: 4096}, "analyze repo")

	if !strings.Contains(out, "Best node: m3") {
		t.Fatalf("expected context block to choose node with resources, got:\n%s", out)
	}
	if !strings.Contains(out, "axis mcp serve") {
		t.Fatalf("expected MCP hint in context block, got:\n%s", out)
	}
}
