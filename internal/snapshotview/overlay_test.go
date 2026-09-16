package snapshotview_test

import (
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/snapshotview"
	"github.com/toasterbook88/axis/internal/state"
)

func baseNode(name string, freeRAM int64) models.NodeFacts {
	return models.NodeFacts{
		Name: name,
		Resources: &models.Resources{
			RAMFreeMB:  freeRAM,
			RAMTotalMB: freeRAM * 2,
			CPUCores:   4,
		},
	}
}

// ── Clone ──────────────────────────────────────────────────────────────────

func TestCloneNil(t *testing.T) {
	if snapshotview.Clone(nil) != nil {
		t.Fatal("Clone(nil) should return nil")
	}
}

func TestClonePreservesScalarFields(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	orig := &models.ClusterSnapshot{
		Timestamp: ts,
		Summary:   models.ClusterSummary{TotalReservableMB: 8192, TotalAllocatableMB: 8192},
	}
	clone := snapshotview.Clone(orig)
	if !clone.Timestamp.Equal(orig.Timestamp) {
		t.Errorf("Timestamp not preserved: got %v, want %v", clone.Timestamp, orig.Timestamp)
	}
	if clone.Summary.TotalAllocatableMB != 8192 {
		t.Error("Summary not preserved in clone")
	}
	if clone.Summary.TotalReservableMB != 8192 {
		t.Error("Reservable summary not preserved in clone")
	}
}

func TestApplyReservationViewNilSnapshot(t *testing.T) {
	// Must not panic.
	snapshotview.ApplyReservationView(nil, nil, nil)
	snapshotview.ApplyReservationView(nil, &state.ClusterState{}, nil)
}

func TestApplyReservationViewKeepsSystemReserveOutOfAllocatablePool(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "alpha",
				Resources: &models.Resources{
					RAMTotalMB: 8192,
					RAMFreeMB:  7900,
				},
			},
		},
	}
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 512},
		},
	}

	snapshotview.ApplyReservationView(snap, st, nil)

	if got := snap.Nodes[0].RAMAllocatableMB; got != 6656 {
		t.Fatalf("RAMAllocatableMB: got %d, want 6656", got)
	}
	if got := snap.Nodes[0].ReservableRAM(); got != 7168 {
		t.Fatalf("ReservableRAM: got %d, want 7168", got)
	}
}

func TestApplyReservationViewEmptyNodes(t *testing.T) {
	snap := &models.ClusterSnapshot{Nodes: []models.NodeFacts{}}
	snapshotview.ApplyReservationView(snap, nil, nil)
	if snap.Summary.TotalReservableMB != 0 || snap.Summary.TotalReservedMB != 0 || snap.Summary.TotalAllocatableMB != 0 {
		t.Error("empty node list should produce zero totals")
	}
}

func TestApplyReservationViewLedgerIsAuthoritativeIncludingZero(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			baseNode("ledger-zero", 4096),
			baseNode("ledger-reserved", 4096),
		},
	}
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"ledger-zero":     {ReservedMB: 1024},
			"ledger-reserved": {ReservedMB: 2048},
		},
	}

	limits := reservation.DefaultLimits()
	limits.SystemReserveMB = 0
	t.Setenv("HOME", t.TempDir())
	ledger := reservation.NewLedger(limits, nil)
	ledger.SetNodeCapacity("ledger-zero", 4096)
	ledger.SetNodeCapacity("ledger-reserved", 4096)
	if _, err := ledger.Reserve(reservation.Entry{
		ID:    "ledger-reservation",
		Node:  "ledger-reserved",
		RAMMB: 512,
	}); err != nil {
		t.Fatalf("reserve from ledger: %v", err)
	}

	snapshotview.ApplyReservationView(snap, st, ledger)

	if got := snap.Nodes[0].RAMReservedMB; got != 0 {
		t.Fatalf("authoritative ledger zero replaced by stale state reservation: got %d, want 0", got)
	}
	if got := snap.Nodes[0].RAMAllocatableMB; got != 4096 {
		t.Fatalf("ledger-zero allocatable: got %d, want 4096", got)
	}
	if got := snap.Nodes[1].RAMReservedMB; got != 512 {
		t.Fatalf("ledger reservation: got %d, want 512", got)
	}
	if got := snap.Summary.TotalReservedMB; got != 512 {
		t.Fatalf("summary reserved: got %d, want 512", got)
	}
}
