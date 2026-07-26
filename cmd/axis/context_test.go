package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

func TestContextPruneDefaultsToDryRunAndNamesNodes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st := &state.ClusterState{
		Nodes:       map[string]state.NodeState{"ghost": {}},
		TaskHistory: []state.TaskExecutionRecord{{Node: "ghost"}},
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	// apply=false is the default posture.
	if err := runContextPrune(&buf, []string{"ghost"}, false); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if !strings.Contains(out, "ghost") {
		t.Errorf("prune must name the nodes it would remove, got: %s", out)
	}
	if !strings.Contains(out, "dry run") {
		t.Errorf("expected dry-run notice, got: %s", out)
	}

	reloaded, err := state.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.TaskHistory) != 1 {
		t.Errorf("dry run modified state: %+v", reloaded.TaskHistory)
	}
}

func TestContextPruneApplyWritesBackupAndPrunesBothStores(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	st := &state.ClusterState{
		Nodes:       map[string]state.NodeState{"ghost": {}},
		TaskHistory: []state.TaskExecutionRecord{{Node: "ghost"}, {Node: "node-a"}},
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	sk := &skills.Store{Skills: []skills.LearnedSkill{{
		ID: "s1", Description: "x", SuccessCount: 2, PreferredNode: "ghost",
		NodeCount: map[string]int{"ghost": 2},
	}}}
	if err := sk.Save(); err != nil {
		t.Fatal(err)
	}
	skillsBefore, err := os.ReadFile(skills.Path())
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := runContextPrune(&buf, []string{"ghost"}, true); err != nil {
		t.Fatal(err)
	}

	reloaded, err := state.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.TaskHistory) != 1 || reloaded.TaskHistory[0].Node != "node-a" {
		t.Errorf("state not pruned: %+v", reloaded.TaskHistory)
	}

	reloadedSkills, err := skills.Load()
	if err != nil {
		t.Fatal(err)
	}
	// All of s1's evidence came from the pruned node, so the skill is gone.
	if len(reloadedSkills.Skills) != 0 {
		t.Errorf("skill backed only by pruned node survived: %+v", reloadedSkills.Skills)
	}

	matches, _ := filepath.Glob(filepath.Join(home, ".axis", "prune-backup-*"))
	if len(matches) != 1 {
		t.Fatalf("expected exactly one backup directory, got %v", matches)
	}
	// The backup must hold the PRE-prune contents, or it is not a backup.
	backedUp, err := os.ReadFile(filepath.Join(matches[0], "skills.json"))
	if err != nil {
		t.Fatalf("skills.json missing from backup: %v", err)
	}
	if !bytes.Equal(backedUp, skillsBefore) {
		t.Errorf("backup does not match pre-prune skills.json\n got: %s\nwant: %s", backedUp, skillsBefore)
	}
	if _, err := os.Stat(filepath.Join(matches[0], "state.json")); err != nil {
		t.Errorf("state.json missing from backup: %v", err)
	}
}

func TestContextPruneNamesDeletedSkillsWithDuplicateIDs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := (&state.ClusterState{
		Nodes: map[string]state.NodeState{"ghost": {}},
	}).Save(); err != nil {
		t.Fatal(err)
	}
	// Skill IDs are NOT unique: RecordSuccess derives them from a
	// one-second timestamp. Two skills sharing an ID, only one pruned.
	sk := &skills.Store{Skills: []skills.LearnedSkill{
		{ID: "20260724-173817", Description: "doomed", SuccessCount: 1,
			PreferredNode: "ghost", NodeCount: map[string]int{"ghost": 1}},
		{ID: "20260724-173817", Description: "survivor", SuccessCount: 1,
			PreferredNode: "node-a", NodeCount: map[string]int{"node-a": 1}},
	}}
	if err := sk.Save(); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := runContextPrune(&buf, []string{"ghost"}, false); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if !strings.Contains(out, `"doomed"`) {
		t.Errorf("deleted skill not named in output:\n%s", out)
	}
	if strings.Contains(out, `"survivor"`) {
		t.Errorf("surviving skill reported as deleted — ID collision mis-attributed:\n%s", out)
	}
}

