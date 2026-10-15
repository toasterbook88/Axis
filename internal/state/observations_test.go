package state

import (
	"encoding/json"
	"strings"
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
		Scope:         scope,
		ObservedAt:    time.Now().UTC().Add(-2 * time.Minute),
		SampleCount:   1,
		LastSuccess:   true,
		WallTimeMS:    100,
		ObservedRSSMB: 1024,
	})
	s.RecordObservation(models.ExecutionObservation{
		Scope:         scope,
		ObservedAt:    time.Now().UTC(),
		SampleCount:   1,
		LastSuccess:   false,
		WallTimeMS:    300,
		ObservedRSSMB: 2048,
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
	if obs.ObservedRSSMB != 2048 {
		t.Fatalf("observed_rss_mb = %d, want 2048", obs.ObservedRSSMB)
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

func TestMergeKeepsUnsetContextAndDeviceAndReplacesSetValues(t *testing.T) {
	s := &ClusterState{}
	scope := models.ObservationScope{
		Node:      "storage",
		Workload:  models.ClassLlamaServer,
		Backend:   "llama.cpp",
		Tool:      "llama-server",
		ModelName: "a.gguf",
	}
	ctx := 2048
	zero := 0
	s.RecordObservation(models.ExecutionObservation{
		Scope:         scope,
		ObservedAt:    time.Now().UTC(),
		LastSuccess:   true,
		WallTimeMS:    10,
		ObservedRSSMB: 100,
		ContextTokens: &ctx,
		DeviceIndex:   &zero,
	})
	zero = 7
	ctx = 1
	s.RecordObservation(models.ExecutionObservation{
		Scope:         scope,
		ObservedAt:    time.Now().UTC(),
		LastSuccess:   true,
		WallTimeMS:    10,
		ObservedRSSMB: 50,
	})
	obs, ok := s.Observation(scope)
	if !ok || obs == nil || obs.ContextTokens == nil || *obs.ContextTokens != 2048 {
		t.Fatalf("nil context sample cleared the previous value: %+v", obs)
	}
	if obs.DeviceIndex == nil || *obs.DeviceIndex != 0 {
		t.Fatalf("nil device sample cleared index 0: %+v", obs)
	}
	if obs.ObservedRSSMB != 100 {
		t.Fatalf("peak = %d, want the previous max 100", obs.ObservedRSSMB)
	}

	nextCtx := 4096
	one := 1
	s.RecordObservation(models.ExecutionObservation{
		Scope:         scope,
		ObservedAt:    time.Now().UTC(),
		LastSuccess:   true,
		WallTimeMS:    10,
		ContextTokens: &nextCtx,
		DeviceIndex:   &one,
	})
	one = 9
	obs, ok = s.Observation(scope)
	if !ok || obs.ContextTokens == nil || *obs.ContextTokens != 4096 || obs.DeviceIndex == nil || *obs.DeviceIndex != 1 {
		t.Fatalf("set sample did not replace context and device: %+v", obs)
	}
	if len(s.Observations) != 1 {
		t.Fatalf("optional fields changed the observation key: %d entries", len(s.Observations))
	}

	other := scope
	if ObservationKey(other) != ObservationKey(obs.Scope) {
		t.Fatal("context and device index changed ObservationKey")
	}
}

func TestObservationOptionalFieldsRoundTripIndexZero(t *testing.T) {
	t.Setenv("AXIS_HOME", t.TempDir())
	s := &ClusterState{}
	scope := models.ObservationScope{
		Node:     "storage",
		Workload: models.ClassLlamaServer,
		Backend:  "llama.cpp",
		Tool:     "llama-server",
	}
	zero := 0
	ctx := 1024
	s.RecordObservation(models.ExecutionObservation{
		Scope:         scope,
		ObservedAt:    time.Now().UTC(),
		LastSuccess:   true,
		WallTimeMS:    4,
		ContextTokens: &ctx,
		DeviceIndex:   &zero,
	})
	raw, err := json.Marshal(s.Observations[ObservationKey(scope)])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"device_index":0`) {
		t.Fatalf("index 0 was omitted: %s", raw)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	obs, ok := loaded.Observation(scope)
	if !ok || obs.DeviceIndex == nil || *obs.DeviceIndex != 0 || obs.ContextTokens == nil || *obs.ContextTokens != 1024 {
		t.Fatalf("round trip = %+v", obs)
	}
	unset := models.ExecutionObservation{Scope: scope, ObservedAt: time.Now().UTC(), WallTimeMS: 1, LastSuccess: true}
	raw, err = json.Marshal(normalizeObservation(unset))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "device_index") || strings.Contains(string(raw), "context_tokens") {
		t.Fatalf("nil pointers were written as zero: %s", raw)
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
