package main

import (
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

func TestPlanTaskPlacementPrefersCacheWhenAvailable(t *testing.T) {
	restore := stubPlacementState(t, &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil)
	defer restore()

	decision, source, err := planTaskPlacement(
		context.Background(),
		"analyze a git repo",
		true,
		false,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return &models.ClusterSnapshot{
				Nodes: []models.NodeFacts{
					nodeComplete("cached-node", 4096, "low", "git"),
				},
			}, "daemon-cache", nil
		},
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return &models.ClusterSnapshot{
				Nodes: []models.NodeFacts{
					nodeComplete("live-node", 8192, "low", "git"),
				},
			}, "live", nil
		},
	)
	if err != nil {
		t.Fatalf("planTaskPlacement: %v", err)
	}
	if source != "daemon-cache" {
		t.Fatalf("expected daemon-cache source, got %q", source)
	}
	if decision.Node != "cached-node" {
		t.Fatalf("expected cached-node, got %q", decision.Node)
	}
}

func TestPlanTaskPlacementFallsBackToLiveWhenCacheFails(t *testing.T) {
	restore := stubPlacementState(t, &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil)
	defer restore()

	decision, source, err := planTaskPlacement(
		context.Background(),
		"analyze a git repo",
		true,
		false,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return nil, "", context.DeadlineExceeded
		},
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return &models.ClusterSnapshot{
				Nodes: []models.NodeFacts{
					nodeComplete("live-node", 8192, "low", "git"),
				},
			}, "live", nil
		},
	)
	if err != nil {
		t.Fatalf("planTaskPlacement: %v", err)
	}
	if source != "live-fallback" {
		t.Fatalf("expected live-fallback source, got %q", source)
	}
	if decision.Node != "live-node" {
		t.Fatalf("expected live-node, got %q", decision.Node)
	}
	if joined := strings.Join(decision.Reasoning, "\n"); !strings.Contains(joined, "daemon cache unavailable; fell back to live snapshot: context deadline exceeded") {
		t.Fatalf("expected cache fallback reasoning, got %q", joined)
	}
}

func TestPlanTaskPlacementCachedOnlyFailsWhenCacheFails(t *testing.T) {
	restore := stubPlacementState(t, &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil)
	defer restore()

	decision, source, err := planTaskPlacement(
		context.Background(),
		"analyze a git repo",
		false,
		true,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return nil, "", context.DeadlineExceeded
		},
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			t.Fatal("expected no live fallback in cached-only mode")
			return nil, "", nil
		},
	)
	if err == nil {
		t.Fatal("expected cached-only cache failure")
	}
	if source != "" {
		t.Fatalf("expected empty source on cached-only failure, got %q", source)
	}
	if decision.OK || decision.Node != "" || decision.FitScore != 0 || len(decision.Reasoning) != 0 {
		t.Fatalf("expected empty decision on cached-only failure, got %#v", decision)
	}
	if got := err.Error(); got != "daemon cache unavailable: context deadline exceeded" {
		t.Fatalf("unexpected cached-only error: %q", got)
	}
}

func TestPlanTaskPlacementUsesReservationOverlayFromLiveSnapshot(t *testing.T) {
	restore := stubPlacementState(t, &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil)
	defer restore()

	decision, source, err := planTaskPlacement(
		context.Background(),
		"analyze a git repo",
		false,
		false,
		nil,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			alpha := nodeComplete("alpha", 8192, "low", "git")
			alpha.Resources.RAMReservedMB = 4096
			alpha.Resources.RAMAllocatableMB = 4096

			beta := nodeComplete("beta", 6144, "low", "git")
			beta.Resources.RAMReservedMB = 0
			beta.Resources.RAMAllocatableMB = 6144

			return &models.ClusterSnapshot{
				Nodes: []models.NodeFacts{alpha, beta},
			}, "live", nil
		},
	)
	if err != nil {
		t.Fatalf("planTaskPlacement: %v", err)
	}
	if source != "live" {
		t.Fatalf("expected live source, got %q", source)
	}
	if decision.Node != "beta" {
		t.Fatalf("expected beta after reservation overlay, got %q", decision.Node)
	}
}

func nodeComplete(name string, freeRAM int64, pressure string, tools ...string) models.NodeFacts {
	node := models.NodeFacts{
		Name:   name,
		Status: models.StatusComplete,
		Resources: &models.Resources{
			RAMFreeMB:  freeRAM,
			RAMTotalMB: 8192,
			Pressure:   pressure,
			CPUCores:   8,
		},
	}
	for _, tool := range tools {
		node.Tools = append(node.Tools, models.ToolInfo{Name: tool, Version: "test"})
	}
	return node
}

func stubPlacementState(t *testing.T, st *state.ClusterState, err error) func() {
	t.Helper()
	prev := loadPlacementState
	loadPlacementState = func() (*state.ClusterState, error) {
		return st, err
	}
	return func() {
		loadPlacementState = prev
	}
}
