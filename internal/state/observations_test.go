package state

import (
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestSaveLoadPreservesObservations(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	s := &ClusterState{
		Nodes:        map[string]NodeState{},
		Observations: map[string]models.ExecutionObservation{},
	}
	scope := models.ObservationScope{
		Node:     "alpha",
		Workload: models.ClassLocalLLMInference,
		Backend:  "ollama",
		Tool:     "ollama",
	}
	s.RecordObservation(models.ExecutionObservation{
		Scope:       scope,
		ObservedAt:  time.Now().UTC(),
		SampleCount: 1,
		LastSuccess: true,
		WallTimeMS:  1500,
	})

	if err := s.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	obs, ok := loaded.Observation(scope)
	if !ok || obs == nil {
		t.Fatal("expected observation after round trip")
	}
	if obs.WallTimeMS != 1500 {
		t.Fatalf("wall_time_ms = %d, want 1500", obs.WallTimeMS)
	}
}

// TestObservationKeyModelNameSegregation verifies that observations with
// different ModelNames produce distinct keys, and that an empty ModelName
// produces the same key as an observation created before ModelName existed
// (backward compatibility invariant).
func TestNormalizeObservationSyncsScopeModelName(t *testing.T) {
	obs := models.ExecutionObservation{
		Scope: models.ObservationScope{
			Node:      "cortex",
			ModelName: "llama3.2:latest",
		},
		SampleCount: 1,
		WallTimeMS:  100,
		LastSuccess: true,
	}
	s := &ClusterState{}
	s.RecordObservation(obs)

	scope := models.ObservationScope{Node: "cortex", ModelName: "llama3.2:latest"}
	stored, ok := s.Observation(scope)
	if !ok || stored == nil {
		t.Fatal("expected stored observation")
	}
	if stored.ModelName != "llama3.2:latest" {
		t.Errorf("ModelName = %q, want %q", stored.ModelName, "llama3.2:latest")
	}
}

func TestObservationIsFresh(t *testing.T) {
	now := time.Now().UTC()
	fresh := models.ExecutionObservation{ObservedAt: now.Add(-time.Hour)}
	if !ObservationIsFresh(fresh, now) {
		t.Fatal("expected fresh observation")
	}
	stale := models.ExecutionObservation{ObservedAt: now.Add(-(ObservationStaleAfter + time.Minute))}
	if ObservationIsFresh(stale, now) {
		t.Fatal("expected stale observation to be rejected")
	}
}
