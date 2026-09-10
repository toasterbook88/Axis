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

	tc := NewToolContext(&RuntimeView{}, nil)
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

func TestDiffFragmentChangedFile(t *testing.T) {
	old := "alpha\nbeta\ngamma"
	new := "alpha\ndelta\ngamma"
	got := diffFragment(old, new)
	if !strings.Contains(got, "1 removed, 1 added") {
		t.Fatalf("net counts missing: %q", got)
	}
	if !strings.Contains(got, "- beta") || !strings.Contains(got, "+ delta") {
		t.Fatalf("first changed lines missing: %q", got)
	}
	if strings.Contains(got, "alpha") {
		t.Fatalf("unchanged prefix leaked into the fragment: %q", got)
	}
}

func TestDiffFragmentNewFile(t *testing.T) {
	got := diffFragment("", "line1\nline2\nline3")
	if !strings.Contains(got, "0 removed, 3 added") || !strings.Contains(got, "+ line1") {
		t.Fatalf("new-file fragment wrong: %q", got)
	}
}

func TestDiffFragmentIdenticalIsEmpty(t *testing.T) {
	if got := diffFragment("same", "same\n"); got != "" {
		t.Fatalf("no-op write produced a fragment: %q", got)
	}
}

func TestWriteFileResultCarriesDiffFragment(t *testing.T) {
	r := NewToolRegistry(nil)

	// write_file restricts paths to the working directory, so the test runs
	// inside its own temp dir and uses a relative path.
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	path := "notes.txt"
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := r.Execute(context.Background(), "write_file", []byte(`{"path":"notes.txt","content":"alpha\ndelta\ngamma"}`))
	if err != nil {
		t.Fatalf("write_file failed: %v", err)
	}
	if !strings.Contains(result, "- beta") || !strings.Contains(result, "+ delta") {
		t.Fatalf("result missing diff fragment:\n%s", result)
	}
}

func TestEditFileResultCarriesDiffFragment(t *testing.T) {
	r := NewToolRegistry(nil)

	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	path := "notes.txt"
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := r.Execute(context.Background(), "edit_file", []byte(`{"path":"notes.txt","target_content":"beta","replacement_content":"delta"}`))
	if err != nil {
		t.Fatalf("edit_file failed: %v", err)
	}
	if !strings.Contains(result, "- beta") || !strings.Contains(result, "+ delta") {
		t.Fatalf("result missing diff fragment:\n%s", result)
	}
}
