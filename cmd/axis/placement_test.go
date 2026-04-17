package main

import (
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

func TestPlacementExplainCmdHumanOutput(t *testing.T) {
	restoreState := stubPlacementState(t, &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil)
	defer restoreState()
	restoreLive := stubTaskLiveLoader(t, func(context.Context) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{
				nodeComplete("alpha", 6144, "low", "git"),
				nodeComplete("beta", 6144, "low"),
			},
		}, "live", nil
	})
	defer restoreLive()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := placementCmd()
		cmd.SetArgs([]string{"explain", "analyze a git repo"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("placement explain Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, "Advisory Placement") || !strings.Contains(stdout, "alpha") {
		t.Fatalf("expected placement ranking output, got %q", stdout)
	}
	if !strings.Contains(stdout, "Filtered") || !strings.Contains(stdout, "beta") {
		t.Fatalf("expected filtered node output, got %q", stdout)
	}
}

func TestPlacementExplainCmdCachedJSONOutput(t *testing.T) {
	restoreState := stubPlacementState(t, &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil)
	defer restoreState()
	restoreFetch := stubTaskSnapshotFetcher(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{
				nodeComplete("cached-node", 6144, "none", "git"),
			},
		}, "daemon-cache", nil
	})
	defer restoreFetch()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := placementCmd()
		cmd.SetArgs([]string{"explain", "--cached", "--format", "json", "analyze a git repo"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("placement explain cached Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, `"source": "daemon-cache"`) {
		t.Fatalf("expected cached source, got %q", stdout)
	}
	if !strings.Contains(stdout, `"decision":`) || !strings.Contains(stdout, `"node": "cached-node"`) {
		t.Fatalf("expected explanation JSON payload, got %q", stdout)
	}
}

func TestTaskPlaceOutputDiffersFromPlacementExplain(t *testing.T) {
	restoreState := stubPlacementState(t, &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil)
	defer restoreState()
	restoreFetch := stubTaskSnapshotFetcher(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{
				nodeComplete("cached-node", 6144, "none", "git"),
			},
		}, "daemon-cache", nil
	})
	defer restoreFetch()

	taskOut, taskErr, err := captureProcessOutput(t, func() error {
		cmd := taskPlaceCmd()
		cmd.SetArgs([]string{"--cached", "--format", "json", "analyze a git repo"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("task place Execute: %v", err)
	}
	if taskErr != "" {
		t.Fatalf("expected no task stderr, got %q", taskErr)
	}

	placementOut, placementErr, err := captureProcessOutput(t, func() error {
		cmd := placementCmd()
		cmd.SetArgs([]string{"explain", "--cached", "--format", "json", "analyze a git repo"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("placement explain Execute: %v", err)
	}
	if placementErr != "" {
		t.Fatalf("expected no placement stderr, got %q", placementErr)
	}

	// task place produces flat {"source":..., "decision":...} JSON;
	// placement explain wraps in {"source":..., "explanation":{"decision":..., "eligible":..., "excluded":...}}.
	if !strings.Contains(taskOut, `"decision":`) {
		t.Fatalf("expected task place to contain 'decision' key, got %q", taskOut)
	}
	if strings.Contains(taskOut, `"explanation":`) {
		t.Fatalf("task place should not contain 'explanation' envelope; got %q", taskOut)
	}
	if !strings.Contains(placementOut, `"explanation":`) {
		t.Fatalf("expected placement explain to contain 'explanation' key, got %q", placementOut)
	}
	if strings.Contains(placementOut, `"eligible":`) && !strings.Contains(taskOut, `"eligible":`) {
		// placement explain includes eligible/excluded; task place does not — this is correct.
	}
}
