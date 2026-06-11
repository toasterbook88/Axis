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

func TestCloneEmptySnapshot(t *testing.T) {
	orig := &models.ClusterSnapshot{}
	got := snapshotview.Clone(orig)
	if got == nil {
		t.Fatal("Clone returned nil for non-nil input")
	}
	if got == orig {
		t.Fatal("Clone returned same pointer")
	}
}

func TestCloneIsDeepCopy(t *testing.T) {
	orig := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "alpha",
				Resources: &models.Resources{
					RAMFreeMB: 4096,
					GPUs:      []models.GPUInfo{{Model: "RTX 4090", Vendor: "nvidia"}},
				},
				Addresses: []models.NetworkAddress{{Kind: "lan", Address: "10.0.0.1"}},
				Tools:     []models.ToolInfo{{Name: "ollama"}},
				Ollama:    &models.OllamaInfo{Models: []string{"llama3"}},
				TurboQuant: &models.TurboQuantInfo{
					Backends:     []string{"metal"},
					Capabilities: []string{"fp16"},
				},
			},
		},
		Warnings: []models.Warning{{Message: "low disk"}},
	}

	clone := snapshotview.Clone(orig)

	// Mutate clone's slices — original must be unchanged.
	clone.Nodes[0].Resources.GPUs[0].Model = "MUTATED"
	clone.Nodes[0].Addresses[0].Address = "0.0.0.0"
	clone.Nodes[0].Tools[0].Name = "MUTATED"
	clone.Nodes[0].Ollama.Models[0] = "MUTATED"
	clone.Nodes[0].TurboQuant.Backends[0] = "MUTATED"
	clone.Nodes[0].TurboQuant.Capabilities[0] = "MUTATED"
	clone.Warnings[0].Message = "MUTATED"

	if orig.Nodes[0].Resources.GPUs[0].Model != "RTX 4090" {
		t.Error("Clone mutated original GPU slice")
	}
	if orig.Nodes[0].Addresses[0].Address != "10.0.0.1" {
		t.Error("Clone mutated original Addresses slice")
	}
	if orig.Nodes[0].Tools[0].Name != "ollama" {
		t.Error("Clone mutated original Tools slice")
	}
	if orig.Nodes[0].Ollama.Models[0] != "llama3" {
		t.Error("Clone mutated original Ollama models")
	}
	if orig.Nodes[0].TurboQuant.Backends[0] != "metal" {
		t.Error("Clone mutated original TurboQuant backends")
	}
	if orig.Nodes[0].TurboQuant.Capabilities[0] != "fp16" {
		t.Error("Clone mutated original TurboQuant capabilities")
	}
	if orig.Warnings[0].Message != "low disk" {
		t.Error("Clone mutated original Warnings slice")
	}
}

func TestCloneNodeWithNilOptionals(t *testing.T) {
	orig := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{Name: "bare", Resources: nil, Ollama: nil, TurboQuant: nil},
		},
	}
	clone := snapshotview.Clone(orig)
	if clone.Nodes[0].Resources != nil {
		t.Error("expected nil Resources in clone")
	}
	if clone.Nodes[0].Ollama != nil {
		t.Error("expected nil Ollama in clone")
	}
	if clone.Nodes[0].TurboQuant != nil {
		t.Error("expected nil TurboQuant in clone")
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

func TestCloneSeparatesNodeSlice(t *testing.T) {
	orig := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{baseNode("a", 1024), baseNode("b", 2048)},
	}
	clone := snapshotview.Clone(orig)
	// Appending to clone must not affect original length.
	clone.Nodes = append(clone.Nodes, baseNode("c", 512))
	if len(orig.Nodes) != 2 {
		t.Errorf("appending to clone affected original: len=%d", len(orig.Nodes))
	}
}

// ── ApplyReservationView ───────────────────────────────────────────────────

func TestApplyReservationViewNilSnapshot(t *testing.T) {
	// Must not panic.
	snapshotview.ApplyReservationView(nil, nil, nil)
	snapshotview.ApplyReservationView(nil, &state.ClusterState{}, nil)
}

func TestApplyReservationViewNilState(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{baseNode("alpha", 4096)},
	}
	snapshotview.ApplyReservationView(snap, nil, nil)
	n := snap.Nodes[0]
	if n.RAMReservedMB != 0 {
		t.Errorf("RAMReservedMB: got %d, want 0", n.RAMReservedMB)
	}
	if n.ReservableRAM() != 4096 {
		t.Errorf("ReservableRAM: got %d, want 4096", n.ReservableRAM())
	}
	if n.RAMAllocatableMB != 4096 {
		t.Errorf("RAMAllocatableMB: got %d, want 4096", n.RAMAllocatableMB)
	}
}

func TestApplyReservationViewWithReservation(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{baseNode("alpha", 8192)},
	}
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 2048},
		},
	}
	snapshotview.ApplyReservationView(snap, st, nil)
	n := snap.Nodes[0]
	if n.RAMReservedMB != 2048 {
		t.Errorf("RAMReservedMB: got %d, want 2048", n.RAMReservedMB)
	}
	if n.ReservableRAM() != 8192 {
		t.Errorf("ReservableRAM: got %d, want 8192", n.ReservableRAM())
	}
	if n.RAMAllocatableMB != 6144 {
		t.Errorf("RAMAllocatableMB: got %d, want 6144", n.RAMAllocatableMB)
	}
}

