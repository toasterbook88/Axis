package main

import (
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

func TestTaskPlaceUsesCacheWhenAvailable(t *testing.T) {
	restore := stubPlacementState(t, &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil)
	defer restore()

	read, err := loadCommandSnapshot(
		context.Background(),
		false,
		false,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return &models.ClusterSnapshot{
				Nodes: []models.NodeFacts{
					nodeComplete("cached-node", 4096, "low", "git"),
				},
			}, "daemon-cache", nil
		},
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			t.Fatal("expected fresh cache to avoid live collection")
			return nil, "", nil
		},
	)
	if err != nil {
		t.Fatalf("loadCommandSnapshot: %v", err)
	}
	if read.source != "daemon-cache" {
		t.Fatalf("expected daemon-cache source, got %q", read.source)
	}
	explanation, _, _, err := explainPlacementFromSnapshot(context.Background(), "analyze a git repo", read.snap, read.source, read.age)
	if err != nil {
		t.Fatalf("explainPlacementFromSnapshot: %v", err)
	}
	decision := explanation.Decision
	if decision.Node != "cached-node" {
		t.Fatalf("expected cached-node, got %q", decision.Node)
	}
}

func TestTaskPlaceFallsBackToLiveWhenCacheFails(t *testing.T) {
	restore := stubPlacementState(t, &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil)
	defer restore()

	read, err := loadCommandSnapshot(
		context.Background(),
		false,
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
		t.Fatalf("loadCommandSnapshot: %v", err)
	}
	if read.source != "live-fallback" {
		t.Fatalf("expected live-fallback source, got %q", read.source)
	}
	explanation, _, _, err := explainPlacementFromSnapshot(context.Background(), "analyze a git repo", read.snap, read.source, read.age)
	if err != nil {
		t.Fatalf("explainPlacementFromSnapshot: %v", err)
	}
	decision := explanation.Decision
	if decision.Node != "live-node" {
		t.Fatalf("expected live-node, got %q", decision.Node)
	}
	if joined := strings.Join(decision.Reasoning, "\n"); !strings.Contains(joined, "using live snapshot (daemon cache unavailable)") {
		t.Fatalf("expected cache fallback reasoning, got %q", joined)
	}
}

func TestTaskPlaceCachedOnlyFailsWhenCacheFails(t *testing.T) {
	restore := stubPlacementState(t, &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil)
	defer restore()

	read, err := loadCommandSnapshot(
		context.Background(),
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
	if read.snap != nil || read.source != "" || read.age != "" {
		t.Fatalf("expected empty snapshot on cached-only failure, got %#v", read)
	}
	if got := err.Error(); got != "daemon cache unavailable: context deadline exceeded" {
		t.Fatalf("unexpected cached-only error: %q", got)
	}
}

func TestTaskPlaceUsesReservationOverlayFromLiveSnapshot(t *testing.T) {
	restore := stubPlacementState(t, &state.ClusterState{Nodes: map[string]state.NodeState{}}, nil)
	defer restore()

	alpha := nodeComplete("alpha", 8192, "low", "git")
	alpha.RAMReservedMB = 4096
	alpha.RAMAllocatableMB = 4096

	beta := nodeComplete("beta", 6144, "low", "git")
	beta.RAMReservedMB = 0
	beta.RAMAllocatableMB = 6144

	explanation, source, _, err := explainPlacementFromSnapshot(
		context.Background(),
		"analyze a git repo",
		&models.ClusterSnapshot{Nodes: []models.NodeFacts{alpha, beta}},
		"live",
		"",
	)
	if err != nil {
		t.Fatalf("explainPlacementFromSnapshot: %v", err)
	}
	if source != "live" {
		t.Fatalf("expected live source, got %q", source)
	}
	decision := explanation.Decision
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
