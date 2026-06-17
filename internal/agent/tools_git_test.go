package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func setupTestGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH, skipping git tools tests")
	}

	tmpDir := t.TempDir()

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git: %v", err)
	}

	// Configure git email/name so commit works in CI
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	return tmpDir
}

func TestGitTools(t *testing.T) {
	tmpDir := setupTestGitRepo(t)
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	tc := &ToolContext{}
	r := NewToolRegistry(tc)

	// 1. Test git_status (Empty/Clean)
	status, err := r.Execute(context.Background(), "git_status", json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("unexpected git_status error: %v", err)
	}
	if !strings.Contains(status, "Branch:") {
		t.Errorf("expected branch info in git_status output, got %q", status)
	}

	// Create a file and commit it
	filePath := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(filePath, []byte("version 1\n"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// 2. Test git_status (Dirty)
	status, err = r.Execute(context.Background(), "git_status", json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("unexpected git_status error: %v", err)
	}
	if !strings.Contains(status, "Status: Dirty") {
		t.Errorf("expected dirty status, got %q", status)
	}

	// Commit the file
	cmd := exec.Command("git", "add", "test.txt")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git add failed: %v", err)
	}
	cmd = exec.Command("git", "commit", "-m", "first commit")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	// 3. Test git_log
	log, err := r.Execute(context.Background(), "git_log", json.RawMessage(`{"count": 5}`))
	if err != nil {
		t.Fatalf("unexpected git_log error: %v", err)
	}
	if !strings.Contains(log, "first commit") {
		t.Errorf("expected 'first commit' in git_log, got %q", log)
	}

	// Modify file to test git_diff
	if err := os.WriteFile(filePath, []byte("version 2\n"), 0644); err != nil {
		t.Fatalf("failed to modify test file: %v", err)
	}

	// 4. Test git_diff
	diff, err := r.Execute(context.Background(), "git_diff", json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("unexpected git_diff error: %v", err)
	}
	if !strings.Contains(diff, "version 1") || !strings.Contains(diff, "version 2") {
		t.Errorf("expected differences in git_diff output, got %q", diff)
	}
}
