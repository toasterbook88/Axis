package modelplan

import (
	"reflect"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestPlannerDeterministicAcrossInputOrder(t *testing.T) {
	now := time.Date(2026, 6, 19, 15, 0, 0, 0, time.UTC)
	planner := Planner{Now: func() time.Time { return now }}
	manifest := ModelManifest{
		Name:                 "test-4l",
		TotalLayers:          4,
		DefaultLayerMemoryMB: 600,
		PerNodeOverheadMB:    200,
	}
	nodeA := testNode("node-a", models.StatusComplete, 1500)
	nodeB := testNode("node-b", models.StatusComplete, 1500)
	nodeC := testNode("node-c", models.StatusComplete, 900)
	links := []LinkObservation{
		testLink(now, "node-a", "node-b", 1000, 1),
		testLink(now, "node-b", "node-a", 900, 1),
		testLink(now, "node-a", "node-c", 500, 2),
		testLink(now, "node-c", "node-a", 500, 2),
		testLink(now, "node-b", "node-c", 400, 3),
		testLink(now, "node-c", "node-b", 400, 3),
	}

	first, err := planner.Plan(testSnapshot(now, models.SnapshotHealthy, nodeC, nodeA, nodeB), nil, manifest, links, PlanOptions{MaxNodes: 3})
	if err != nil {
		t.Fatalf("first Plan() error = %v", err)
	}
	reversedLinks := append([]LinkObservation(nil), links...)
	for left, right := 0, len(reversedLinks)-1; left < right; left, right = left+1, right-1 {
		reversedLinks[left], reversedLinks[right] = reversedLinks[right], reversedLinks[left]
	}
	second, err := planner.Plan(testSnapshot(now, models.SnapshotHealthy, nodeB, nodeC, nodeA), nil, manifest, reversedLinks, PlanOptions{MaxNodes: 3})
	if err != nil {
		t.Fatalf("second Plan() error = %v", err)
	}

	if !reflect.DeepEqual(first.Shards, second.Shards) {
		t.Fatalf("shards differ by input order:\nfirst:  %+v\nsecond: %+v", first.Shards, second.Shards)
	}
	if !reflect.DeepEqual(first.Links, second.Links) {
		t.Fatalf("links differ by input order:\nfirst:  %+v\nsecond: %+v", first.Links, second.Links)
	}
}

func assertLayerCoverage(t *testing.T, plan DistributedPlacementPlan, totalLayers int) {
	t.Helper()
	cursor := 0
	for _, shard := range plan.Shards {
		if shard.StartLayer != cursor {
			t.Fatalf("shard %+v starts at %d, want %d", shard, shard.StartLayer, cursor)
		}
		if shard.EndLayerExclusive <= shard.StartLayer {
			t.Fatalf("shard %+v has empty or reversed range", shard)
		}
		cursor = shard.EndLayerExclusive
	}
	if cursor != totalLayers {
		t.Fatalf("layer coverage ends at %d, want %d", cursor, totalLayers)
	}
}

func testSnapshot(timestamp time.Time, status models.SnapshotStatus, nodes ...models.NodeFacts) *models.ClusterSnapshot {
	return &models.ClusterSnapshot{
		Timestamp: timestamp,
		Status:    status,
		Nodes:     append([]models.NodeFacts(nil), nodes...),
	}
}

func testNode(name string, status models.NodeStatus, allocatableMB int64) models.NodeFacts {
	return models.NodeFacts{
		Name:             name,
		Status:           status,
		RAMAllocatableMB: allocatableMB,
		Resources: &models.Resources{
			RAMTotalMB:       allocatableMB + 1024,
			RAMFreeMB:        allocatableMB + 1024,
			RAMAllocatableMB: allocatableMB,
			Pressure:         "none",
		},
	}
}

func testLink(measuredAt time.Time, source, destination string, bandwidth, latency float64) LinkObservation {
	return LinkObservation{
		SourceNode:      source,
		DestinationNode: destination,
		Interconnect:    "test-link",
		BandwidthMBps:   bandwidth,
		LatencyP95MS:    latency,
		Source:          "unit-test",
		MeasuredAt:      measuredAt,
	}
}
