package main

import (
	"context"
	"fmt"
	"os"
	"strings"
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
	// A2A task delegate loads or generates tokens; stub globally so unit tests
	// do not mutate ~/.axis or fail in hermetic/read-only test environments.
	prevToken := loadAPIToken
	loadAPIToken = func() (string, error) {
		return "test-token", nil
	}
	// Restart counters come from the build host's systemd; stub globally so
	// doctor output does not depend on what happens to be looping there.
	prevLoops := doctorProbeRestartLoops
	doctorProbeRestartLoops = func(context.Context) ([]unitRestarts, error) {
		return nil, errOwnershipProbeUnsupported
	}

	// Restart paths detect the daemon's supervisor by reading the unit or
	// plist under HOME and asking systemctl/launchctl, which reach the real
	// user manager whatever HOME is. Run under a developer's HOME, a restart
	// test would restart their real daemon. Detect against an empty home and
	// refuse service-manager commands; tests that exercise a supervisor
	// install their own deps.
	serviceHome, err := os.MkdirTemp("", "axis-test-service-home-")
	if err != nil {
		panic(err)
	}
	prevServiceDeps := restartServiceDeps
	restartServiceDeps = inertServiceDeps(serviceHome)

	code := m.Run()

	doctorProbeRestartLoops = prevLoops
	restartServiceDeps = prevServiceDeps
	_ = os.RemoveAll(serviceHome)
	loadAPIToken = prevToken
	doctorProbeMLX = prevMLX
	doctorProbeLlamaServer = prevLlama
	doctorProbeOllama = prevOllama
	doctorProbeInstall = prevInstall
	doctorProbeRemoteShell = prevShell
	getGitRepoState = prevGit
	os.Exit(code)
}

// inertServiceDeps returns service-manager seams that find no supervisor and
// cannot run systemctl or launchctl.
func inertServiceDeps(home string) func() daemonServiceDependencies {
	return func() daemonServiceDependencies {
		deps := defaultDaemonServiceDependencies()
		deps.homeDir = func() (string, error) { return home, nil }
		deps.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
			return nil, fmt.Errorf("%s %s is disabled in tests", name, strings.Join(args, " "))
		}
		return deps
	}
}
