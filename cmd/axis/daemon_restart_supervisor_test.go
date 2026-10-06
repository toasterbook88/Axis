package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/buildinfo"
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

	sup, _, err := detectSupervisor(context.Background(), deps)
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

	sup, _, err := detectSupervisor(context.Background(), deps)
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
	detectSupervisor = func(context.Context, daemonServiceDependencies) (supervisorType, supervisedService, error) {
		return supervisorNone, supervisedService{}, nil
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

// healthServer serves /health with the given daemon identity and counts hits.
func healthServer(t *testing.T, version, commit string) (string, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&hits, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "version": version, "commit": commit})
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), &hits
}

// runVersion executes the real version command and returns its output.
func runVersion(t *testing.T, cliCommit string, args ...string) string {
	t.Helper()
	prev := buildinfo.Commit
	t.Cleanup(func() { buildinfo.Commit = prev })
	buildinfo.Commit = cliCommit
	cmd := versionCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("version %v: %v", args, err)
	}
	return out.String()
}

// TestVersionReportsRunningDaemonVersion verifies that versionCmd queries
// the running daemon and reports its version and commit.
func TestVersionReportsRunningDaemonVersion(t *testing.T) {
	addr, hits := healthServer(t, Version, "abc1234")
	out := runVersion(t, "abc1234", "--cache-addr", addr)
	want := fmt.Sprintf("daemon:   v%s (commit abc1234) on %s", Version, addr)
	if !strings.Contains(out, want) {
		t.Fatalf("missing %q in:\n%s", want, out)
	}
	if strings.Contains(out, "warning:") || atomic.LoadInt32(hits) != 1 {
		t.Fatalf("expected one query and no warning (hits=%d):\n%s", atomic.LoadInt32(hits), out)
	}
}

// TestVersionWarnsOnDaemonVersionMismatch verifies that a version mismatch,
// or a commit mismatch at the same version, between CLI and daemon is reported.
func TestVersionWarnsOnDaemonVersionMismatch(t *testing.T) {
	addr, _ := healthServer(t, "0.0.0-old", "abc1234")
	out := runVersion(t, "abc1234", "--cache-addr", addr)
	if want := fmt.Sprintf("warning:  daemon version 0.0.0-old differs from CLI version %s", Version); !strings.Contains(out, want) {
		t.Fatalf("missing %q in:\n%s", want, out)
	}

	addr, _ = healthServer(t, Version, "def5678")
	out = runVersion(t, "abc1234", "--cache-addr", addr)
	if want := fmt.Sprintf("warning:  daemon commit def5678 differs from CLI commit abc1234 (same version %s)", Version); !strings.Contains(out, want) {
		t.Fatalf("missing %q in:\n%s", want, out)
	}
}

// TestVersionReportsDaemonNotResponding verifies that a non-responding
// daemon is reported.
func TestVersionReportsDaemonNotResponding(t *testing.T) {
	out := runVersion(t, "abc1234", "--cache-addr", "127.0.0.1:1")
	if !strings.Contains(out, "daemon:   not responding on 127.0.0.1:1") {
		t.Fatalf("expected not-responding line in:\n%s", out)
	}
}

// TestVersionLocalFlagSkipsDaemonQuery verifies that --local skips the
// daemon query.
func TestVersionLocalFlagSkipsDaemonQuery(t *testing.T) {
	addr, hits := healthServer(t, Version, "abc1234")
	out := runVersion(t, "abc1234", "--local", "--cache-addr", addr)
	if atomic.LoadInt32(hits) != 0 || strings.Contains(out, "daemon:") {
		t.Fatalf("--local must not query the daemon (hits=%d):\n%s", atomic.LoadInt32(hits), out)
	}
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
// the version and commit when the daemon is responding.
func TestVersionQueryDaemonSuccess(t *testing.T) {
	addr, _ := healthServer(t, "9.9.9", "abc1234")
	id, err := versionQueryDaemon(context.Background(), addr)
	if err != nil {
		t.Fatalf("versionQueryDaemon: %v", err)
	}
	if id.Version != "9.9.9" || id.Commit != "abc1234" {
		t.Fatalf("got %+v", id)
	}
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
			if name == "systemctl" && len(args) > 0 && args[0] == "--user" && len(args) > 1 && args[1] == "restart" {
				restartCalled = true
			}
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
			if name == "systemctl" && len(args) > 0 && args[0] == "--user" && len(args) > 1 && args[1] == "restart" {
				restartCalled = true
			}
			return nil, nil
		},
	}

	// Mock restartFetchMeta to return current version so pollDaemonVersion succeeds.
	prevFetch := restartFetchMeta
	defer func() { restartFetchMeta = prevFetch }()
	var polled []string
	restartFetchMeta = func(_ context.Context, addr string) (daemon.Metadata, error) {
		polled = append(polled, addr)
		return serving(), nil
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
	// The unit was installed with --addr /tmp/axis.sock; readiness must be
	// polled there, not on the caller's default address.
	if len(polled) == 0 || polled[0] != "/tmp/axis.sock" {
		t.Fatalf("expected readiness poll on /tmp/axis.sock, got %q", polled)
	}
}