func TestApplyReservationViewAllocatableFloorZero(t *testing.T) {
	// When reserved > free RAM, allocatable must not go negative.
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{baseNode("alpha", 1024)},
	}
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 4096},
		},
	}
	snapshotview.ApplyReservationView(snap, st, nil)
	if got := snap.Nodes[0].ReservableRAM(); got != 1024 {
		t.Errorf("reservable should floor at 1024, got %d", got)
	}
	if snap.Nodes[0].RAMAllocatableMB != 0 {
		t.Errorf("allocatable should floor at 0, got %d", snap.Nodes[0].RAMAllocatableMB)
	}
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

func TestApplyReservationViewSummaryTotals(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			baseNode("alpha", 8192),
			baseNode("beta", 4096),
		},
	}
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 1024},
			"beta":  {ReservedMB: 512},
		},
	}
	snapshotview.ApplyReservationView(snap, st, nil)

	wantReserved := int64(1024 + 512)
	wantReservable := int64(8192 + 4096)
	wantAllocatable := int64((8192 - 1024) + (4096 - 512))

	if snap.Summary.TotalReservableMB != wantReservable {
		t.Errorf("TotalReservableMB: got %d, want %d", snap.Summary.TotalReservableMB, wantReservable)
	}
	if snap.Summary.TotalReservedMB != wantReserved {
		t.Errorf("TotalReservedMB: got %d, want %d", snap.Summary.TotalReservedMB, wantReserved)
	}
	if snap.Summary.TotalAllocatableMB != wantAllocatable {
		t.Errorf("TotalAllocatableMB: got %d, want %d", snap.Summary.TotalAllocatableMB, wantAllocatable)
	}
}

func TestApplyReservationViewSkipsNodesWithoutResources(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{Name: "nodata"},
			baseNode("beta", 2048),
		},
	}
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"beta": {ReservedMB: 512},
		},
	}
	snapshotview.ApplyReservationView(snap, st, nil)

	if snap.Nodes[0].Resources != nil {
		t.Error("expected nil resources on first node to remain nil")
	}
	if snap.Nodes[1].RAMAllocatableMB != 1536 {
		t.Errorf("beta allocatable: got %d, want 1536", snap.Nodes[1].RAMAllocatableMB)
	}
	if snap.Nodes[1].ReservableRAM() != 2048 {
		t.Errorf("beta reservable: got %d, want 2048", snap.Nodes[1].ReservableRAM())
	}
}

func TestApplyReservationViewUnknownNodeGetsZeroReservation(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{baseNode("gamma", 4096)},
	}
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{}, // gamma not present
	}
	snapshotview.ApplyReservationView(snap, st, nil)
	if snap.Nodes[0].RAMReservedMB != 0 {
		t.Error("node not in state should get zero reservation")
	}
	if got := snap.Nodes[0].ReservableRAM(); got != 4096 {
		t.Errorf("expected 4096 reservable, got %d", got)
	}
	if snap.Nodes[0].RAMAllocatableMB != 4096 {
		t.Errorf("expected 4096 allocatable, got %d", snap.Nodes[0].RAMAllocatableMB)
	}
}

func TestApplyReservationViewEmptyNodes(t *testing.T) {
	snap := &models.ClusterSnapshot{Nodes: []models.NodeFacts{}}
	snapshotview.ApplyReservationView(snap, nil, nil)
	if snap.Summary.TotalReservableMB != 0 || snap.Summary.TotalReservedMB != 0 || snap.Summary.TotalAllocatableMB != 0 {
		t.Error("empty node list should produce zero totals")
	}
}

func TestApplyReservationViewParentInheritance(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "node-a",
				Resources: &models.Resources{
					RAMTotalMB: 8192,
					RAMFreeMB:  8192,
				},
				SystemReserveMB: 0,
			},
		},
	}

	// Create a Ledger and set capacity
	limits := reservation.DefaultLimits()
	limits.SystemReserveMB = 0
	t.Setenv("HOME", t.TempDir())
	ledger := reservation.NewLedger(limits, nil)
	ledger.SetNodeCapacity("node-a", 8192)

	// Reserve parent reservation of 4096MB
	_, err := ledger.Reserve(reservation.Entry{
		ID:           "parent-id",
		Node:         "node-a",
		RAMMB:        4096,
		OwnerExecID:  "parent-id",
		OwnerSurface: "test",
	})
	if err != nil {
		t.Fatalf("failed to reserve: %v", err)
	}

	// Without AXIS_EXECUTION_PARENT_ID, reservation should be applied normally
	snapshotview.ApplyReservationView(snap, nil, ledger)
	if snap.Nodes[0].RAMReservedMB != 4096 {
		t.Errorf("expected 4096MB reserved, got %d", snap.Nodes[0].RAMReservedMB)
	}
	if snap.Nodes[0].RAMAllocatableMB != 3072 {
		t.Errorf("expected 3072MB allocatable, got %d", snap.Nodes[0].RAMAllocatableMB)
	}

	// With AXIS_EXECUTION_PARENT_ID set to parent-id, reservation should be inherited (subtracted)
	t.Setenv("AXIS_EXECUTION_PARENT_ID", "parent-id")
	snapshotview.ApplyReservationView(snap, nil, ledger)
	if snap.Nodes[0].RAMReservedMB != 0 {
		t.Errorf("expected 0MB reserved after parent subtraction, got %d", snap.Nodes[0].RAMReservedMB)
	}
	if snap.Nodes[0].RAMAllocatableMB != 7168 {
		t.Errorf("expected 7168MB allocatable after parent subtraction, got %d", snap.Nodes[0].RAMAllocatableMB)
	}
}
