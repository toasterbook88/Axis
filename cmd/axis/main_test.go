package main

import (
	"os"
	"testing"

	"github.com/toasterbook88/axis/internal/git"
)

func TestMain(m *testing.M) {
	// Stub git status by default to avoid environment-specific test differences.
	prevGit := getGitRepoState
	getGitRepoState = func(dir string) (git.RepoState, error) {
		return git.RepoState{IsRepo: false}, nil
	}

	code := m.Run()

	getGitRepoState = prevGit
	os.Exit(code)
}