// seedBothStores writes a state and a skills store that both reference
// "ghost", and returns their exact on-disk bytes.
func seedBothStores(t *testing.T) (stateBytes, skillsBytes []byte) {
	t.Helper()
	if err := (&state.ClusterState{
		Nodes:       map[string]state.NodeState{"ghost": {}},
		TaskHistory: []state.TaskExecutionRecord{{Node: "ghost"}, {Node: "node-a"}},
	}).Save(); err != nil {
		t.Fatal(err)
	}
	if err := (&skills.Store{Skills: []skills.LearnedSkill{{
		ID: "s1", Description: "x", SuccessCount: 1,
		PreferredNode: "ghost", NodeCount: map[string]int{"ghost": 1},
	}}}).Save(); err != nil {
		t.Fatal(err)
	}
	var err error
	if stateBytes, err = os.ReadFile(state.Path()); err != nil {
		t.Fatal(err)
	}
	if skillsBytes, err = os.ReadFile(skills.Path()); err != nil {
		t.Fatal(err)
	}
	return stateBytes, skillsBytes
}

// TestContextPruneRollsBackBothStoresOnWriteFailure covers BOTH write
// positions. WriteFileAtomic can rename successfully and then fail its
// directory sync, so a failing Save may already have changed the file — the
// simulated failures below therefore also mutate the store, and the
// transaction must restore every attempted write regardless.
func TestContextPruneRollsBackBothStoresOnWriteFailure(t *testing.T) {
	cases := []struct {
		name string
		fail string // "state" or "skills"
	}{
		{"state write fails after committing", "state"},
		{"skills write fails after committing", "skills"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			stateBefore, skillsBefore := seedBothStores(t)

			prevState, prevSkills := saveStateStore, saveSkillsStore
			t.Cleanup(func() { saveStateStore, saveSkillsStore = prevState, prevSkills })

			// Write-then-error: the store IS modified, and an error is
			// still returned. This is exactly WriteFileAtomic's behaviour
			// when the parent-directory fsync fails.
			if tc.fail == "state" {
				saveStateStore = func(s *state.ClusterState) error {
					_ = prevState(s)
					return errors.New("sync parent dir: disk failure")
				}
			} else {
				saveSkillsStore = func(s *skills.Store) error {
					_ = prevSkills(s)
					return errors.New("sync parent dir: disk failure")
				}
			}

			var buf bytes.Buffer
			err := runContextPrune(&buf, []string{"ghost"}, true)
			if err == nil {
				t.Fatal("expected an error when the write fails")
			}
			if !strings.Contains(err.Error(), "rolled back") {
				t.Errorf("error should report rollback, got: %v", err)
			}

			stateAfter, readErr := os.ReadFile(state.Path())
			if readErr != nil {
				t.Fatal(readErr)
			}
			skillsAfter, readErr := os.ReadFile(skills.Path())
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(stateBefore, stateAfter) {
				t.Errorf("state.json not rolled back:\nbefore %s\nafter  %s", stateBefore, stateAfter)
			}
			if !bytes.Equal(skillsBefore, skillsAfter) {
				t.Errorf("skills.json not rolled back:\nbefore %s\nafter  %s", skillsBefore, skillsAfter)
			}
		})
	}
}

func TestContextPruneLeavesUnaffectedStoresUntouched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Only skills references the pruned node. state.json does not exist at
	// all, and must not be created by a prune that has nothing to do there.
	if err := (&skills.Store{Skills: []skills.LearnedSkill{{
		ID: "s1", Description: "ghost only", SuccessCount: 1,
		PreferredNode: "ghost", NodeCount: map[string]int{"ghost": 1},
	}}}).Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state.Path()); !os.IsNotExist(err) {
		t.Fatalf("precondition: state.json should be absent, got %v", err)
	}

	var buf bytes.Buffer
	if err := runContextPrune(&buf, []string{"ghost"}, true); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(state.Path()); !os.IsNotExist(err) {
		t.Error("prune created state.json despite having nothing to prune there")
	}

	got, err := skills.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skills) != 0 {
		t.Errorf("skills not pruned: %+v", got.Skills)
	}
}

func TestContextPruneSerializesAgainstConcurrentSkillsWriter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := (&state.ClusterState{
		Nodes: map[string]state.NodeState{"ghost": {}},
	}).Save(); err != nil {
		t.Fatal(err)
	}
	if err := (&skills.Store{Skills: []skills.LearnedSkill{{
		ID: "doomed", Description: "ghost only", SuccessCount: 1,
		PreferredNode: "ghost", NodeCount: map[string]int{"ghost": 1},
	}}}).Save(); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 4; i++ {
			_ = skills.Update(func(s *skills.Store) error {
				s.RecordSuccess(fmt.Sprintf("concurrent-%d", i), "cmd", "node-a")
				return nil
			})
		}
	}()

	var buf bytes.Buffer
	pruneErr := runContextPrune(&buf, []string{"ghost"}, true)
	wg.Wait()
	if pruneErr != nil {
		t.Fatal(pruneErr)
	}

	got, err := skills.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Five atomic operations (four Updates + one prune) serialize under the
	// lock, so EVERY legal ordering yields the same result: the four
	// concurrent skills present, "doomed" gone. A count below four means an
	// Update was lost; five means the prune was clobbered.
	if len(got.Skills) != 4 {
		t.Errorf("lost an update or clobbered the prune: got %d skills, want 4: %+v",
			len(got.Skills), got.Skills)
	}
	for _, sk := range got.Skills {
		if sk.ID == "doomed" {
			t.Error("pruned skill survived a concurrent write")
		}
		if sk.PreferredNode == "ghost" || sk.NodeCount["ghost"] > 0 {
			t.Errorf("pruned node reference survived: %+v", sk)
		}
	}
}

