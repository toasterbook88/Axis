package api

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/events"
	"github.com/toasterbook88/axis/internal/git"
	"github.com/toasterbook88/axis/internal/knowledge"
)

func TestMain(m *testing.M) {
	// Stub knowledge.GetGitRepoState to return IsRepo: false by default.
	// This ensures that golden files and JSON outputs in tests do not
	// depend on the host machine's git repository details.
	prevGit := knowledge.GetGitRepoState
	knowledge.GetGitRepoState = func(dir string) (git.RepoState, error) {
		return git.RepoState{IsRepo: false}, nil
	}

	// Redirect the asynchronous event log away from per-test HOME/.axis dirs.
	// Writing event logs into t.TempDir() races with Go's TempDir cleanup and
	// causes flaky "directory not empty" failures.
	eventLogDir, err := os.MkdirTemp("", "axis-api-events-*")
	if err != nil {
		panic(fmt.Sprintf("failed to create temp directory: %v", err))
	}
	if err := events.ResetTestLog(filepath.Join(eventLogDir, "events.jsonl")); err != nil {
		panic(fmt.Sprintf("ResetTestLog: %v", err))
	}

	code := m.Run()

	if err := events.FlushEvents(5 * time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "warning: FlushEvents after api tests: %v\n", err)
	}
	_ = os.RemoveAll(eventLogDir)

	knowledge.GetGitRepoState = prevGit
	os.Exit(code)
}
