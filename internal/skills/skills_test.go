package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReturnsEmptyStoreWhenMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s := Load()
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

	s := Load()
	if len(s.Skills) != 0 || len(s.Failures) != 0 {
		t.Fatalf("expected empty store on invalid json, got %+v", s)
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

	got := Load()
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
	s.RecordSuccess("git status", "git status --short", "node-b")

	if len(s.Skills) != 1 {
		t.Fatalf("expected one learned skill, got %d", len(s.Skills))
	}
	if s.Skills[0].SuccessCount != 2 {
		t.Fatalf("expected success count 2, got %d", s.Skills[0].SuccessCount)
	}
	if s.Skills[0].NodeCount["node-a"] != 1 || s.Skills[0].NodeCount["node-b"] != 1 {
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
