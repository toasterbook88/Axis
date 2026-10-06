package placement

import (
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

func llamaPeakNode(name string, allocatable int64) models.NodeFacts {
	return models.NodeFacts{
		Name:             name,
		RAMAllocatableMB: allocatable,
		Resources:        &models.Resources{RAMTotalMB: allocatable + 2048, RAMFreeMB: allocatable},
	}
}

func recordLlamaObserved(st *state.ClusterState, node, model string, observed int64, observedAt time.Time) {
	st.RecordObservation(models.ExecutionObservation{
		Scope: models.ObservationScope{
			Node:      node,
			Workload:  models.ClassLlamaServer,
			Backend:   "llama.cpp",
			Tool:      "llama-server",
			ModelName: model,
		},
		ObservedAt:    observedAt,
		LastSuccess:   true,
		WallTimeMS:    20,
		ObservedRSSMB: observed,
	})
}

func TestLlamaServerObservedExclusionUsesRecordedObservation(t *testing.T) {
	now := time.Now().UTC()
	st := &state.ClusterState{}
	recordLlamaObserved(st, "tight", "a.gguf", 5000, now)
	recordLlamaObserved(st, "wide", "a.gguf", 5000, now)
	st.RecordObservation(models.ExecutionObservation{
		Scope: models.ObservationScope{
			Node:      "tight",
			Workload:  models.ClassLocalLLMInference,
			Backend:   "ollama",
			Tool:      "ollama",
			ModelName: "a.gguf",
		},
		ObservedAt:    now,
		LastSuccess:   true,
		WallTimeMS:    20,
		ObservedRSSMB: 99999,
	})

	reason, blocked := LlamaServerObservedExclusion(llamaPeakNode("tight", 1000), "a.gguf", st)
	if !blocked || reason != "observed RSS 5000MB exceeds allocatable 1000MB" {
		t.Fatalf("exclusion = %q blocked=%v", reason, blocked)
	}
	if _, blocked := LlamaServerObservedExclusion(llamaPeakNode("wide", 8000), "a.gguf", st); blocked {
		t.Fatal("peak within allocatable RAM excluded the node")
	}
	if _, blocked := LlamaServerObservedExclusion(llamaPeakNode("tight", 1000), "other.gguf", st); blocked {
		t.Fatal("a different model name reused the llama-server peak")
	}
	if _, blocked := LlamaServerObservedExclusion(llamaPeakNode("missing", 1000), "a.gguf", st); blocked {
		t.Fatal("missing observation excluded the node")
	}
	if _, blocked := LlamaServerObservedExclusion(llamaPeakNode("tight", 1000), "a.gguf", nil); blocked {
		t.Fatal("nil state excluded the node")
	}
	ollamaOnly := &state.ClusterState{}
	ollamaOnly.RecordObservation(models.ExecutionObservation{
		Scope: models.ObservationScope{
			Node:      "tight",
			Workload:  models.ClassLocalLLMInference,
			Backend:   "ollama",
			Tool:      "ollama",
			ModelName: "a.gguf",
		},
		ObservedAt:    now,
		LastSuccess:   true,
		WallTimeMS:    20,
		ObservedRSSMB: 99999,
	})
	if _, blocked := LlamaServerObservedExclusion(llamaPeakNode("tight", 1000), "a.gguf", ollamaOnly); blocked {
		t.Fatal("an ollama peak excluded a llama-server plan")
	}

	stale := &state.ClusterState{}
	recordLlamaObserved(stale, "tight", "a.gguf", 5000, now.Add(-(state.ObservationStaleAfter + time.Hour)))
	if _, blocked := LlamaServerObservedExclusion(llamaPeakNode("tight", 1000), "a.gguf", stale); blocked {
		t.Fatal("stale peak excluded the node")
	}
}
