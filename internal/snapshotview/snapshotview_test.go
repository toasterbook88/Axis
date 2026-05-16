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

func TestCloneProducesIndependentCopy(t *testing.T) {
	orig := &models.ClusterSnapshot{
		Status: models.SnapshotHealthy,
		Nodes: []models.NodeFacts{
			{Name: "alpha"},
		},
		Warnings: []models.Warning{
			{Kind: "unreachable", Message: "host down"},
		},
	}

	clone := snapshotview.Clone(orig)
	if clone == orig {
		t.Fatal("expected a new pointer, not the same")
	}
	if clone.Status != orig.Status {
		t.Errorf("status mismatch: got %q, want %q", clone.Status, orig.Status)
	}
	if len(clone.Nodes) != 1 || clone.Nodes[0].Name != "alpha" {
		t.Errorf("unexpected nodes: %v", clone.Nodes)
	}
	if len(clone.Warnings) != 1 || clone.Warnings[0].Kind != "unreachable" {
		t.Errorf("unexpected warnings: %v", clone.Warnings)
	}

	// Mutating the clone must not affect the original.
	clone.Nodes[0].Name = "mutated"
	if orig.Nodes[0].Name != "alpha" {
		t.Error("mutating clone node changed original")
	}
	clone.Warnings[0].Kind = "mutated"
	if orig.Warnings[0].Kind != "unreachable" {
		t.Error("mutating clone warning changed original")
	}
}

func TestCloneDeepCopiesResources(t *testing.T) {
	gpu := "NVIDIA A100"
	orig := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "gpu-node",
				Resources: &models.Resources{
					RAMTotalMB: 32768,
					RAMFreeMB:  16384,
					GPUs: []models.GPUInfo{{
						Model:        gpu,
						Vendor:       "nvidia",
						Capabilities: []string{"cuda", "tensor"},
					}},
				},
			},
		},
	}

	clone := snapshotview.Clone(orig)
	clone.Nodes[0].Resources.GPUs[0].Model = "mutated"
	clone.Nodes[0].Resources.GPUs[0].Capabilities[0] = "mutated"
	if orig.Nodes[0].Resources.GPUs[0].Model != gpu {
		t.Error("mutating clone GPUs changed original")
	}
	if orig.Nodes[0].Resources.GPUs[0].Capabilities[0] != "cuda" {
		t.Error("mutating clone GPU capabilities changed original")
	}

	// Resources pointer itself must be different.
	if clone.Nodes[0].Resources == orig.Nodes[0].Resources {
		t.Error("expected cloned Resources to be a new pointer")
	}
}

func TestCloneDeepCopiesAddressesAndTools(t *testing.T) {
	orig := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "alpha",
				Addresses: []models.NetworkAddress{
					{Kind: "ipv4", Address: "10.0.0.1"},
				},
				Tools: []models.ToolInfo{
					{Name: "ollama", Path: "/usr/bin/ollama"},
				},
			},
		},
	}

	clone := snapshotview.Clone(orig)
	clone.Nodes[0].Addresses[0].Address = "mutated"
	if orig.Nodes[0].Addresses[0].Address != "10.0.0.1" {
		t.Error("mutating clone address changed original")
	}
	clone.Nodes[0].Tools[0].Name = "mutated"
	if orig.Nodes[0].Tools[0].Name != "ollama" {
		t.Error("mutating clone tool changed original")
	}
}

func TestCloneDeepCopiesOllama(t *testing.T) {
	orig := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "alpha",
				Ollama: &models.OllamaInfo{
					Installed: true,
					Models:    []string{"llama3", "mistral"},
				},
			},
		},
	}

	clone := snapshotview.Clone(orig)
	clone.Nodes[0].Ollama.Models[0] = "mutated"
	if orig.Nodes[0].Ollama.Models[0] != "llama3" {
		t.Error("mutating clone Ollama models changed original")
	}
	if clone.Nodes[0].Ollama == orig.Nodes[0].Ollama {
		t.Error("expected cloned Ollama to be a new pointer")
	}
}

