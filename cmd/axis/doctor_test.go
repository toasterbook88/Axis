package main

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestDoctorUsesAuthenticatedSSHCheck(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "nodes.yaml")
	restorePath := stubDoctorConfigPath(t, func() string {
		return tmpFile
	})
	defer restorePath()

	restoreLoad := stubDoctorConfigLoader(t, func(string) (*config.Config, error) {
		return &config.Config{
			Nodes: []config.NodeConfig{
				{Name: "alpha", Hostname: "alpha.local", SSHUser: "axis"},
				{Name: "beta", Hostname: "beta.local", SSHUser: "axis", SSHPort: 2222, TimeoutSec: 25},
			},
		}, nil
	})
	defer restoreLoad()

	var seen []config.NodeConfig
	restoreSSH := stubDoctorSSHChecker(t, func(_ context.Context, node config.NodeConfig) error {
		seen = append(seen, node)
		if node.Name == "beta" {
			return errors.New("ssh handshake beta.local:2222: ssh: handshake failed: knownhosts: key mismatch")
		}
		return nil
	})
	defer restoreSSH()

	restoreCache := stubStatusCachedLoader(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return nil, "", errors.New("daemon unavailable")
	})
	defer restoreCache()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := doctorCmd()
		cmd.SetArgs(nil)
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("doctor Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	stdout = stripANSI(stdout)

	if len(seen) != 2 {
		t.Fatalf("checked %d nodes, want 2", len(seen))
	}
	if seen[1].EffectiveSSHPort() != 2222 {
		t.Fatalf("beta port = %d, want 2222", seen[1].EffectiveSSHPort())
	}
	if seen[1].EffectiveTimeout() != 25 {
		t.Fatalf("beta timeout = %d, want 25", seen[1].EffectiveTimeout())
	}
	if !strings.Contains(stdout, "alpha (alpha.local:22)") {
		t.Fatalf("expected alpha success in output, got %q", stdout)
	}
	if !strings.Contains(stdout, "beta (beta.local:2222)") {
		t.Fatalf("expected beta failure header in output, got %q", stdout)
	}
	if !strings.Contains(stdout, "knownhosts") {
		t.Fatalf("expected knownhosts failure detail in output, got %q", stdout)
	}
	if !strings.Contains(stdout, "key mismatch") {
		t.Fatalf("expected key mismatch detail in output, got %q", stdout)
	}
	if !strings.Contains(stdout, "Some checks failed") {
		t.Fatalf("expected doctor warning summary, got %q", stdout)
	}
}

func TestDoctorReportsHealthySSHAndDaemon(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "nodes.yaml")
	restorePath := stubDoctorConfigPath(t, func() string {
		return tmpFile
	})
	defer restorePath()

	restoreLoad := stubDoctorConfigLoader(t, func(string) (*config.Config, error) {
		return &config.Config{
			Nodes: []config.NodeConfig{
				{Name: "alpha", Hostname: "alpha.local", SSHUser: "axis"},
			},
		}, nil
	})
	defer restoreLoad()

	restoreSSH := stubDoctorSSHChecker(t, func(context.Context, config.NodeConfig) error {
		return nil
	})
	defer restoreSSH()

	restoreCache := stubStatusCachedLoader(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{{Name: "alpha"}},
		}, "cached", nil
	})
	defer restoreCache()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := doctorCmd()
		cmd.SetArgs(nil)
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("doctor Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	stdout = stripANSI(stdout)
	if !strings.Contains(stdout, "alpha (alpha.local:22)") {
		t.Fatalf("expected SSH success in output, got %q", stdout)
	}
	if !strings.Contains(stdout, "Reachable, 1 node(s) cached") {
		t.Fatalf("expected daemon success in output, got %q", stdout)
	}
	if !strings.Contains(stdout, "All checks passed") {
		t.Fatalf("expected success summary in output, got %q", stdout)
	}
}

func stubDoctorConfigPath(t *testing.T, fn func() string) func() {
	t.Helper()
	prev := doctorConfigPath
	doctorConfigPath = fn
	return func() {
		doctorConfigPath = prev
	}
}

func stubDoctorConfigLoader(t *testing.T, fn func(string) (*config.Config, error)) func() {
	t.Helper()
	prev := loadDoctorConfig
	loadDoctorConfig = fn
	return func() {
		loadDoctorConfig = prev
	}
}

func stubDoctorSSHChecker(t *testing.T, fn func(context.Context, config.NodeConfig) error) func() {
	t.Helper()
	prev := doctorCheckNodeSSH
	doctorCheckNodeSSH = fn
	return func() {
		doctorCheckNodeSSH = prev
	}
}

func stripANSI(s string) string {
	return ansiEscapePattern.ReplaceAllString(s, "")
}
