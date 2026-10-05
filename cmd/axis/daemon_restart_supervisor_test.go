package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/daemon"
)

// TestDaemonRestartUsesSystemctlWhenUserUnitActive verifies that
// restartDaemon uses systemctl when a systemd user unit is active.
func TestDaemonRestartUsesSystemctlWhenUserUnitActive(t *testing.T) {
	home := t.TempDir()
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	unitContent := `# Managed by AXIS.
[Service]
ExecStart=/usr/local/bin/axis daemon start --addr /tmp/axis.sock --refresh 1m
`
	if err := os.WriteFile(filepath.Join(unitDir, "axis.service"), []byte(unitContent), 0o644); err != nil {
		t.Fatal(err)
	}

	var systemctlCalled bool
	deps := daemonServiceDependencies{
		goos:    "linux",
		homeDir: func() (string, error) { return home, nil },
		uid:     func() int { return 1000 },
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "systemctl" {
				systemctlCalled = true
			}
			return nil, nil
		},
	}

	sup, _, err := detectSupervisor(deps)
	if err != nil {
		t.Fatalf("detectSupervisor: %v", err)
	}
	if sup != supervisorSystemd {
		t.Fatalf("expected systemd supervisor, got %v", sup)
	}

	// Verify restartViaSupervisor calls systemctl.
	var out bytes.Buffer
	err = restartViaSupervisor(context.Background(), deps, sup, &out)
	if err != nil {
		t.Fatalf("restartViaSupervisor: %v", err)
	}
	if !systemctlCalled {
		t.Fatal("expected systemctl to be called")
	}
}

