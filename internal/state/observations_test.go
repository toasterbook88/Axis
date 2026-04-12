package state

import (
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestRecordObservationMergesSamplesAndPeaks(t *testing.T) {
	s := &ClusterState{}
	scope := models.ObservationScope{
		Node:     "alpha",
		Workload: models.ClassRepoAnalysis,
		Tool:     "git",
	}

	s.RecordObservation(models.ExecutionObservation{
		Scope:       scope,
		ObservedAt:  time.Now().UTC().Add(-2 * time.Minute),
		SampleCount: 1,
		LastSuccess: true,
		WallTimeMS:  100,
		PeakRAMMB:   1024,
	})
	s.RecordObservation(models.ExecutionObservation{
		Scope:       scope,
		ObservedAt:  time.Now().UTC(),
		SampleCount: 1,
		LastSuccess: false,
		WallTimeMS:  300,
		PeakRAMMB:   2048,
	})

	obs, ok := s.Observation(scope)
	if !ok || obs == nil {
		t.Fatal("expected merged observation")
	}
	if obs.SampleCount != 2 {
		t.Fatalf("sample_count = %d, want 2", obs.SampleCount)
	}
	if obs.WallTimeMS != 200 {
		t.Fatalf("wall_time_ms = %d, want 200", obs.WallTimeMS)
	}
	if obs.PeakRAMMB != 2048 {
		t.Fatalf("peak_ram_mb = %d, want 2048", obs.PeakRAMMB)
	}
	if obs.LastSuccess {
		t.Fatal("expected last_success to track the latest sample")
	}
}

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
func TestObservationKeyModelNameSegregation(t *testing.T) {
	base := models.ObservationScope{
		Node:     "cortex",
		Workload: models.ClassLocalLLMInference,
		Backend:  "ollama",
		Tool:     "ollama",
	}

	// Empty ModelName must equal the legacy key (no model field in hash input).
	keyNoModel := ObservationKey(base)
	baseWithEmpty := base
	baseWithEmpty.ModelName = ""
	if ObservationKey(baseWithEmpty) != keyNoModel {
		t.Error("empty ModelName should produce the same key as omitted ModelName")
	}

	// Non-empty ModelName must produce a different key.
	withLlama := base
	withLlama.ModelName = "llama3.2:latest"
	keyLlama := ObservationKey(withLlama)
	if keyLlama == keyNoModel {
		t.Error("non-empty ModelName should produce a different key from empty")
	}

	// Two different model names must produce different keys.
	withQwen := base
	withQwen.ModelName = "qwen2.5:14b"
	keyQwen := ObservationKey(withQwen)
	if keyQwen == keyLlama {
		t.Error("different model names should produce different keys")
	}

	// Case-insensitive: "Llama3.2:latest" must equal "llama3.2:latest".
	withLlamaUpper := base
	withLlamaUpper.ModelName = "Llama3.2:latest"
	if ObservationKey(withLlamaUpper) != keyLlama {
		t.Error("ObservationKey should be case-insensitive for ModelName")
	}
}

// TestObservationStoreSeparatesByModelName ensures that RecordObservation
// stores model-scoped observations separately so per-model history stays clean.
func TestObservationStoreSeparatesByModelName(t *testing.T) {
	s := &ClusterState{}
	base := models.ObservationScope{
		Node:     "cortex",
		Workload: models.ClassLocalLLMInference,
		Backend:  "ollama",
		Tool:     "ollama",
	}

	llamaScope := base
	llamaScope.ModelName = "llama3.2:latest"
	qwenScope := base
	qwenScope.ModelName = "qwen2.5:14b"

	now := time.Now().UTC()
	s.RecordObservation(models.ExecutionObservation{
		Scope: llamaScope, ObservedAt: now, SampleCount: 1,
		LastSuccess: true, WallTimeMS: 800,
	})
	s.RecordObservation(models.ExecutionObservation{
		Scope: qwenScope, ObservedAt: now, SampleCount: 1,
		LastSuccess: true, WallTimeMS: 1200,
	})

	llamaObs, ok := s.Observation(llamaScope)
	if !ok || llamaObs == nil {
		t.Fatal("expected llama observation")
	}
	if llamaObs.WallTimeMS != 800 {
		t.Errorf("llama WallTimeMS = %d, want 800", llamaObs.WallTimeMS)
	}

	qwenObs, ok := s.Observation(qwenScope)
	if !ok || qwenObs == nil {
		t.Fatal("expected qwen observation")
	}
	if qwenObs.WallTimeMS != 1200 {
		t.Errorf("qwen WallTimeMS = %d, want 1200", qwenObs.WallTimeMS)
	}

	// The unscoped base scope should not return either model-specific entry.
	_, ok = s.Observation(base)
	if ok {
		t.Error("unscoped lookup should not match a model-scoped entry")
	}
}

// TestNormalizeObservationSyncsScopeModelName verifies that when ModelName is
// empty but Scope.ModelName is set, normalizeObservation copies it over so
// empiricalReason always has a model name to display after merges.
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
