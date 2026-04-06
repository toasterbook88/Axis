package snapshot

import (
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func ts() time.Time {
	return time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
}

func completeNode(name string, totalRAM, freeRAM int64, pressure string) models.NodeFacts {
	return models.NodeFacts{
		Name:   name,
		Status: models.StatusComplete,
		Resources: &models.Resources{
			CPUCores:   8,
			RAMTotalMB: totalRAM,
			RAMFreeMB:  freeRAM,
			Pressure:   pressure,
		},
		CollectedAt: ts(),
	}
}

// --- Healthy scenarios ---

func TestBuild_AllComplete_Healthy(t *testing.T) {
	nodes := []models.NodeFacts{
		completeNode("m3", 8192, 4000, "none"),
		completeNode("m1", 8192, 5000, "none"),
	}
	snap := Build(nodes)

	if snap.Status != models.SnapshotHealthy {
		t.Errorf("expected healthy, got %q", snap.Status)
	}
	if snap.Summary.TotalNodes != 2 {
		t.Errorf("total_nodes: got %d, want 2", snap.Summary.TotalNodes)
	}
	if snap.Summary.ReachableNodes != 2 {
		t.Errorf("reachable: got %d, want 2", snap.Summary.ReachableNodes)
	}
	if snap.Summary.TotalRAMMB != 16384 {
		t.Errorf("total_ram: got %d, want 16384", snap.Summary.TotalRAMMB)
	}
	if snap.Summary.TotalFreeRAMMB != 9000 {
		t.Errorf("free_ram: got %d, want 9000", snap.Summary.TotalFreeRAMMB)
	}
	if snap.Summary.TotalAllocatableMB != 9000 {
		t.Errorf("allocatable_ram: got %d, want 9000", snap.Summary.TotalAllocatableMB)
	}
	if snap.Summary.TotalReservedMB != 0 {
		t.Errorf("reserved_ram: got %d, want 0", snap.Summary.TotalReservedMB)
	}
	if len(snap.Warnings) != 0 {
		t.Errorf("expected 0 warnings, got %d", len(snap.Warnings))
	}
}

func TestBuild_SingleNode_Healthy(t *testing.T) {
	nodes := []models.NodeFacts{
		completeNode("solo", 16384, 10000, "none"),
	}
	snap := Build(nodes)

	if snap.Status != models.SnapshotHealthy {
		t.Errorf("expected healthy, got %q", snap.Status)
	}
	if snap.Summary.TotalNodes != 1 {
		t.Errorf("total_nodes: got %d, want 1", snap.Summary.TotalNodes)
	}
	if snap.Summary.TotalRAMMB != 16384 {
		t.Errorf("total_ram: got %d, want 16384", snap.Summary.TotalRAMMB)
	}
}

func TestBuild_AllocatableRespectsSystemReserveFloor(t *testing.T) {
	nodes := []models.NodeFacts{
		completeNode("solo", 8192, 7900, "none"),
	}
	snap := Build(nodes)

	if snap.Summary.TotalAllocatableMB != 7168 {
		t.Fatalf("allocatable_ram: got %d, want 7168", snap.Summary.TotalAllocatableMB)
	}
}

// --- Degraded scenarios ---

func TestBuild_UnreachableNode_Degraded(t *testing.T) {
	nodes := []models.NodeFacts{
		completeNode("m3", 8192, 4000, "none"),
		{
			Name:        "m1",
			Status:      models.StatusUnreachable,
			Error:       "ssh dial timeout",
			CollectedAt: ts(),
		},
	}
	snap := Build(nodes)

	if snap.Status != models.SnapshotDegraded {
		t.Errorf("expected degraded, got %q", snap.Status)
	}
	if snap.Summary.ReachableNodes != 1 {
		t.Errorf("reachable: got %d, want 1", snap.Summary.ReachableNodes)
	}
	// RAM should only count the reachable node
	if snap.Summary.TotalRAMMB != 8192 {
		t.Errorf("total_ram: got %d, want 8192 (only m3)", snap.Summary.TotalRAMMB)
	}
	// Should have an unreachable warning
	if len(snap.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(snap.Warnings))
	}
	if snap.Warnings[0].Kind != "unreachable" {
		t.Errorf("warning kind: got %q, want unreachable", snap.Warnings[0].Kind)
	}
	if snap.Warnings[0].Node != "m1" {
		t.Errorf("warning node: got %q, want m1", snap.Warnings[0].Node)
	}
}

func TestBuild_PartialNode_Degraded(t *testing.T) {
	nodes := []models.NodeFacts{
		completeNode("m3", 8192, 4000, "none"),
		{
			Name:   "m1",
			Status: models.StatusPartial,
			Resources: &models.Resources{
				RAMTotalMB: 8192,
				RAMFreeMB:  3000,
			},
			CollectedAt: ts(),
		},
	}
	snap := Build(nodes)

	if snap.Status != models.SnapshotDegraded {
		t.Errorf("expected degraded, got %q", snap.Status)
	}
	// Partial nodes are reachable and contribute RAM
	if snap.Summary.ReachableNodes != 2 {
		t.Errorf("reachable: got %d, want 2", snap.Summary.ReachableNodes)
	}
	if snap.Summary.TotalRAMMB != 16384 {
		t.Errorf("total_ram: got %d, want 16384", snap.Summary.TotalRAMMB)
	}
	// Should have a partial warning
	found := false
	for _, w := range snap.Warnings {
		if w.Kind == "partial" && w.Node == "m1" {
			found = true
		}
	}
	if !found {
		t.Error("expected partial warning for m1")
	}
}