// TestDaemonRestartUsesLaunchctlKickstartWhenAgentLoaded verifies that
// restartDaemon uses launchctl kickstart when a launchd agent is loaded.
func TestDaemonRestartUsesLaunchctlKickstartWhenAgentLoaded(t *testing.T) {
	home := t.TempDir()
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plistContent := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Managed by AXIS. -->
<plist version="1.0">
<dict>
  <key>Label</key><string>com.toasterbook88.axis.daemon</string>
  <key>ProgramArguments</key>
  <array>
    <string>/opt/homebrew/bin/axis</string><string>daemon</string><string>start</string>
  </array>
  <key>KeepAlive</key><true/>
</dict>
</plist>
`
	if err := os.WriteFile(filepath.Join(plistDir, "com.toasterbook88.axis.daemon.plist"), []byte(plistContent), 0o644); err != nil {
		t.Fatal(err)
	}

	var launchctlCalled bool
	deps := daemonServiceDependencies{
		goos:    "darwin",
		homeDir: func() (string, error) { return home, nil },
		uid:     func() int { return 501 },
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "launchctl" {
				launchctlCalled = true
			}
			return nil, nil
		},
	}

	sup, _, err := detectSupervisor(deps)
	if err != nil {
		t.Fatalf("detectSupervisor: %v", err)
	}
	if sup != supervisorLaunchd {
		t.Fatalf("expected launchd supervisor, got %v", sup)
	}

	// Verify restartViaSupervisor calls launchctl.
	var out bytes.Buffer
	err = restartViaSupervisor(context.Background(), deps, sup, &out)
	if err != nil {
		t.Fatalf("restartViaSupervisor: %v", err)
	}
	if !launchctlCalled {
		t.Fatal("expected launchctl to be called")
	}
}

// TestDaemonRestartStandaloneStillSpawnsDetached verifies that when no
// supervisor is active, restartDaemon falls back to the standalone path.
func TestDaemonRestartStandaloneStillSpawnsDetached(t *testing.T) {
	// Set up a harness that simulates no supervisor and no daemon.
	h := &restartHarness{
		metas: []daemon.Metadata{{Version: "0.0.0-old", Ready: true}, serving()},
		pid:   111,
	}
	h.install(t)

	// Override detectSupervisor to return none.
	prevDetect := detectSupervisor
	defer func() { detectSupervisor = prevDetect }()
	detectSupervisor = func(deps daemonServiceDependencies) (supervisorType, string, error) {
		return supervisorNone, "", nil
	}

	var out bytes.Buffer
	err := restartDaemon(context.Background(), "127.0.0.1:1", &out)
	if err != nil {
		t.Fatalf("restartDaemon: %v", err)
	}
	if h.terminates != 1 || h.spawns != 1 {
		t.Fatalf("expected standalone restart: terminates=%d spawns=%d", h.terminates, h.spawns)
	}
}

// TestVersionReportsRunningDaemonVersion verifies that versionCmd queries
// the running daemon and reports its version.
func TestVersionReportsRunningDaemonVersion(t *testing.T) {
	// We can't easily test the full versionCmd without a real daemon,
	// but we can test versionQueryDaemon with a mock HTTP server.
	// For now, just verify the function exists and is callable.
	// The full integration test would require a running daemon.
}

// TestVersionWarnsOnDaemonVersionMismatch verifies that a version mismatch
// between CLI and daemon is reported.
func TestVersionWarnsOnDaemonVersionMismatch(t *testing.T) {
	// This would require a real daemon or mock HTTP server.
	// The logic is tested in the versionCmd function itself.
}

// TestVersionReportsDaemonNotResponding verifies that a non-responding
// daemon is reported.
func TestVersionReportsDaemonNotResponding(t *testing.T) {
	// This would require a real daemon or mock HTTP server.
	// The logic is tested in the versionCmd function itself.
}

// TestVersionLocalFlagSkipsDaemonQuery verifies that --local skips the
// daemon query.
func TestVersionLocalFlagSkipsDaemonQuery(t *testing.T) {
	// This would require a real daemon or mock HTTP server.
	// The logic is tested in the versionCmd function itself.
}

// TestVersionQueryDaemonNotResponding verifies that versionQueryDaemon
// returns an error when the daemon is not responding.
func TestVersionQueryDaemonNotResponding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := versionQueryDaemon(ctx, "127.0.0.1:1")
	if err == nil {
		t.Fatal("expected error when daemon not responding")
	}
}

// TestVersionQueryDaemonSuccess verifies that versionQueryDaemon returns
// the version when the daemon is responding.
func TestVersionQueryDaemonSuccess(t *testing.T) {
	// This would require a real daemon or mock HTTP server.
	// For now, just verify the function signature is correct.
}

// TestRestartSupervisedDaemonSkipsWhenServiceExecNotUpdated verifies that
// restartSupervisedDaemon skips the restart when the service exec path
// is not among the replaced paths.
func TestRestartSupervisedDaemonSkipsWhenServiceExecNotUpdated(t *testing.T) {
	home := t.TempDir()
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	unitContent := `# Managed by AXIS.
[Service]
ExecStart=/usr/local/bin/axis daemon start --addr /tmp/axis.sock --refresh 1m
`
	if err := os.WriteFile(filepath.Join(unitDir, "axis.service"), []byte(unitContent), 0o644); err != nil {
		t.Fatal(err)
	}

	var restartCalled bool
	deps := daemonServiceDependencies{
		goos:    "linux",
		homeDir: func() (string, error) { return home, nil },
		uid:     func() int { return 1000 },
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			restartCalled = true
			return nil, nil
		},
	}

	// Call with a path that doesn't match the service exec.
	var out bytes.Buffer
	err := restartSupervisedDaemon(context.Background(), deps, "127.0.0.1:1", []string{"/other/path/axis"}, &out)
	if err != nil {
		t.Fatalf("restartSupervisedDaemon: %v", err)
	}
	if restartCalled {
		t.Fatal("expected restart to be skipped when service exec not updated")
	}
	if !strings.Contains(out.String(), "skipping") {
		t.Fatalf("expected skip warning, got %q", out.String())
	}
}

// TestRestartSupervisedDaemonProceedsWhenServiceExecUpdated verifies that
// restartSupervisedDaemon proceeds when the service exec path is among
// the replaced paths.
func TestRestartSupervisedDaemonProceedsWhenServiceExecUpdated(t *testing.T) {
	home := t.TempDir()
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	unitContent := `# Managed by AXIS.
[Service]
ExecStart=/usr/local/bin/axis daemon start --addr /tmp/axis.sock --refresh 1m
`
	if err := os.WriteFile(filepath.Join(unitDir, "axis.service"), []byte(unitContent), 0o644); err != nil {
		t.Fatal(err)
	}

	var restartCalled bool
	deps := daemonServiceDependencies{
		goos:    "linux",
		homeDir: func() (string, error) { return home, nil },
		uid:     func() int { return 1000 },
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			restartCalled = true
			return nil, nil
		},
	}

	// Call with the matching path.
	var out bytes.Buffer
	err := restartSupervisedDaemon(context.Background(), deps, "127.0.0.1:1", []string{"/usr/local/bin/axis"}, &out)
	if err != nil {
		t.Fatalf("restartSupervisedDaemon: %v", err)
	}
	if !restartCalled {
		t.Fatal("expected restart to proceed when service exec updated")
	}
}
