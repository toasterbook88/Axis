package snapshotview_test

import (
	"testing"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/snapshotview"
	"github.com/toasterbook88/axis/internal/state"
)

// --- Clone ---

func TestCloneNilReturnsNil(t *testing.T) {
	if got := snapshotview.Clone(nil); got != nil {
		t.Fatal("expected nil clone of nil snapshot")
	}
}

func TestApplyReservationViewNilSnapIsNoop(t *testing.T) {
	// Must not panic.
	snapshotview.ApplyReservationView(nil, nil, nil)
	snapshotview.ApplyReservationView(nil, &state.ClusterState{}, nil)
}

func TestApplyReservationViewWithNilStateUsesZeroReserved(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "alpha",
				Resources: &models.Resources{
					RAMTotalMB: 8192,
					RAMFreeMB:  4096,
				},
			},
		},
	}

	snapshotview.ApplyReservationView(snap, nil, nil)

	node := snap.Nodes[0]
	if node.RAMReservedMB != 0 {
		t.Errorf("expected reserved 0, got %d", node.RAMReservedMB)
	}
	if node.ReservableRAM() != 4096 {
		t.Errorf("expected reservable 4096, got %d", node.ReservableRAM())
	}
	if node.RAMAllocatableMB != 4096 {
		t.Errorf("expected allocatable 4096, got %d", node.RAMAllocatableMB)
	}
	if snap.Summary.TotalReservableMB != 4096 {
		t.Errorf("expected summary reservable 4096, got %d", snap.Summary.TotalReservableMB)
	}
	if snap.Summary.TotalReservedMB != 0 {
		t.Errorf("expected summary reserved 0, got %d", snap.Summary.TotalReservedMB)
	}
	if snap.Summary.TotalAllocatableMB != 4096 {
		t.Errorf("expected summary allocatable 4096, got %d", snap.Summary.TotalAllocatableMB)
	}
}

func TestApplyReservationViewAppliesReservedFromState(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "alpha",
				Resources: &models.Resources{
					RAMTotalMB: 16384,
					RAMFreeMB:  8192,
				},
			},
			{
				Name: "beta",
				Resources: &models.Resources{
					RAMTotalMB: 8192,
					RAMFreeMB:  4096,
				},
			},
		},
	}

	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 1024},
			"beta":  {ReservedMB: 512},
		},
	}

	snapshotview.ApplyReservationView(snap, st, nil)

	alpha := snap.Nodes[0]
	if alpha.RAMReservedMB != 1024 {
		t.Errorf("alpha reserved: got %d, want 1024", alpha.RAMReservedMB)
	}
	if alpha.ReservableRAM() != 8192 {
		t.Errorf("alpha reservable: got %d, want 8192", alpha.ReservableRAM())
	}
	if alpha.RAMAllocatableMB != 7168 {
		t.Errorf("alpha allocatable: got %d, want 7168", alpha.RAMAllocatableMB)
	}

	beta := snap.Nodes[1]
	if beta.RAMReservedMB != 512 {
		t.Errorf("beta reserved: got %d, want 512", beta.RAMReservedMB)
	}
	if beta.ReservableRAM() != 4096 {
		t.Errorf("beta reservable: got %d, want 4096", beta.ReservableRAM())
	}
	if beta.RAMAllocatableMB != 3584 {
		t.Errorf("beta allocatable: got %d, want 3584", beta.RAMAllocatableMB)
	}

	if snap.Summary.TotalReservableMB != 12288 {
		t.Errorf("summary reservable: got %d, want 12288", snap.Summary.TotalReservableMB)
	}
	if snap.Summary.TotalReservedMB != 1536 {
		t.Errorf("summary reserved: got %d, want 1536", snap.Summary.TotalReservedMB)
	}
	if snap.Summary.TotalAllocatableMB != 10752 {
		t.Errorf("summary allocatable: got %d, want 10752", snap.Summary.TotalAllocatableMB)
	}
}

func TestApplyReservationViewNodeNotInStateGetsZeroReserved(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "unknown",
				Resources: &models.Resources{
					RAMFreeMB: 2048,
				},
			},
		},
	}

	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"other": {ReservedMB: 1024},
		},
	}

	snapshotview.ApplyReservationView(snap, st, nil)

	if snap.Nodes[0].RAMReservedMB != 0 {
		t.Errorf("expected reserved 0 for unknown node, got %d", snap.Nodes[0].RAMReservedMB)
	}
	if snap.Nodes[0].ReservableRAM() != 2048 {
		t.Errorf("expected reservable 2048, got %d", snap.Nodes[0].ReservableRAM())
	}
	if snap.Nodes[0].RAMAllocatableMB != 2048 {
		t.Errorf("expected allocatable 2048, got %d", snap.Nodes[0].RAMAllocatableMB)
	}
}