func TestBuild_ErrorNode_Degraded(t *testing.T) {
	nodes := []models.NodeFacts{
		{
			Name:        "broken",
			Status:      models.StatusError,
			Error:       "collector panic",
			CollectedAt: ts(),
		},
	}
	snap := Build(nodes)

	if snap.Status != models.SnapshotDegraded {
		t.Errorf("expected degraded, got %q", snap.Status)
	}
	if snap.Summary.ReachableNodes != 0 {
		t.Errorf("reachable: got %d, want 0", snap.Summary.ReachableNodes)
	}
	if len(snap.Warnings) != 1 || snap.Warnings[0].Kind != "error" {
		t.Errorf("expected error warning, got %v", snap.Warnings)
	}
}

func TestBuild_AllUnreachable_Degraded(t *testing.T) {
	nodes := []models.NodeFacts{
		{Name: "a", Status: models.StatusUnreachable, Error: "timeout", CollectedAt: ts()},
		{Name: "b", Status: models.StatusUnreachable, Error: "refused", CollectedAt: ts()},
	}
	snap := Build(nodes)

	if snap.Status != models.SnapshotDegraded {
		t.Errorf("expected degraded, got %q", snap.Status)
	}
	if snap.Summary.TotalNodes != 2 {
		t.Errorf("total: got %d, want 2", snap.Summary.TotalNodes)
	}
	if snap.Summary.ReachableNodes != 0 {
		t.Errorf("reachable: got %d, want 0", snap.Summary.ReachableNodes)
	}
	if snap.Summary.TotalRAMMB != 0 {
		t.Errorf("total_ram: got %d, want 0", snap.Summary.TotalRAMMB)
	}
	if len(snap.Warnings) != 2 {
		t.Errorf("expected 2 warnings, got %d", len(snap.Warnings))
	}
}

// --- RAM pressure warnings ---

func TestBuild_RAMPressureWarning(t *testing.T) {
	// 5% free → should trigger ram_pressure warning (threshold: <10%)
	nodes := []models.NodeFacts{
		completeNode("stressed", 8192, 400, "high"),
	}
	snap := Build(nodes)

	// Should be healthy (node is complete) but have a ram_pressure warning
	if snap.Status != models.SnapshotHealthy {
		t.Errorf("expected healthy, got %q", snap.Status)
	}
	found := false
	for _, w := range snap.Warnings {
		if w.Kind == "ram_pressure" && w.Node == "stressed" {
			found = true
		}
	}
	if !found {
		t.Error("expected ram_pressure warning for stressed node")
	}
}

func TestBuild_NoRAMPressureWhenAboveThreshold(t *testing.T) {
	// 25% free → should NOT trigger ram_pressure warning
	nodes := []models.NodeFacts{
		completeNode("healthy", 8192, 2048, "none"),
	}
	snap := Build(nodes)

	for _, w := range snap.Warnings {
		if w.Kind == "ram_pressure" {
			t.Errorf("unexpected ram_pressure warning: %v", w)
		}
	}
}

func TestBuild_UsesReservationAwareSummaryWhenPresent(t *testing.T) {
	nodes := []models.NodeFacts{
		{
			Name:   "alpha",
			Status: models.StatusComplete,
			Resources: &models.Resources{
				RAMTotalMB:       8192,
				RAMFreeMB:        4096,
				RAMReservedMB:    1024,
				RAMAllocatableMB: 3072,
			},
			CollectedAt: ts(),
		},
	}

	snap := Build(nodes)
	if snap.Summary.TotalFreeRAMMB != 4096 {
		t.Errorf("free_ram: got %d, want 4096", snap.Summary.TotalFreeRAMMB)
	}
	if snap.Summary.TotalReservedMB != 1024 {
		t.Errorf("reserved_ram: got %d, want 1024", snap.Summary.TotalReservedMB)
	}
	if snap.Summary.TotalAllocatableMB != 3072 {
		t.Errorf("allocatable_ram: got %d, want 3072", snap.Summary.TotalAllocatableMB)
	}
}

// --- Edge cases ---

func TestBuild_EmptyNodes(t *testing.T) {
	snap := Build(nil)
	if snap.Status != models.SnapshotHealthy {
		t.Errorf("expected healthy for empty, got %q", snap.Status)
	}
	if snap.Summary.TotalNodes != 0 {
		t.Errorf("total: got %d, want 0", snap.Summary.TotalNodes)
	}
}

func TestBuild_NilResources(t *testing.T) {
	nodes := []models.NodeFacts{
		{
			Name:        "no-resources",
			Status:      models.StatusComplete,
			CollectedAt: ts(),
			// Resources is nil
		},
	}
	snap := Build(nodes)

	if snap.Status != models.SnapshotHealthy {
		t.Errorf("expected healthy, got %q", snap.Status)
	}
	if snap.Summary.TotalRAMMB != 0 {
		t.Errorf("total_ram: got %d, want 0", snap.Summary.TotalRAMMB)
	}
}

func TestBuild_TimestampIsSet(t *testing.T) {
	before := time.Now().UTC()
	snap := Build([]models.NodeFacts{completeNode("n", 8192, 4000, "none")})
	after := time.Now().UTC()

	if snap.Timestamp.Before(before) || snap.Timestamp.After(after) {
		t.Errorf("timestamp %v not between %v and %v", snap.Timestamp, before, after)
	}
}
