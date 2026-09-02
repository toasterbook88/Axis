package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

// healthyDoctorEnv stubs every doctor dependency to a passing state. Individual
// tests override the one dependency whose disposition they exercise, so a
// failure can only come from the behavior under test.
func healthyDoctorEnv(t *testing.T) {
	t.Helper()
	tmpFile := filepath.Join(t.TempDir(), "nodes.yaml")
	t.Cleanup(stubDoctorConfigPath(t, func() string { return tmpFile }))
	t.Cleanup(stubDoctorOwnershipHealthy(t))
	t.Cleanup(stubDoctorConfigLoader(t, func(string) (*config.Config, error) {
		return &config.Config{
			Nodes: []config.NodeConfig{{Name: "alpha", Hostname: "10.0.0.1", SSHUser: "axis"}},
		}, nil
	}))
	t.Cleanup(stubDoctorSSHChecker(t, func(context.Context, config.NodeConfig) error { return nil }))
	t.Cleanup(stubStatusCachedLoader(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{Nodes: []models.NodeFacts{{Name: "alpha"}}}, "", nil
	}))
}

func runDoctorForExit(t *testing.T, args ...string) (string, error) {
	t.Helper()
	stdout, _, err := captureProcessOutput(t, func() error {
		cmd := doctorCmd()
		cmd.SetArgs(args)
		return cmd.Execute()
	})
	return stripANSI(stdout), err
}

// A failed check must make the process fail, whether or not --strict is set.
// Previously runDoctor always returned nil, so automation could not distinguish
// a healthy cluster from a broken one.
func TestDoctorBareFailReturnsExitFour(t *testing.T) {
	healthyDoctorEnv(t)
	t.Cleanup(stubDoctorSSHChecker(t, func(context.Context, config.NodeConfig) error {
		return errors.New("unreachable")
	}))

	stdout, err := runDoctorForExit(t)
	if !strings.Contains(stdout, "Some checks failed") {
		t.Fatalf("expected failure summary in report, got %q", stdout)
	}
	if got := ExitCode(err); got != ExitErrCommandFail {
		t.Fatalf("bare doctor with a failed check: exit %d, want %d", got, ExitErrCommandFail)
	}
}

func TestDoctorStrictFailReturnsExitFour(t *testing.T) {
	healthyDoctorEnv(t)
	// --strict promotes an unreachable daemon cache from warn to fail.
	t.Cleanup(stubStatusCachedLoader(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return nil, "", errors.New("daemon unavailable")
	}))

	stdout, err := runDoctorForExit(t, "--strict")
	if !strings.Contains(stdout, "Some checks failed") {
		t.Fatalf("expected failure summary in report, got %q", stdout)
	}
	if got := ExitCode(err); got != ExitErrCommandFail {
		t.Fatalf("strict doctor with a failed check: exit %d, want %d", got, ExitErrCommandFail)
	}
}

// Warnings are advisory. Without --strict an unreachable daemon cache is a warn,
// and warnings must not fail automation.
func TestDoctorWarningOnlyReturnsZero(t *testing.T) {
	healthyDoctorEnv(t)
	t.Cleanup(stubStatusCachedLoader(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return nil, "", errors.New("daemon unavailable")
	}))

	stdout, err := runDoctorForExit(t)
	if !strings.Contains(stdout, "advisory warnings") {
		t.Fatalf("expected advisory summary, got %q", stdout)
	}
	if err != nil {
		t.Fatalf("warning-only doctor must exit 0, got error %v (exit %d)", err, ExitCode(err))
	}
}

