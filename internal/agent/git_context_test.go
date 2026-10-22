package agent

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// Editing one file or committing between runs changed the git block, which
// sat in the system prompt, so a local prompt cache could not reuse it. The
// block now goes with the user message.
func TestGitContextStaysOutOfSystemPrompt(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "a.txt")
	gitIn(t, repo, "commit", "-q", "-m", "first")
	t.Chdir(repo)

	run := func() (system []chat.Message, latestUser string) {
		backend := &hookBackend{}
		a := New(Config{Backend: backend, MaxTurns: 1, MaxTokens: 100000, Output: io.Discard})
		if err := a.Run(context.Background(), "show nodes"); err != nil {
			t.Fatalf("Run: %v", err)
		}
		for _, m := range backend.calls[0] {
			switch m.Role {
			case chat.RoleSystem:
				system = append(system, m)
			case chat.RoleUser:
				latestUser = m.Content
			}
		}
		return system, latestUser
	}

	cleanSystem, cleanUser := run()
	if !strings.Contains(cleanUser, "Git repository context (at session start)") || !strings.Contains(cleanUser, "- Status: Clean") {
		t.Fatalf("git context missing from the user message: %q", cleanUser)
	}

	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirtySystem, dirtyUser := run()
	if !strings.Contains(dirtyUser, "Dirty (1 files changed)") || !strings.HasSuffix(dirtyUser, "show nodes") {
		t.Fatalf("dirty state missing from the user message: %q", dirtyUser)
	}
	if !reflect.DeepEqual(cleanSystem, dirtySystem) {
		t.Fatal("system prompt changed when one file was edited")
	}
	for _, m := range cleanSystem {
		if strings.Contains(m.Content, "Git repository context") || strings.Contains(m.Content, "Git Repository Context") {
			t.Fatal("git context is still in the system prompt")
		}
	}
}
