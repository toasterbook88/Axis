package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLoadReturnsEmptyStoreWhenMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if s == nil {
		t.Fatal("expected store")
	}
	if len(s.Skills) != 0 || len(s.Failures) != 0 {
		t.Fatalf("expected empty store, got %+v", s)
	}
}

func TestLoadReturnsEmptyStoreOnInvalidJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".axis")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills.json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("write invalid json: %v", err)
	}

	s, err := Load()
	if err == nil {
		t.Fatal("expected recoverable warning on invalid json")
	}
	if len(s.Skills) != 0 || len(s.Failures) != 0 {
		t.Fatalf("expected empty store on invalid json, got %+v", s)
	}
	matches, globErr := filepath.Glob(filepath.Join(dir, "skills.json.corrupt-*"))
	if globErr != nil {
		t.Fatalf("Glob() error = %v", globErr)
	}
	if len(matches) != 1 {
		t.Fatalf("expected one quarantined backup, got %v", matches)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "skills.json")); !os.IsNotExist(statErr) {
		t.Fatalf("expected original skills.json to be quarantined, stat err = %v", statErr)
	}
}

func TestLoadFailsWhenQuarantineFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	previous := quarantineCorruptSkillsFile
	t.Cleanup(func() { quarantineCorruptSkillsFile = previous })
	quarantineCorruptSkillsFile = func(path string, cause error) error {
		return os.ErrPermission
	}

	dir := filepath.Join(os.Getenv("HOME"), ".axis")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills.json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("write invalid json: %v", err)
	}

	s, err := Load()
	if err == nil {
		t.Fatal("expected hard error when quarantine fails")
	}
	if s != nil {
		t.Fatalf("expected nil store on hard error, got %+v", s)
	}
}

func TestSaveCreatesParentDirectoryAndRoundTrips(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	s := newStore()
	s.RecordSuccess("git status", "git status --short", "node-a")
	s.RecordFailure("bad command", "failed")
	if err := s.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got.Skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(got.Skills))
	}
	if len(got.Failures) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(got.Failures))
	}
}

func TestRecordSuccessAggregatesExistingSkill(t *testing.T) {
	s := newStore()

	s.RecordSuccess("git status", "git status --short", "node-a")
	if s.Skills[0].PreferredNode != "node-a" {
		t.Fatalf("expected preferred node node-a, got %q", s.Skills[0].PreferredNode)
	}

	s.RecordSuccess("git status", "git status --short", "node-b")
	// Tie breaker: "node-a" < "node-b" alphabetically, so node-a remains preferred
	if s.Skills[0].PreferredNode != "node-a" {
		t.Fatalf("expected preferred node node-a under tie, got %q", s.Skills[0].PreferredNode)
	}

	s.RecordSuccess("git status", "git status --short", "node-b")
	if s.Skills[0].PreferredNode != "node-b" {
		t.Fatalf("expected preferred node to update to node-b, got %q", s.Skills[0].PreferredNode)
	}

	if len(s.Skills) != 1 {
		t.Fatalf("expected one learned skill, got %d", len(s.Skills))
	}
	if s.Skills[0].SuccessCount != 3 {
		t.Fatalf("expected success count 3, got %d", s.Skills[0].SuccessCount)
	}
	if s.Skills[0].NodeCount["node-a"] != 1 || s.Skills[0].NodeCount["node-b"] != 2 {
		t.Fatalf("expected node counts to be tracked, got %+v", s.Skills[0].NodeCount)
	}
}

func TestBestMatchPrefersMatchingSkill(t *testing.T) {
	s := newStore()
	s.RecordSuccess("git status", "git status --short", "node-a")
	s.RecordSuccess("build project", "go build ./...", "node-b")

	got, ok := s.BestMatch("check git status")
	if !ok {
		t.Fatal("expected best match")
	}
	if got.Description != "git status" {
		t.Fatalf("expected git status, got %q", got.Description)
	}
}

func TestIsKnownBadMatchesCaseInsensitiveDescription(t *testing.T) {
	s := newStore()
	s.RecordFailure("Bad Command", "failed")

	if !s.IsKnownBad("bad command") {
		t.Fatal("expected known-bad match")
	}
}

func TestSkillsPruneNodesRemovesEvidenceNotJustReferences(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s := &Store{
		Skills: []LearnedSkill{
			{
				ID:            "s1",
				Description:   "mixed evidence",
				SuccessCount:  4, // == 3 + 1, the RecordSuccess invariant
				PreferredNode: "ghost",
				NodeCount:     map[string]int{"ghost": 3, "node-a": 1},
			},
			{
				ID:            "s2",
				Description:   "test", // the shape of this host's real contamination
				SuccessCount:  5,
				PreferredNode: "ghost",
				NodeCount:     map[string]int{"ghost": 5},
			},
		},
	}

	rep := s.PruneNodes(map[string]bool{"ghost": true})

	if rep.SkillsDeleted() != 1 {
		t.Errorf("skill with no surviving evidence must be deleted, got %d", rep.SkillsDeleted())
	}
	if len(s.Skills) != 1 || s.Skills[0].ID != "s1" {
		t.Fatalf("expected only s1 to survive, got %+v", s.Skills)
	}

	surv := s.Skills[0]
	// Evidence is subtracted, not merely dereferenced.
	if surv.SuccessCount != 1 {
		t.Errorf("SuccessCount must drop by the pruned node counts: got %d, want 1", surv.SuccessCount)
	}
	if _, ok := surv.NodeCount["ghost"]; ok {
		t.Error("pruned node survived in NodeCount")
	}
	// Preference falls back to the surviving node.
	if surv.PreferredNode != "node-a" {
		t.Errorf("expected preferred to fall back to node-a, got %q", surv.PreferredNode)
	}
	// The invariant still holds after pruning.
	sum := 0
	for _, c := range surv.NodeCount {
		sum += c
	}
	if sum != surv.SuccessCount {
		t.Errorf("SuccessCount %d != sum(NodeCount) %d", surv.SuccessCount, sum)
	}
}

