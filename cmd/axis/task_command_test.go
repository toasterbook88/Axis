package main

import (
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

func TestTaskPlaceCmdHumanOutput(t *testing.T) {
	restoreState := stubPlacementState(t, &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil)
	defer restoreState()
	restoreLive := stubTaskLiveLoader(t, func(context.Context) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{
				nodeComplete("alpha", 6144, "low", "git"),
			},
		}, "live", nil
	})
	defer restoreLive()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := taskPlaceCmd()
		cmd.SetArgs([]string{"analyze a git repo"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("task place Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, "alpha") {
		t.Fatalf("expected selected node output, got %q", stdout)
	}
	if !strings.Contains(stdout, "Tool:") && !strings.Contains(stdout, "git") {
		t.Fatalf("expected tool output, got %q", stdout)
	}
	if !strings.Contains(stdout, "Reason") {
		t.Fatalf("expected reasoning output, got %q", stdout)
	}
}

func TestTaskPlaceCmdCachedJSONOutput(t *testing.T) {
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
		cmd := taskPlaceCmd()
		cmd.SetArgs([]string{"--cached", "--format", "json", "analyze a git repo"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("task place cached Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, `"source": "daemon-cache"`) {
		t.Fatalf("expected cached source, got %q", stdout)
	}
	if !strings.Contains(stdout, `"node": "cached-node"`) {
		t.Fatalf("expected cached node in JSON, got %q", stdout)
	}
}

func TestTaskContextCmdLiveOutput(t *testing.T) {
	restoreLive := stubTaskLiveLoader(t, func(context.Context) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{
				nodeComplete("alpha", 4096, "low", "git"),
			},
			Summary: models.ClusterSummary{
				TotalNodes:     1,
				TotalFreeRAMMB: 4096,
			},
		}, "live", nil
	})
	defer restoreLive()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := taskContextCmd()
		cmd.SetArgs([]string{"analyze a git repo"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("task context Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, "Source: live") {
		t.Fatalf("expected live source, got %q", stdout)
	}
	if !strings.Contains(stdout, "Best node: alpha") {
		t.Fatalf("expected best node, got %q", stdout)
	}
}

func TestTaskContextCmdCachedOutput(t *testing.T) {
	restoreFetch := stubTaskSnapshotFetcher(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{
				nodeComplete("cached-node", 4096, "none", "git"),
			},
			Summary: models.ClusterSummary{
				TotalNodes:         1,
				TotalAllocatableMB: 4096,
			},
		}, "daemon-cache", nil
	})
	defer restoreFetch()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := taskContextCmd()
		cmd.SetArgs([]string{"--cached", "analyze a git repo"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("task context cached Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, "Source: daemon-cache") {
		t.Fatalf("expected daemon-cache source, got %q", stdout)
	}
	if !strings.Contains(stdout, "Best node: cached-node") {
		t.Fatalf("expected cached node, got %q", stdout)
	}
}

func TestPrintContextBlockWritesOutput(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			nodeComplete("alpha", 4096, "none", "git"),
		},
		Summary: models.ClusterSummary{TotalNodes: 1, TotalFreeRAMMB: 4096},
	}

	stdout, stderr, err := captureProcessOutput(t, func() error {
		printContextBlock(snap, models.TaskRequirements{}, "analyze a git repo", "live", nil, nil)
		return nil
	})
	if err != nil {
		t.Fatalf("printContextBlock: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, "AXIS CLUSTER CONTEXT") {
		t.Fatalf("expected context header, got %q", stdout)
	}
}

func stubTaskSnapshotFetcher(t *testing.T, fn func(context.Context, string) (*models.ClusterSnapshot, string, error)) func() {
	t.Helper()
	prev := fetchTaskSnapshot
	fetchTaskSnapshot = fn
	return func() {
		fetchTaskSnapshot = prev
	}
}

func stubTaskLiveLoader(t *testing.T, fn func(context.Context) (*models.ClusterSnapshot, string, error)) func() {
	t.Helper()
	prev := loadTaskLiveSnapshot
	loadTaskLiveSnapshot = fn
	return func() {
		loadTaskLiveSnapshot = prev
	}
}
