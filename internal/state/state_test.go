package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
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
	Maintain(loaded)

	got, ok := loaded.Nodes["alpha"]
	if !ok {
		t.Fatal("expected alpha node to round-trip")
	}
	if got.ReservedMB != 2048 {
		t.Fatalf("ReservedMB = %d, want 2048", got.ReservedMB)
	}
	if got.ExecReservationsMB["exec-1"] != 2048 {
		t.Fatalf("ExecReservationsMB = %v, want exec-1=2048", got.ExecReservationsMB)
	}
	if got.ExecHeartbeatAt["exec-1"].IsZero() {
		t.Fatalf("ExecHeartbeatAt = %v, want non-zero heartbeat", got.ExecHeartbeatAt)
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
	Maintain(loaded)

	if _, ok := loaded.Nodes["expired"]; ok {
		t.Fatal("expected expired reservation to be removed")
	}
	if _, ok := loaded.Nodes["fresh"]; !ok {
		t.Fatal("expected fresh reservation to remain")
	}
}

func TestLoadReclaimsStaleActiveReservationsToPerExecCap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := filepath.Join(home, ".axis", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	payload := ClusterState{
		Nodes: map[string]NodeState{
			"alpha": {
				ReservedMB:   4096,
				LastPlacedAt: time.Now().Add(-46 * time.Minute).UTC(),
				ActiveTasks:  1,
				ActiveExecs:  []string{"exec-1"},
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
	Maintain(loaded)

	ns, ok := loaded.Nodes["alpha"]
	if !ok {
		t.Fatal("expected stale active reservation to remain after reclaim")
	}
	if ns.ReservedMB != 1024 {
		t.Fatalf("ReservedMB = %d, want 1024 after reclaim", ns.ReservedMB)
	}
	if ns.ActiveTasks != 1 || len(ns.ActiveExecs) != 1 {
		t.Fatalf("expected active execution metadata to survive reclaim, got %+v", ns)
	}
}

func TestLoadPreservesActiveReservationWithRecentHeartbeat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	prevAlive := execOwnerAlive
	execOwnerAlive = func(pid int) bool { return pid == 4242 }
	t.Cleanup(func() { execOwnerAlive = prevAlive })

	path := filepath.Join(home, ".axis", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	payload := ClusterState{
		Nodes: map[string]NodeState{
			"alpha": {
				ReservedMB:   4096,
				LastPlacedAt: time.Now().Add(-25 * time.Hour).UTC(),
				ActiveTasks:  1,
				ActiveExecs:  []string{"exec-1"},
				ExecReservationsMB: map[string]int64{
					"exec-1": 4096,
				},
				ExecHeartbeatAt: map[string]time.Time{
					"exec-1": time.Now().Add(-30 * time.Second).UTC(),
				},
				ExecOwnerPID: map[string]int{
					"exec-1": 4242,
				},
				ExecOwnerSurface: map[string]string{
					"exec-1": "task-run",
				},
				ExecOwnerLabel: map[string]string{
					"exec-1": "cli",
				},
				ExecOrigin: map[string]models.ExecutionOrigin{
					"exec-1": models.NewExecutionOrigin("local-node", "host.local", "abc-123"),
				},
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

	ns, ok := loaded.Nodes["alpha"]
	if !ok {
		t.Fatal("expected active heartbeat-backed reservation to remain")
	}
	if ns.ReservedMB != 4096 {
		t.Fatalf("ReservedMB = %d, want 4096", ns.ReservedMB)
	}
	if ns.ExecOwnerPID["exec-1"] != 4242 {
		t.Fatalf("ExecOwnerPID = %v, want exec-1=4242", ns.ExecOwnerPID)
	}
	if ns.ExecOwnerSurface["exec-1"] != "task-run" {
		t.Fatalf("ExecOwnerSurface = %v, want exec-1=task-run", ns.ExecOwnerSurface)
	}
	if ns.ExecOwnerLabel["exec-1"] != "cli" {
		t.Fatalf("ExecOwnerLabel = %v, want exec-1=cli", ns.ExecOwnerLabel)
	}
	if ns.ExecOrigin["exec-1"] != models.NewExecutionOrigin("local-node", "host.local", "abc-123") {
		t.Fatalf("ExecOrigin = %v, want exec-1 local origin", ns.ExecOrigin)
	}
}

func TestLoadDropsAncientActiveReservations(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := filepath.Join(home, ".axis", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	payload := ClusterState{
		Nodes: map[string]NodeState{
			"alpha": {
				ReservedMB:   2048,
				LastPlacedAt: time.Now().Add(-25 * time.Hour).UTC(),
				ActiveTasks:  1,
				ActiveExecs:  []string{"exec-1"},
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
	Maintain(loaded)

	if _, ok := loaded.Nodes["alpha"]; ok {
		t.Fatal("expected ancient active reservation to be dropped")
	}
}

func TestLoadReclaimsActiveReservationWithDeadOwnerPID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	prevAlive := execOwnerAlive
	execOwnerAlive = func(pid int) bool { return false }
	t.Cleanup(func() { execOwnerAlive = prevAlive })

	path := filepath.Join(home, ".axis", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	payload := ClusterState{
		Nodes: map[string]NodeState{
			"alpha": {
				ReservedMB:   2048,
				LastPlacedAt: time.Now().Add(-10 * time.Second).UTC(),
				ActiveTasks:  1,
				ActiveExecs:  []string{"exec-1"},
				ExecReservationsMB: map[string]int64{
					"exec-1": 2048,
				},
				ExecHeartbeatAt: map[string]time.Time{
					"exec-1": time.Now().UTC(),
				},
				ExecOwnerPID: map[string]int{
					"exec-1": 9999,
				},
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
	Maintain(loaded)
	if _, ok := loaded.Nodes["alpha"]; ok {
		t.Fatalf("expected dead-owner reservation to be reclaimed, got %+v", loaded.Nodes["alpha"])
	}
}

func TestSemanticFingerprintIgnoresHeartbeatOnlyChanges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	base := ClusterState{
		Nodes: map[string]NodeState{
			"alpha": {
				ReservedMB:   2048,
				LastTask:     "serve",
				LastPlacedAt: time.Unix(1_700_000_000, 0).UTC(),
				ActiveTasks:  1,
				ActiveExecs:  []string{"exec-1"},
				ExecReservationsMB: map[string]int64{
					"exec-1": 2048,
				},
				ExecHeartbeatAt: map[string]time.Time{
					"exec-1": time.Unix(1_700_000_010, 0).UTC(),
				},
			},
		},
		UpdatedAt: time.Unix(1_700_000_020, 0).UTC(),
	}
	data, err := json.Marshal(base)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	first, exists, err := SemanticFingerprint(path)
	if err != nil {
		t.Fatalf("SemanticFingerprint() error = %v", err)
	}
	if !exists {
		t.Fatal("expected semantic fingerprint file to exist")
	}

	base.Nodes["alpha"] = NodeState{
		ReservedMB:   2048,
		LastTask:     "serve",
		LastPlacedAt: time.Unix(1_700_000_000, 0).UTC(),
		ActiveTasks:  1,
		ActiveExecs:  []string{"exec-1"},
		ExecReservationsMB: map[string]int64{
			"exec-1": 2048,
		},
		ExecHeartbeatAt: map[string]time.Time{
			"exec-1": time.Unix(1_700_000_099, 0).UTC(),
		},
	}
	base.UpdatedAt = time.Unix(1_700_000_100, 0).UTC()
	data, err = json.Marshal(base)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	second, exists, err := SemanticFingerprint(path)
	if err != nil {
		t.Fatalf("SemanticFingerprint() second call error = %v", err)
	}
	if !exists {
		t.Fatal("expected semantic fingerprint file to still exist")
	}
	if first != second {
		t.Fatalf("expected heartbeat-only change to keep semantic fingerprint stable: %x != %x", first, second)
	}
}

func TestSemanticFingerprintChangesForReservationShape(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	writeState := func(reserved int64) [32]byte {
		t.Helper()
		payload := ClusterState{
			Nodes: map[string]NodeState{
				"alpha": {
					ReservedMB:   reserved,
					LastTask:     "serve",
					LastPlacedAt: time.Unix(1_700_000_000, 0).UTC(),
					ActiveTasks:  1,
					ActiveExecs:  []string{"exec-1"},
					ExecReservationsMB: map[string]int64{
						"exec-1": reserved,
					},
					ExecHeartbeatAt: map[string]time.Time{
						"exec-1": time.Unix(1_700_000_010, 0).UTC(),
					},
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
		sum, exists, err := SemanticFingerprint(path)
		if err != nil {
			t.Fatalf("SemanticFingerprint() error = %v", err)
		}
		if !exists {
			t.Fatal("expected semantic fingerprint file to exist")
		}
		return sum
	}

	first := writeState(2048)
	second := writeState(1024)
	if first == second {
		t.Fatalf("expected reservation-shape change to alter semantic fingerprint: %x", first)
	}
}

func TestSemanticFingerprintChangesForOwnerProvenance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	writeState := func(surface string) [32]byte {
		t.Helper()
		payload := ClusterState{
			Nodes: map[string]NodeState{
				"alpha": {
					ReservedMB:   2048,
					LastTask:     "serve",
					LastPlacedAt: time.Unix(1_700_000_000, 0).UTC(),
					ActiveTasks:  1,
					ActiveExecs:  []string{"exec-1"},
					ExecReservationsMB: map[string]int64{
						"exec-1": 2048,
					},
					ExecHeartbeatAt: map[string]time.Time{
						"exec-1": time.Unix(1_700_000_010, 0).UTC(),
					},
					ExecOwnerSurface: map[string]string{
						"exec-1": surface,
					},
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
		sum, exists, err := SemanticFingerprint(path)
		if err != nil {
			t.Fatalf("SemanticFingerprint() error = %v", err)
		}
		if !exists {
			t.Fatal("expected semantic fingerprint file to exist")
		}
		return sum
	}

	first := writeState("task-run")
	second := writeState("http-run")
	if first == second {
		t.Fatalf("expected owner-provenance change to alter semantic fingerprint: %x", first)
	}
}

func TestSemanticFingerprintChangesForExecutionOrigin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	writeState := func(origin models.ExecutionOrigin) [32]byte {
		t.Helper()
		payload := ClusterState{
			Nodes: map[string]NodeState{
				"alpha": {
					ReservedMB:   2048,
					LastTask:     "serve",
					LastPlacedAt: time.Unix(1_700_000_000, 0).UTC(),
					ActiveTasks:  1,
					ActiveExecs:  []string{"exec-1"},
					ExecReservationsMB: map[string]int64{
						"exec-1": 2048,
					},
					ExecHeartbeatAt: map[string]time.Time{
						"exec-1": time.Unix(1_700_000_010, 0).UTC(),
					},
					ExecOrigin: map[string]models.ExecutionOrigin{
						"exec-1": origin,
					},
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
		sum, exists, err := SemanticFingerprint(path)
		if err != nil {
			t.Fatalf("SemanticFingerprint() error = %v", err)
		}
		if !exists {
			t.Fatal("expected semantic fingerprint file to exist")
		}
		return sum
	}

	first := writeState(models.NewExecutionOrigin("node-a", "host-a.local", "abc-123"))
	second := writeState(models.NewExecutionOrigin("node-b", "host-b.local", "def-456"))
	if first == second {
		t.Fatalf("expected execution-origin change to alter semantic fingerprint: %x", first)
	}
}

func TestLoadReclaimsExecutionsWithStaleHeartbeats(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := filepath.Join(home, ".axis", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	payload := ClusterState{
		Nodes: map[string]NodeState{
			"alpha": {
				ReservedMB:   4096,
				LastPlacedAt: time.Now().UTC(),
				ActiveTasks:  2,
				ActiveExecs:  []string{"stale", "live"},
				ExecReservationsMB: map[string]int64{
					"stale": 3072,
					"live":  1024,
				},
				ExecHeartbeatAt: map[string]time.Time{
					"stale": time.Now().Add(-3 * time.Minute).UTC(),
					"live":  time.Now().Add(-30 * time.Second).UTC(),
				},
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
	Maintain(loaded)

	ns, ok := loaded.Nodes["alpha"]
	if !ok {
		t.Fatal("expected live execution to remain")
	}
	if ns.ReservedMB != 1024 {
		t.Fatalf("ReservedMB = %d, want 1024 after stale heartbeat reclaim", ns.ReservedMB)
	}
	if len(ns.ActiveExecs) != 1 || ns.ActiveExecs[0] != "live" {
		t.Fatalf("ActiveExecs = %v, want [live]", ns.ActiveExecs)
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
	Maintain(loaded)
	if len(loaded.Nodes) != 0 {
		t.Fatalf("expected legacy reservations to be cleared, got %v", loaded.Nodes)
	}

	_ = loaded.Save()

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

func TestRecordPlacementCapsDecisionHistory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	s := &ClusterState{}

	for i := 0; i < 25; i++ {
		s.RecordPlacement("alpha", 100, fmt.Sprintf("task-%d", i))
	}

	if len(s.Decisions) != 20 {
		t.Fatalf("len(Decisions) = %d, want 20", len(s.Decisions))
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
	Maintain(loaded)

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

	// Verify migration happened: legacy tombstone becomes a {Node}-scoped record
	// so placement queries on {Node, Workload} can still match it.
	found := false
	for _, rec := range loaded.Failures {
		if rec.Scope.Node == "node1" && rec.Scope.Tool == "" && rec.Count == 2 {
			found = true
		}
	}
	if !found {
		t.Fatal("expected legacy tombstone to be migrated to a {Node}-scoped FailureRecord")
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
