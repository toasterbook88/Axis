package api

import (
	"os"
	"testing"

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

	code := m.Run()

	knowledge.GetGitRepoState = prevGit
	os.Exit(code)
}