func TestContextPruneBackupsDoNotCollide(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	st := &state.ClusterState{
		Nodes:       map[string]state.NodeState{"ghost": {}, "ghost2": {}},
		TaskHistory: []state.TaskExecutionRecord{{Node: "ghost"}, {Node: "ghost2"}},
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}

	// Two applies inside the same wall-clock second must not overwrite
	// each other's backup.
	for _, n := range []string{"ghost", "ghost2"} {
		var buf bytes.Buffer
		if err := runContextPrune(&buf, []string{n}, true); err != nil {
			t.Fatal(err)
		}
	}

	matches, _ := filepath.Glob(filepath.Join(home, ".axis", "prune-backup-*"))
	if len(matches) != 2 {
		t.Errorf("expected 2 distinct backup directories, got %v", matches)
	}
}

func TestContextPruneMigratesLegacyStateBeforeSaving(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Raw version-0 JSON on purpose: ClusterState.Save cannot build this
	// fixture, because saveTo stamps the current version unconditionally --
	// which is exactly the defect under test. A prune that reads unmigrated
	// state via LoadUnlocked and then saves would mark the file current while
	// leaving the surviving legacy tombstone unconverted, and every later
	// Load would skip it, stranding the record permanently.
	raw := `{
  "nodes": {},
  "tombstones": {
    "k-ghost":  {"task_pattern": "run ollama", "node_name": "ghost",  "fail_count": 2, "last_failure": "2026-07-01T00:00:00Z", "expires_at": "2126-07-01T00:00:00Z"},
    "k-node-a": {"task_pattern": "build repo", "node_name": "node-a", "fail_count": 3, "last_failure": "2026-07-02T00:00:00Z", "expires_at": "2126-07-02T00:00:00Z"}
  },
  "recent_decisions": []
}`
	if err := os.MkdirAll(filepath.Join(home, ".axis"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.Path(), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(state.Path())
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := runContextPrune(&buf, []string{"ghost"}, true); err != nil {
		t.Fatal(err)
	}

	// LoadUnlocked, so the assertions see exactly what was written rather
	// than a migration applied on the way back in.
	got, err := state.LoadUnlocked()
	if err != nil {
		t.Fatal(err)
	}

	// 1 is state's currentStateVersion; bump this if the schema advances.
	if got.Version != 1 {
		t.Errorf("pruned state was not stamped with the current version: %d", got.Version)
	}
	if len(got.Tombstones) != 0 {
		t.Errorf("legacy tombstones survived the migration: %+v", got.Tombstones)
	}
	if len(got.Failures) != 1 {
		t.Fatalf("surviving tombstone not migrated into Failures: %+v", got.Failures)
	}
	for _, f := range got.Failures {
		if f.Scope.Node != "node-a" {
			t.Errorf("expected only node-a to survive, got %+v", f)
		}
	}

	// The backup must still hold the exact original bytes, migration and all.
	matches, _ := filepath.Glob(filepath.Join(home, ".axis", "prune-backup-*"))
	if len(matches) != 1 {
		t.Fatalf("expected one backup directory, got %v", matches)
	}
	backedUp, err := os.ReadFile(filepath.Join(matches[0], "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(backedUp, before) {
		t.Errorf("backup is not the pre-prune bytes\n got: %s\nwant: %s", backedUp, before)
	}
}

func TestContextPruneDryRunDoesNotPersistPendingMigration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	raw := `{
  "nodes": {},
  "tombstones": {
    "k-ghost": {"task_pattern": "run ollama", "node_name": "ghost", "fail_count": 2, "last_failure": "2026-07-01T00:00:00Z", "expires_at": "2126-07-01T00:00:00Z"}
  },
  "recent_decisions": []
}`
	if err := os.MkdirAll(filepath.Join(home, ".axis"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.Path(), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := runContextPrune(&buf, []string{"ghost"}, false); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(state.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(raw), after) {
		t.Errorf("dry run persisted a pending migration\n got: %s\nwant: %s", after, raw)
	}
}
