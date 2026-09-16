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

func TestBuild_NoVantageWhenNoLocalNode(t *testing.T) {
	a := completeNode("node-a", 8192, 4000, "none")
	a.Hostname = "remote-a.example.com"
	b := completeNode("node-b", 8192, 5000, "none")
	b.Hostname = "remote-b.example.com"

	snap := Build([]models.NodeFacts{a, b})
	if snap.Vantage != nil {
		t.Errorf("expected no vantage when collecting host is absent, got %+v", snap.Vantage)
	}
}
