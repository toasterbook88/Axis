package knowledge

import (
	"encoding/json"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

func TestBuildUsesRealLoadAndReservationOverlay(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "alpha",
				Resources: &models.Resources{
					RAMTotalMB: 8192,
					RAMFreeMB:  4096,
					Load1M:     1.5,
					Load5M:     1.0,
					Load15M:    0.5,
				},
				Ollama: &models.OllamaInfo{Installed: true, Running: true},
			},
		},
	}
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 1024},
		},
	}

	k := Build(snap, st, "alpha")
	if k == nil {
		t.Fatal("expected knowledge")
	}
	if k.BestNode != "alpha" {
		t.Fatalf("BestNode = %q, want alpha", k.BestNode)
	}
	if k.Load["alpha"] != 1.5 {
		t.Fatalf("Load[alpha] = %.2f, want 1.5", k.Load["alpha"])
	}
	if k.Snapshot.Nodes[0].RAMReservedMB != 1024 {
		t.Fatalf("RAMReservedMB = %d, want 1024", k.Snapshot.Nodes[0].RAMReservedMB)
	}
	if k.Snapshot.Nodes[0].RAMAllocatableMB != 3072 {
		t.Fatalf("RAMAllocatableMB = %d, want 3072", k.Snapshot.Nodes[0].RAMAllocatableMB)
	}
	if _, ok := k.Ollama["alpha"]; !ok {
		t.Fatal("expected ollama map entry for alpha")
	}
}

func TestBuildIsNilSafe(t *testing.T) {
	k := Build(nil, nil, "")
	if k == nil {
		t.Fatal("expected knowledge")
	}
	if len(k.Snapshot.Nodes) != 0 {
		t.Fatalf("expected empty snapshot nodes, got %d", len(k.Snapshot.Nodes))
	}
	if len(k.Load) != 0 {
		t.Fatalf("expected empty load map, got %v", k.Load)
	}
}

func TestExecutionContextJSONPreservesTopLevelKeys(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "alpha",
				Resources: &models.Resources{
					RAMTotalMB: 8192,
					RAMFreeMB:  4096,
					Load1M:     2.25,
				},
			},
		},
	}
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 512},
		},
	}
	decision := models.PlacementDecision{Node: "alpha", OK: true}

	data, err := ExecutionContextJSON(snap, st, decision, "run tests", nil, nil)
	if err != nil {
		t.Fatalf("ExecutionContextJSON() error = %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	for _, key := range []string{"timestamp", "best_node", "snapshot", "state", "ollama", "load", "decision", "task_desc"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("expected key %q in execution payload", key)
		}
	}

	loadMap, ok := payload["load"].(map[string]any)
	if !ok {
		t.Fatalf("expected load map, got %#v", payload["load"])
	}
	if loadMap["alpha"] != 2.25 {
		t.Fatalf("expected load alpha 2.25, got %#v", loadMap["alpha"])
	}
}

func TestClusterKnowledgeJSON(t *testing.T) {
	k := Build(&models.ClusterSnapshot{}, nil, "")
	if got := k.JSON(); got == "" {
		t.Fatal("expected JSON output")
	}
}
