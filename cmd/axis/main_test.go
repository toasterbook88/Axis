package main

import (
	"context"
	"os"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/git"
)

func TestMain(m *testing.M) {
	// Stub git status by default to avoid environment-specific test differences.
	prevGit := getGitRepoState
	getGitRepoState = func(dir string) (git.RepoState, error) {
		return git.RepoState{IsRepo: false}, nil
	}
	// Doctor mesh shell probe uses real SSH; stub globally for unit tests.
	prevShell := doctorProbeRemoteShell
	doctorProbeRemoteShell = func(context.Context, config.NodeConfig) (string, bool) {
		return "", false
	}
	// Install hygiene enumerates axis binaries on the build host; stub globally
	// so doctor output does not depend on the developer's own installs.
	prevInstall := doctorProbeInstall
	doctorProbeInstall = func() DoctorCheck {
		return DoctorCheck{Name: "AXIS Install", Status: "pass", Message: "single system-wide install (stubbed)"}
	}

	code := m.Run()

	doctorProbeInstall = prevInstall
	doctorProbeRemoteShell = prevShell
	getGitRepoState = prevGit
	os.Exit(code)
}