func TestCloneDeepCopiesTurboQuant(t *testing.T) {
	orig := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "alpha",
				TurboQuant: &models.TurboQuantInfo{
					Supported:    true,
					Backends:     []string{"metal"},
					Capabilities: []string{"fp16"},
				},
			},
		},
	}

	clone := snapshotview.Clone(orig)
	clone.Nodes[0].TurboQuant.Backends[0] = "mutated"
	if orig.Nodes[0].TurboQuant.Backends[0] != "metal" {
		t.Error("mutating clone TurboQuant backends changed original")
	}
	clone.Nodes[0].TurboQuant.Capabilities[0] = "mutated"
	if orig.Nodes[0].TurboQuant.Capabilities[0] != "fp16" {
		t.Error("mutating clone TurboQuant capabilities changed original")
	}
	if clone.Nodes[0].TurboQuant == orig.Nodes[0].TurboQuant {
		t.Error("expected cloned TurboQuant to be a new pointer")
	}
}

func TestCloneNodeWithNilResourcesAndNilOllama(t *testing.T) {
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

func TestCloneEmptyNodesAndWarnings(t *testing.T) {
	orig := &models.ClusterSnapshot{Status: models.SnapshotHealthy}
	clone := snapshotview.Clone(orig)
	if len(clone.Nodes) != 0 {
		t.Errorf("expected empty nodes, got %d", len(clone.Nodes))
	}
	if len(clone.Warnings) != 0 {
		t.Errorf("expected empty warnings, got %d", len(clone.Warnings))
	}
}

// --- ApplyReservationView ---

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

func TestApplyReservationViewClampsAllocatableToZero(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "alpha",
				Resources: &models.Resources{
					RAMFreeMB: 512,
				},
			},
		},
	}

	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 2048},
		},
	}

	snapshotview.ApplyReservationView(snap, st, nil)

	if snap.Nodes[0].ReservableRAM() != 512 {
		t.Errorf("expected reservable 512, got %d", snap.Nodes[0].ReservableRAM())
	}
	if snap.Nodes[0].RAMAllocatableMB != 0 {
		t.Errorf("expected allocatable clamped to 0, got %d", snap.Nodes[0].RAMAllocatableMB)
	}
	if snap.Summary.TotalReservableMB != 512 {
		t.Errorf("expected summary reservable 512, got %d", snap.Summary.TotalReservableMB)
	}
	if snap.Summary.TotalAllocatableMB != 0 {
		t.Errorf("expected summary allocatable clamped to 0, got %d", snap.Summary.TotalAllocatableMB)
	}
}

func TestApplyReservationViewSkipsNodesWithNilResources(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{Name: "bare", Resources: nil},
			{
				Name: "alpha",
				Resources: &models.Resources{
					RAMFreeMB: 4096,
				},
			},
		},
	}

	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"bare":  {ReservedMB: 999},
			"alpha": {ReservedMB: 256},
		},
	}

	snapshotview.ApplyReservationView(snap, st, nil)

	// bare node has nil resources — must stay nil without panic
	if snap.Nodes[0].Resources != nil {
		t.Error("expected nil Resources to remain nil")
	}
	if snap.Nodes[1].RAMAllocatableMB != 3840 {
		t.Errorf("alpha allocatable: got %d, want 3840", snap.Nodes[1].RAMAllocatableMB)
	}
	if snap.Nodes[1].ReservableRAM() != 4096 {
		t.Errorf("alpha reservable: got %d, want 4096", snap.Nodes[1].ReservableRAM())
	}
	// Only alpha's allocatable counts.
	if snap.Summary.TotalReservableMB != 4096 {
		t.Errorf("summary reservable: got %d, want 4096", snap.Summary.TotalReservableMB)
	}
	if snap.Summary.TotalAllocatableMB != 3840 {
		t.Errorf("summary allocatable: got %d, want 3840", snap.Summary.TotalAllocatableMB)
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
