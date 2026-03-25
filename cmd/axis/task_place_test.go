package main

import (
	"context"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestPlanTaskPlacementPrefersCacheWhenAvailable(t *testing.T) {
	decision, source, err := planTaskPlacement(
		context.Background(),
		"analyze a git repo",
		true,
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
	decision, source, err := planTaskPlacement(
		context.Background(),
		"analyze a git repo",
		true,
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
	if source != "live" {
		t.Fatalf("expected live source, got %q", source)
	}
	if decision.Node != "live-node" {
		t.Fatalf("expected live-node, got %q", decision.Node)
	}
}

func TestPlanTaskPlacementUsesReservationOverlayFromLiveSnapshot(t *testing.T) {
	decision, source, err := planTaskPlacement(
		context.Background(),
		"analyze a git repo",
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
