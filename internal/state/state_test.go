package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestUpdatePreservesConcurrentTaskHistory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	const writers = 20
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errCh <- Update(func(st *ClusterState) error {
				st.RecordTaskExecution(TaskExecutionRecord{ExecID: fmt.Sprintf("exec-%d", i)})
				return nil
			})
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(loaded.TaskHistory) != writers {
		t.Fatalf("TaskHistory length = %d, want %d", len(loaded.TaskHistory), writers)
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

func TestPruneNodesRefusesExecutionState(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name string
		node NodeState
	}{
		{"reserved RAM", NodeState{ReservedMB: 1024}},
		{"active task count", NodeState{ActiveTasks: 1}},
		{"active execution", NodeState{ActiveExecs: []string{"exec-1"}}},
		{"reservation map", NodeState{ExecReservationsMB: map[string]int64{"exec-1": 1024}}},
		{"heartbeat map", NodeState{ExecHeartbeatAt: map[string]time.Time{"exec-1": now}}},
		{"owner PID map", NodeState{ExecOwnerPID: map[string]int{"exec-1": 123}}},
		{"owner surface map", NodeState{ExecOwnerSurface: map[string]string{"exec-1": "task-run"}}},
		{"owner label map", NodeState{ExecOwnerLabel: map[string]string{"exec-1": "build"}}},
		{"origin map", NodeState{ExecOrigin: map[string]models.ExecutionOrigin{"exec-1": {}}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &ClusterState{
				Nodes: map[string]NodeState{
					"ghost": tc.node,
					"old":   {},
				},
				TaskHistory: []TaskExecutionRecord{
					{Node: "ghost"},
					{Node: "old"},
				},
			}

			rep := s.PruneNodes(map[string]bool{"ghost": true, "old": true})

			if len(rep.Blocked) != 1 || rep.Blocked[0].Node != "ghost" {
				t.Fatalf("expected ghost to block the prune, got %+v", rep.Blocked)
			}
			if !rep.Empty() {
				t.Fatalf("blocked prune reported changes: %+v", rep)
			}
			if len(s.Nodes) != 2 || len(s.TaskHistory) != 2 {
				t.Fatalf("blocked prune partially mutated state: %+v", s)
			}
		})
	}
}

func TestMigratePendingToleratesNilFailureStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// A hand-built or partially decoded state may carry a nil Failures map.
	// runMigrations writes into it, so a nil map would panic.
	s := &ClusterState{
		Version:    0,
		Tombstones: map[string]TombstoneEntry{"k1": {NodeName: "node-a"}},
	}

	if !MigratePending(s) {
		t.Fatal("expected a pending migration to run")
	}
	if len(s.Failures) != 1 {
		t.Errorf("tombstone not migrated: %+v", s.Failures)
	}
}
