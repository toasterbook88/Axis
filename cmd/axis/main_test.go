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
	// Local AI backend probes run real discovery scripts; a probe that errors
	// (timeout, killed binary) downgrades doctor to an advisory warn. Stub
	// globally so doctor output and test timing do not depend on the host's
	// inference installs. Probe-state tests override per test.
	prevOllama := doctorProbeOllama
	prevLlama := doctorProbeLlamaServer
	prevMLX := doctorProbeMLX
	notInstalled := func(context.Context) doctorBackendStatus { return doctorBackendStatus{Installed: false} }
	doctorProbeOllama = notInstalled
	doctorProbeLlamaServer = notInstalled
	doctorProbeMLX = notInstalled

	code := m.Run()

	doctorProbeMLX = prevMLX
	doctorProbeLlamaServer = prevLlama
	doctorProbeOllama = prevOllama
	doctorProbeInstall = prevInstall
	doctorProbeRemoteShell = prevShell
	getGitRepoState = prevGit
	os.Exit(code)
}