func TestSkillsPruneNodesSparesUntargetedAutoDiscoveredTemplates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// AutoDiscoverSkills shape: SuccessCount 0, nil NodeCount, PreferredNode
	// set to the node the tool was discovered on. A zero-evidence deletion
	// rule would destroy both of these on any prune.
	s := &Store{
		Skills: []LearnedSkill{
			{
				ID:            "auto-jq-20260724",
				Description:   "use jq (auto-discovered on node-a)",
				SuccessCount:  0,
				PreferredNode: "node-a",
			},
			{
				ID:            "auto-uv-20260724",
				Description:   "use uv (auto-discovered on ghost)",
				SuccessCount:  0,
				PreferredNode: "ghost",
			},
		},
	}

	rep := s.PruneNodes(map[string]bool{"ghost": true})

	if len(s.Skills) != 1 {
		t.Fatalf("expected exactly the node-a template to survive, got %+v", s.Skills)
	}
	if s.Skills[0].ID != "auto-jq-20260724" {
		t.Errorf("wrong template survived: %q", s.Skills[0].ID)
	}
	if s.Skills[0].PreferredNode != "node-a" {
		t.Errorf("untargeted template was modified: %q", s.Skills[0].PreferredNode)
	}
	if rep.SkillsDeleted() != 1 || rep.AutoTemplatesDeleted() != 1 {
		t.Errorf("expected 1 auto template deleted, got %+v", rep)
	}
}

func TestSkillsPruneNodesLeavesUntouchedSkillsAlone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// No reference to the pruned node anywhere: the skill must be bit-identical
	// afterwards, and must not count toward the report.
	s := &Store{
		Skills: []LearnedSkill{{
			ID: "s1", Description: "unrelated",
			SuccessCount: 3, PreferredNode: "node-a",
			NodeCount: map[string]int{"node-a": 3},
		}},
	}
	before := s.Skills[0]

	rep := s.PruneNodes(map[string]bool{"ghost": true})

	if !rep.Empty() {
		t.Errorf("prune touching nothing must report nothing, got %+v", rep)
	}
	if len(s.Skills) != 1 || s.Skills[0].SuccessCount != before.SuccessCount ||
		s.Skills[0].PreferredNode != before.PreferredNode {
		t.Errorf("untouched skill was modified: %+v", s.Skills)
	}
}

func TestSkillsPruneNodesKeepsSkillWithUnattributedEvidence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// A hand-edited or migrated store may carry SuccessCount that exceeds
	// the sum of NodeCount. That excess is not attributable to any node and
	// must not be destroyed, so the skill survives.
	s := &Store{
		Skills: []LearnedSkill{{
			ID: "s1", Description: "partly unattributed",
			SuccessCount: 9,
			NodeCount:    map[string]int{"ghost": 2},
		}},
	}

	s.PruneNodes(map[string]bool{"ghost": true})

	if len(s.Skills) != 1 {
		t.Fatal("skill with unattributed evidence must survive")
	}
	if s.Skills[0].SuccessCount != 7 {
		t.Errorf("SuccessCount should drop to 7, got %d", s.Skills[0].SuccessCount)
	}
}

func TestSkillsUpdateReloadsFromDiskInsideLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// A stale in-memory store must not be able to clobber newer disk state:
	// this is the guarded.go:381 nil-store truncation path.
	seed := &Store{Skills: []LearnedSkill{{
		ID: "existing", Description: "already learned",
		SuccessCount: 5, NodeCount: map[string]int{"node-a": 5},
	}}}
	if err := seed.Save(); err != nil {
		t.Fatal(err)
	}

	if err := Update(func(s *Store) error {
		s.RecordSuccess("new thing", "echo hi", "node-a")
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Skills) != 2 {
		t.Fatalf("Update must build on the on-disk store, got %d skills: %+v",
			len(got.Skills), got.Skills)
	}
}

func TestSkillsUpdateSerializesConcurrentWriters(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := (&Store{}).Save(); err != nil {
		t.Fatal(err)
	}

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- Update(func(s *Store) error {
				s.RecordSuccess(fmt.Sprintf("skill-%d", i), "cmd", "node-a")
				return nil
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// Without serialization, concurrent read-modify-write loses updates and
	// this count comes back below writers.
	if len(got.Skills) != writers {
		t.Errorf("lost concurrent updates: got %d skills, want %d", len(got.Skills), writers)
	}
}
