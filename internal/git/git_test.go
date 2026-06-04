package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	if len(state.DirtyFiles) != 1 || !strings.Contains(state.DirtyFiles[0], "dummy.txt") {
		t.Fatalf("expected dirty file list to contain dummy.txt, got %v", state.DirtyFiles)
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
