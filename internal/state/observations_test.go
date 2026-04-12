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
