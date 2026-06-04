package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGetRepoState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH, skipping test")
	}

	tmpDir, err := os.MkdirTemp("", "axis-git-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Initial check on non-repo dir
	state, err := GetRepoState(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoState failed: %v", err)
	}
	if state.IsRepo {
		t.Fatal("expected IsRepo to be false for new temp dir")
	}

	// Initialize git repo
	cmd := exec.CommandContext(ctx, "git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git init: %v", err)
	}

	// Configure git dummy user
	cmd = exec.CommandContext(ctx, "git", "config", "user.name", "Test User")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.CommandContext(ctx, "git", "config", "user.email", "test@example.com")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	// Check on empty repo
	state, err = GetRepoState(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoState failed: %v", err)
	}
	if !state.IsRepo {
		t.Fatal("expected IsRepo to be true after init")
	}

	// Create a dummy file
	testFile := filepath.Join(tmpDir, "dummy.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0644); err != nil {
		t.Fatalf("failed to write dummy file: %v", err)
	}

	// Check dirty status
	state, err = GetRepoState(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoState failed: %v", err)
	}
	if !state.IsDirty {
		t.Fatal("expected IsDirty to be true after writing file")
	}
	if state.DirtyCount != 1 {
		t.Fatalf("expected DirtyCount to be 1, got %d", state.DirtyCount)
	}
	if len(state.DirtyFiles) != 1 || !strings.Contains(state.DirtyFiles[0], "dummy.txt") {
		t.Fatalf("expected dirty file list to contain dummy.txt, got %v", state.DirtyFiles)
	}

	// Create 11 more files (total 12 dirty files) to verify truncation to 10
	for i := 1; i <= 11; i++ {
		f := filepath.Join(tmpDir, filepath.Clean(filepath.Join("/", filepath.Base(filepath.Join("dummy", strconv.Itoa(i)+".txt")))))
		if err := os.WriteFile(f, []byte("hello"), 0644); err != nil {
			t.Fatalf("failed to write dummy file %d: %v", i, err)
		}
	}

	state, err = GetRepoState(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoState failed: %v", err)
	}
	if state.DirtyCount != 12 {
		t.Fatalf("expected DirtyCount to be 12, got %d", state.DirtyCount)
	}
	if len(state.DirtyFiles) != 10 {
		t.Fatalf("expected DirtyFiles to be truncated to 10, got %d (files: %v)", len(state.DirtyFiles), state.DirtyFiles)
	}

	// Cleanup the extra 11 files to keep test clean
	for i := 1; i <= 11; i++ {
		_ = os.Remove(filepath.Join(tmpDir, filepath.Clean(filepath.Join("/", filepath.Base(filepath.Join("dummy", strconv.Itoa(i)+".txt"))))))
	}

	// Add and commit
	cmd = exec.CommandContext(ctx, "git", "add", "dummy.txt")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git add: %v", err)
	}

	cmd = exec.CommandContext(ctx, "git", "commit", "-m", "initial commit")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git commit: %v", err)
	}

	// Check clean status
	state, err = GetRepoState(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoState failed: %v", err)
	}
	if state.IsDirty {
		t.Fatal("expected IsDirty to be false after commit")
	}
	if state.Commit == "" {
		t.Fatal("expected Commit to be populated")
	}
	if state.Subject != "initial commit" {
		t.Fatalf("expected Subject to be 'initial commit', got %q", state.Subject)
	}
}
