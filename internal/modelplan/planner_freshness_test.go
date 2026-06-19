package modelplan

import (
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestPlannerRejectsStaleSnapshot(t *testing.T) {
	now := time.Date(2026, 6, 19, 15, 0, 0, 0, time.UTC)
	planner := Planner{
		Now:            func() time.Time { return now },
		MaxSnapshotAge: time.Minute,
	}
	snapshot := testSnapshot(now.Add(-2*time.Minute), models.SnapshotHealthy,
		testNode("node-a", models.StatusComplete, 4096),
	)

	_, err := planner.Plan(snapshot, nil, ModelManifest{
		Name:                 "test",
		TotalLayers:          2,
		DefaultLayerMemoryMB: 100,
	}, nil, PlanOptions{})
	if err == nil || !strings.Contains(err.Error(), "snapshot is stale") {
		t.Fatalf("Plan() error = %v, want stale snapshot error", err)
	}
}

func TestPlannerRejectsMissingOrStaleTopologyForMultiNodePlan(t *testing.T) {
	now := time.Date(2026, 6, 19, 15, 0, 0, 0, time.UTC)
	planner := Planner{
		Now:        func() time.Time { return now },
		MaxLinkAge: time.Minute,
	}
	snapshot := testSnapshot(now, models.SnapshotHealthy,
		testNode("node-a", models.StatusComplete, 1500),
		testNode("node-b", models.StatusComplete, 1500),
	)
	manifest := ModelManifest{
		Name:                 "test-4l",
		TotalLayers:          4,
		DefaultLayerMemoryMB: 600,
		PerNodeOverheadMB:    200,
	}
	staleLinks := []LinkObservation{
		testLink(now.Add(-2*time.Minute), "node-a", "node-b", 1000, 1),
		testLink(now.Add(-2*time.Minute), "node-b", "node-a", 1000, 1),
	}

	_, err := planner.Plan(snapshot, nil, manifest, staleLinks, PlanOptions{MaxNodes: 2})
	if err == nil || !strings.Contains(err.Error(), "fresh directional topology") {
		t.Fatalf("Plan() error = %v, want topology error", err)
	}
}

func TestPlannerDegradedSnapshotIsPartialAndExcludesIncompleteNode(t *testing.T) {
	now := time.Date(2026, 6, 19, 15, 0, 0, 0, time.UTC)
	planner := Planner{Now: func() time.Time { return now }}
	snapshot := testSnapshot(now, models.SnapshotDegraded,
		testNode("node-a", models.StatusComplete, 4096),
		testNode("node-b", models.StatusPartial, 4096),
	)
	manifest := ModelManifest{
		Name:                 "test",
		TotalLayers:          2,
		DefaultLayerMemoryMB: 500,
	}

	plan, err := planner.Plan(snapshot, nil, manifest, nil, PlanOptions{})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if plan.Status != PlanPartial {
		t.Fatalf("status = %q, want %q", plan.Status, PlanPartial)
	}
	if len(plan.Excluded) != 1 || plan.Excluded[0].Node != "node-b" {
		t.Fatalf("excluded = %+v, want node-b", plan.Excluded)
	}
	if len(plan.Warnings) == 0 {
		t.Fatal("partial plan has no warning")
	}
}
