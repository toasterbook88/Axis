package modelplan

import (
	"reflect"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestPlannerSingleNode(t *testing.T) {
	now := time.Date(2026, 6, 19, 15, 0, 0, 0, time.UTC)
	planner := Planner{Now: func() time.Time { return now }}
	snapshot := testSnapshot(now, models.SnapshotHealthy,
		testNode("node-a", models.StatusComplete, 4096),
	)
	manifest := ModelManifest{
		Name:                 "test-4l",
		TotalLayers:          4,
		DefaultLayerMemoryMB: 500,
		PerNodeOverheadMB:    256,
	}

	plan, err := planner.Plan(snapshot, nil, manifest, nil, PlanOptions{})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if plan.Status != PlanComplete {
		t.Fatalf("status = %q, want %q", plan.Status, PlanComplete)
	}
	if plan.Strategy != StrategySingleNode {
		t.Fatalf("strategy = %q, want %q", plan.Strategy, StrategySingleNode)
	}
	if plan.CoordinatorNode != "node-a" {
		t.Fatalf("coordinator = %q, want node-a", plan.CoordinatorNode)
	}
	if len(plan.Shards) != 1 {
		t.Fatalf("len(shards) = %d, want 1", len(plan.Shards))
	}
	assertLayerCoverage(t, plan, 4)
	shard := plan.Shards[0]
	if shard.RequiredMemoryMB != 2256 {
		t.Fatalf("required memory = %dMB, want 2256MB", shard.RequiredMemoryMB)
	}
	if plan.EstimatedTotalMemoryMB != 2256 {
		t.Fatalf("estimated total = %dMB, want 2256MB", plan.EstimatedTotalMemoryMB)
	}
	if len(plan.Links) != 0 {
		t.Fatalf("single-node plan links = %d, want 0", len(plan.Links))
	}
}

func TestPlannerPipelineUsesBestFreshDirectionalPath(t *testing.T) {
	now := time.Date(2026, 6, 19, 15, 0, 0, 0, time.UTC)
	planner := Planner{Now: func() time.Time { return now }}
	snapshot := testSnapshot(now, models.SnapshotHealthy,
		testNode("node-b", models.StatusComplete, 1500),
		testNode("node-a", models.StatusComplete, 1500),
	)
	manifest := ModelManifest{
		Name:                 "test-4l",
		TotalLayers:          4,
		DefaultLayerMemoryMB: 600,
		PerNodeOverheadMB:    200,
	}
	links := []LinkObservation{
		testLink(now, "node-b", "node-a", 100, 2),
		testLink(now, "node-a", "node-b", 1000, 1),
	}

	plan, err := planner.Plan(snapshot, nil, manifest, links, PlanOptions{MaxNodes: 2})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if plan.Strategy != StrategyPipeline {
		t.Fatalf("strategy = %q, want %q", plan.Strategy, StrategyPipeline)
	}
	if len(plan.Shards) != 2 {
		t.Fatalf("len(shards) = %d, want 2", len(plan.Shards))
	}
	assertLayerCoverage(t, plan, 4)
	if plan.Shards[0].Node != "node-a" || plan.Shards[1].Node != "node-b" {
		t.Fatalf("node order = %s -> %s, want node-a -> node-b", plan.Shards[0].Node, plan.Shards[1].Node)
	}
	if len(plan.Links) != 1 {
		t.Fatalf("len(links) = %d, want 1", len(plan.Links))
	}
	if plan.Links[0].BandwidthMBps != 1000 || plan.Links[0].LatencyP95MS != 1 {
		t.Fatalf("selected link = %+v, want 1000MB/s at 1ms", plan.Links[0])
	}
	for _, shard := range plan.Shards {
		if shard.RequiredMemoryMB > shard.AllocatableMB {
			t.Fatalf("shard %+v exceeds allocatable memory", shard)
		}
	}
}

func TestNormalizeManifestAddsKVCachePerLayer(t *testing.T) {
	manifest, layers, total, minimum, err := normalizeManifest(ModelManifest{
		Name:                         "kv-test",
		TotalLayers:                  2,
		DefaultLayerMemoryMB:         100,
		KVCacheBytesPerTokenPerLayer: 1024,
		ContextWindowTokens:          1024,
		Concurrency:                  2,
	})
	if err != nil {
		t.Fatalf("normalizeManifest() error = %v", err)
	}
	want := []int64{102, 102}
	if !reflect.DeepEqual(layers, want) {
		t.Fatalf("layers = %v, want %v", layers, want)
	}
	if !reflect.DeepEqual(manifest.LayerMemoryMB, want) {
		t.Fatalf("normalized manifest layers = %v, want %v", manifest.LayerMemoryMB, want)
	}
	if total != 204 || minimum != 102 {
		t.Fatalf("total/minimum = %d/%d, want 204/102", total, minimum)
	}
}