// A missing config is a failed check. Without a TTY there is no remediation
// prompt, so the command must still report failure to the caller.
func TestDoctorMissingConfigNonTTYReturnsExitFour(t *testing.T) {
	healthyDoctorEnv(t)
	t.Cleanup(stubDoctorConfigLoader(t, func(string) (*config.Config, error) {
		return nil, errors.New("no such file")
	}))

	stdout, err := runDoctorForExit(t)
	if !strings.Contains(stdout, "Configuration File") {
		t.Fatalf("expected configuration check in report, got %q", stdout)
	}
	if got := ExitCode(err); got != ExitErrCommandFail {
		t.Fatalf("missing config without a TTY: exit %d, want %d", got, ExitErrCommandFail)
	}
}

func stubDoctorTerminal(t *testing.T, isTTY bool) {
	t.Helper()
	prevIn, prevOut := doctorStdinIsTerminal, doctorStdoutIsTerminal
	doctorStdinIsTerminal = func() bool { return isTTY }
	doctorStdoutIsTerminal = func() bool { return isTTY }
	t.Cleanup(func() { doctorStdinIsTerminal, doctorStdoutIsTerminal = prevIn, prevOut })
}

func stubDoctorWizard(t *testing.T, fn func(*cobra.Command) error) {
	t.Helper()
	prev := doctorRunInitWizard
	doctorRunInitWizard = fn
	t.Cleanup(func() { doctorRunInitWizard = prev })
}

func runDoctorWithStdin(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	stdout, _, err := captureProcessOutput(t, func() error {
		cmd := doctorCmd()
		cmd.SetArgs(args)
		cmd.SetIn(strings.NewReader(stdin))
		return cmd.Execute()
	})
	return stripANSI(stdout), err
}

// Declining the wizard leaves the configuration still missing, so the failed
// check must still surface as exit 4 rather than a silent success.
func TestDoctorMissingConfigTTYDeclineReturnsExitFour(t *testing.T) {
	healthyDoctorEnv(t)
	t.Cleanup(stubDoctorConfigLoader(t, func(string) (*config.Config, error) {
		return nil, errors.New("no such file")
	}))
	stubDoctorTerminal(t, true)
	stubDoctorWizard(t, func(*cobra.Command) error {
		t.Fatal("wizard must not run when the operator declines")
		return nil
	})

	stdout, err := runDoctorWithStdin(t, "n\n")
	if !strings.Contains(stdout, "setup wizard") {
		t.Fatalf("expected the remediation prompt, got %q", stdout)
	}
	if got := ExitCode(err); got != ExitErrCommandFail {
		t.Fatalf("declined remediation: exit %d, want %d", got, ExitErrCommandFail)
	}
}

// An accepted wizard owns the command result. Its success must not be
// overwritten by the failed-config disposition that prompted it.
func TestDoctorMissingConfigTTYAcceptPreservesWizardSuccess(t *testing.T) {
	healthyDoctorEnv(t)
	t.Cleanup(stubDoctorConfigLoader(t, func(string) (*config.Config, error) {
		return nil, errors.New("no such file")
	}))
	stubDoctorTerminal(t, true)
	ran := false
	stubDoctorWizard(t, func(*cobra.Command) error { ran = true; return nil })

	_, err := runDoctorWithStdin(t, "y\n")
	if !ran {
		t.Fatal("accepted remediation did not run the wizard")
	}
	if err != nil {
		t.Fatalf("successful wizard must exit 0, got %v (exit %d)", err, ExitCode(err))
	}
}

// An accepted wizard's error must reach the caller unchanged, not be replaced
// by a generic exit 4.
func TestDoctorMissingConfigTTYAcceptPreservesWizardError(t *testing.T) {
	healthyDoctorEnv(t)
	t.Cleanup(stubDoctorConfigLoader(t, func(string) (*config.Config, error) {
		return nil, errors.New("no such file")
	}))
	stubDoctorTerminal(t, true)
	wizardErr := errors.New("wizard could not write nodes.yaml")
	stubDoctorWizard(t, func(*cobra.Command) error { return wizardErr })

	_, err := runDoctorWithStdin(t, "y\n")
	if !errors.Is(err, wizardErr) {
		t.Fatalf("wizard error must be preserved, got %v", err)
	}
}
