package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveAndLoadRoundTripCreatesDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	original := &ClusterState{
		Nodes: map[string]NodeState{
			"alpha": {
				ReservedMB:   2048,
				LastTask:     "build project",
				LastPlacedAt: time.Now().UTC(),
				ActiveTasks:  1,
				ActiveExecs:  []string{"exec-1"},
			},
		},
		Decisions: []string{"alpha: build project"},
	}

	if err := original.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(home, ".axis", "state.json")); err != nil {
		t.Fatalf("expected state file to exist: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	got, ok := loaded.Nodes["alpha"]
	if !ok {
		t.Fatal("expected alpha node to round-trip")
	}
	if got.ReservedMB != 2048 {
		t.Fatalf("ReservedMB = %d, want 2048", got.ReservedMB)
	}
	if got.LastTask != "build project" {
		t.Fatalf("LastTask = %q, want build project", got.LastTask)
	}
	if len(loaded.Decisions) != 1 || loaded.Decisions[0] != "alpha: build project" {
		t.Fatalf("Decisions = %v, want single round-tripped decision", loaded.Decisions)
	}
}

func TestLoadExpiresStaleReservations(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := filepath.Join(home, ".axis", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	payload := ClusterState{
		Nodes: map[string]NodeState{
			"expired": {
				ReservedMB:   1024,
				LastPlacedAt: time.Now().Add(-46 * time.Minute).UTC(),
			},
			"fresh": {
				ReservedMB:   512,
				LastPlacedAt: time.Now().Add(-10 * time.Minute).UTC(),
				ActiveTasks:  1,
				ActiveExecs:  []string{"exec-1"},
			},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if _, ok := loaded.Nodes["expired"]; ok {
		t.Fatal("expected expired reservation to be removed")
	}
	if _, ok := loaded.Nodes["fresh"]; !ok {
		t.Fatal("expected fresh reservation to remain")
	}
}

func TestLoadClearsLegacyReservationsWithoutExecIDs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := filepath.Join(home, ".axis", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	payload := ClusterState{
		Nodes: map[string]NodeState{
			"legacy": {
				ReservedMB:   1152,
				LastTask:     "git status",
				LastPlacedAt: time.Now().UTC(),
				ActiveTasks:  1,
			},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(loaded.Nodes) != 0 {
		t.Fatalf("expected legacy reservations to be cleared, got %v", loaded.Nodes)
	}

	reloadedData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var reloaded ClusterState
	if err := json.Unmarshal(reloadedData, &reloaded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(reloaded.Nodes) != 0 {
		t.Fatalf("expected cleaned state file, got %v", reloaded.Nodes)
	}
}

func TestRecordPlacementAccumulatesReservationsAndCapsDecisionHistory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	s := &ClusterState{}

	for i := 0; i < 25; i++ {
		_, err := s.AcquireTask("alpha", "task", 100)
		if err != nil {
			t.Fatalf("AcquireTask() error = %v", err)
		}
	}

	got := s.Nodes["alpha"]
	if got.ReservedMB != 2500 {
		t.Fatalf("ReservedMB = %d, want 2500", got.ReservedMB)
	}
	if got.ActiveTasks != 25 {
		t.Fatalf("ActiveTasks = %d, want 25", got.ActiveTasks)
	}
	if len(s.Decisions) != 20 {
		t.Fatalf("len(Decisions) = %d, want 20", len(s.Decisions))
	}
}

func TestStateLifecycleAcquireAndRelease(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	s := &ClusterState{}

	execID, err := s.AcquireTask("alpha", "git status", 512)
	if err != nil {
		t.Fatalf("AcquireTask() error = %v", err)
	}

	ns, ok := s.Nodes["alpha"]
	if !ok {
		t.Fatal("expected alpha node after acquire")
	}
	if ns.ReservedMB != 512 {
		t.Fatalf("ReservedMB = %d, want 512", ns.ReservedMB)
	}
	if ns.ActiveTasks != 1 {
		t.Fatalf("ActiveTasks = %d, want 1", ns.ActiveTasks)
	}
	if len(ns.ActiveExecs) != 1 || ns.ActiveExecs[0] != execID {
		t.Fatalf("ActiveExecs = %v, want [%s]", ns.ActiveExecs, execID)
	}

	if err := s.ReleaseTask("alpha", execID, 512); err != nil {
		t.Fatalf("ReleaseTask() error = %v", err)
	}
	if _, ok := s.Nodes["alpha"]; ok {
		t.Fatal("expected alpha node to be pruned after release")
	}
}

func TestLoadNormalizesActiveTasksToExecCount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := filepath.Join(home, ".axis", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	payload := ClusterState{
		Nodes: map[string]NodeState{
			"alpha": {
				ReservedMB:   1024,
				LastPlacedAt: time.Now().UTC(),
				ActiveTasks:  99,
				ActiveExecs:  []string{"exec-1", "exec-2"},
			},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	ns := loaded.Nodes["alpha"]
	if ns.ActiveTasks != 2 {
		t.Fatalf("ActiveTasks = %d, want 2", ns.ActiveTasks)
	}
}

func TestSaveInitializesNilNodes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	s := &ClusterState{}
	if err := s.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if s.Nodes == nil {
		t.Fatal("expected Save to initialize Nodes map")
	}
}

func TestRecordPlacementPersistsReservation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	s := &ClusterState{}
	s.RecordPlacement("alpha", 256, "inspect repo")

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	ns, ok := loaded.Nodes["alpha"]
	if !ok {
		t.Fatal("expected alpha node after RecordPlacement")
	}
	if ns.ReservedMB != 256 {
		t.Fatalf("ReservedMB = %d, want 256", ns.ReservedMB)
	}
	if len(ns.ActiveExecs) != 1 {
		t.Fatalf("ActiveExecs = %v, want single exec", ns.ActiveExecs)
	}
}

func TestReleaseTaskHandlesMissingStateAndClampsValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var empty ClusterState
	if err := empty.ReleaseTask("missing", "exec", 10); err != nil {
		t.Fatalf("ReleaseTask() on empty state error = %v", err)
	}

	s := &ClusterState{
		Nodes: map[string]NodeState{
			"alpha": {
				ReservedMB:   128,
				LastPlacedAt: time.Now().UTC(),
				ActiveTasks:  1,
				ActiveExecs:  []string{"exec-1"},
			},
		},
	}

	if err := s.ReleaseTask("alpha", "wrong-exec", 512); err != nil {
		t.Fatalf("ReleaseTask() error = %v", err)
	}

	if _, ok := s.Nodes["alpha"]; ok {
		t.Fatal("expected alpha node to be pruned after clamp to zero")
	}
}

func TestLoadRecoversFromInvalidJSONByQuarantiningFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := filepath.Join(home, ".axis", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	loaded, err := Load()
	if err == nil {
		t.Fatal("expected recoverable warning on invalid json")
	}
	if loaded == nil {
		t.Fatal("expected recovered empty state")
	}
	if len(loaded.Nodes) != 0 {
		t.Fatalf("expected empty recovered state, got %v", loaded.Nodes)
	}

	matches, globErr := filepath.Glob(filepath.Join(home, ".axis", "state.json.corrupt-*"))
	if globErr != nil {
		t.Fatalf("Glob() error = %v", globErr)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one quarantined backup, got %v", matches)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("expected original state.json to be quarantined, stat err = %v", statErr)
	}
}

func TestLoadFailsWhenStateQuarantineFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	previous := quarantineCorruptStateFile
	t.Cleanup(func() { quarantineCorruptStateFile = previous })
	quarantineCorruptStateFile = func(path string, cause error) error {
		return os.ErrPermission
	}

	path := filepath.Join(home, ".axis", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	loaded, err := Load()
	if err == nil {
		t.Fatal("expected hard error when quarantine fails")
	}
	if loaded != nil {
		t.Fatalf("expected nil state on hard error, got %+v", loaded)
	}
}

// --- Migration Tests ---

func TestTombstoneMigrationRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	now := time.Now().UTC()
	// Manually construct JSON to simulate legacy state
	jsonStr := fmt.Sprintf(`{
	  "nodes": {},
	  "tombstones": {
	    "test-pattern@node1": {
	      "task_pattern": "test-pattern",
	      "node_name": "node1",
	      "fail_count": 2,
	      "last_failure": %q,
	      "expires_at": %q
	    }
	  }
	}`, now.Format(time.RFC3339Nano), now.Add(48*time.Hour).Format(time.RFC3339Nano))

	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(jsonStr), 0644); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.Failures == nil {
		t.Fatal("expected Failures map to be initialized")
	}

	// Verify migration happened
	found := false
	for _, rec := range loaded.Failures {
		if rec.Scope.Node == "node1" && rec.Scope.Tool == "test-pattern" && rec.Count == 2 {
			found = true
		}
	}
	if !found {
		t.Fatal("expected legacy tombstone to be migrated to FailureRecord")
	}

	// Saving should no longer write 'tombstones'
	if err := loaded.Save(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"tombstones"`) {
		t.Fatal("saved state should not contain legacy tombstones key")
	}
}
