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

func TestDoctorTreatsDaemonFailureAsAdvisoryByDefault(t *testing.T) {
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
	if !strings.Contains(stdout, "Core checks passed with advisory warnings") {
		t.Fatalf("expected advisory summary, got %q", stdout)
	}
	if strings.Contains(stdout, "All checks passed") {
		t.Fatalf("did not expect full success summary, got %q", stdout)
	}
}

func TestDoctorStrictTreatsDaemonFailureAsFailure(t *testing.T) {
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
		return nil, "", errors.New("daemon unavailable")
	})
	defer restoreCache()

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := doctorCmd()
		cmd.SetArgs([]string{"--strict"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("doctor Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	stdout = stripANSI(stdout)
	if !strings.Contains(stdout, "Some checks failed") {
		t.Fatalf("expected strict failure summary, got %q", stdout)
	}
}

// --- AI backend doctor check tests ---

func stubDoctorLlamaServer(t *testing.T, fn func(context.Context) doctorBackendStatus) func() {
	t.Helper()
	prev := doctorProbeLlamaServer
	doctorProbeLlamaServer = fn
	return func() { doctorProbeLlamaServer = prev }
}

func stubDoctorMLX(t *testing.T, fn func(context.Context) doctorBackendStatus) func() {
	t.Helper()
	prev := doctorProbeMLX
	doctorProbeMLX = fn
	return func() { doctorProbeMLX = prev }
}

func minimalDoctorStubs(t *testing.T) (restoreAll func()) {
	t.Helper()
	restorePath := stubDoctorConfigPath(t, func() string { return filepath.Join(t.TempDir(), "nodes.yaml") })
	restoreLoad := stubDoctorConfigLoader(t, func(string) (*config.Config, error) {
		return &config.Config{Nodes: []config.NodeConfig{{Name: "n", Hostname: "n.local", SSHUser: "axis"}}}, nil
	})
	restoreSSH := stubDoctorSSHChecker(t, func(context.Context, config.NodeConfig) error { return nil })
	restoreCache := stubStatusCachedLoader(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{Nodes: []models.NodeFacts{{Name: "n"}}}, "cached", nil
	})
	restoreLlama := stubDoctorLlamaServer(t, func(context.Context) doctorBackendStatus {
		return doctorBackendStatus{Installed: false}
	})
	restoreMLX := stubDoctorMLX(t, func(context.Context) doctorBackendStatus {
		return doctorBackendStatus{Installed: false}
	})
	return func() {
		restorePath()
		restoreLoad()
		restoreSSH()
		restoreCache()
		restoreLlama()
		restoreMLX()
	}
}

func TestDoctorShowsLlamaServerNotInstalled(t *testing.T) {
	restore := minimalDoctorStubs(t)
	defer restore()

	// Override llama-server to not installed (default from minimalDoctorStubs).
	stdout, _, err := captureProcessOutput(t, func() error {
		return doctorCmd().Execute()
	})
	if err != nil {
		t.Fatalf("doctor Execute: %v", err)
	}
	out := stripANSI(stdout)
	if !strings.Contains(out, "llama-server: not installed") {
		t.Errorf("expected 'llama-server: not installed', got:\n%s", out)
	}
	if !strings.Contains(out, "All checks passed") {
		t.Errorf("expected 'All checks passed' (not-installed is not a failure), got:\n%s", out)
	}
}

func TestDoctorShowsLlamaServerRunning(t *testing.T) {
	restore := minimalDoctorStubs(t)
	defer restore()

	restoreLlama := stubDoctorLlamaServer(t, func(context.Context) doctorBackendStatus {
		return doctorBackendStatus{Installed: true, Running: true, Port: 8080, ResidentCount: 2}
	})
	defer restoreLlama()

	stdout, _, err := captureProcessOutput(t, func() error {
		return doctorCmd().Execute()
	})
	if err != nil {
		t.Fatalf("doctor Execute: %v", err)
	}
	out := stripANSI(stdout)
	if !strings.Contains(out, "llama-server: running on :8080") {
		t.Errorf("expected running+port in output, got:\n%s", out)
	}
	if !strings.Contains(out, "2 models loaded") {
		t.Errorf("expected '2 models loaded', got:\n%s", out)
	}
	if !strings.Contains(out, "All checks passed") {
		t.Errorf("expected 'All checks passed', got:\n%s", out)
	}
}

func TestDoctorShowsMLXRunningNoModels(t *testing.T) {
	restore := minimalDoctorStubs(t)
	defer restore()

	restoreMLX := stubDoctorMLX(t, func(context.Context) doctorBackendStatus {
		return doctorBackendStatus{Installed: true, Running: true, Port: 8080, ResidentCount: 0}
	})
	defer restoreMLX()

	stdout, _, err := captureProcessOutput(t, func() error {
		return doctorCmd().Execute()
	})
	if err != nil {
		t.Fatalf("doctor Execute: %v", err)
	}
	out := stripANSI(stdout)
	if !strings.Contains(out, "mlx: running on :8080") {
		t.Errorf("expected mlx running in output, got:\n%s", out)
	}
	if !strings.Contains(out, "no models loaded") {
		t.Errorf("expected 'no models loaded', got:\n%s", out)
	}
}

func TestDoctorShowsLlamaServerInstalledNotRunning(t *testing.T) {
	restore := minimalDoctorStubs(t)
	defer restore()

	restoreLlama := stubDoctorLlamaServer(t, func(context.Context) doctorBackendStatus {
		return doctorBackendStatus{Installed: true, Running: false}
	})
	defer restoreLlama()

	stdout, _, err := captureProcessOutput(t, func() error {
		return doctorCmd().Execute()
	})
	if err != nil {
		t.Fatalf("doctor Execute: %v", err)
	}
	out := stripANSI(stdout)
	if !strings.Contains(out, "llama-server: installed, not running") {
		t.Errorf("expected 'installed, not running', got:\n%s", out)
	}
	if !strings.Contains(out, "All checks passed") {
		t.Errorf("expected installed-not-running to be advisory only, got:\n%s", out)
	}
}

func TestDoctorBackendProbeErrorIsAdvisory(t *testing.T) {
	restore := minimalDoctorStubs(t)
	defer restore()

	restoreLlama := stubDoctorLlamaServer(t, func(context.Context) doctorBackendStatus {
		return doctorBackendStatus{Err: errors.New("bash: command not found")}
	})
	defer restoreLlama()

	stdout, _, err := captureProcessOutput(t, func() error {
		return doctorCmd().Execute()
	})
	if err != nil {
		t.Fatalf("doctor Execute: %v", err)
	}
	out := stripANSI(stdout)
	if !strings.Contains(out, "probe error") {
		t.Errorf("expected 'probe error' in output, got:\n%s", out)
	}
	// Probe errors are advisory, not core failures.
	if !strings.Contains(out, "Core checks passed with advisory warnings") {
		t.Errorf("expected advisory summary (not core failure), got:\n%s", out)
	}
}

func TestFormatResidentModelCount(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, ", no models loaded"},
		{1, ", 1 model loaded"},
		{3, ", 3 models loaded"},
	}
	for _, tc := range cases {
		if got := formatResidentModelCount(tc.n); got != tc.want {
			t.Errorf("formatResidentModelCount(%d) = %q, want %q", tc.n, got, tc.want)
		}
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
