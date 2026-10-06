package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/daemon"
)

// updateRestartHarness stubs only the lowest restart seams: supervisor
// detection, the service-manager command runner (systemctl/launchctl) and the
// daemon metadata fetch. restartAfterUpdate, restartSupervisedDaemon,
// restartViaSupervisor, pollDaemonVersion and installRelease all run for real.
type updateRestartHarness struct {
	supervisor    supervisorType
	service       supervisedService
	managerErr    error  // returned by the service-manager restart command
	daemonVersion string // version the daemon reports after restart ("" = current)

	mu           sync.Mutex
	managerCalls []string
	polledAddrs  []string
}

func (h *updateRestartHarness) install(t *testing.T) {
	t.Helper()
	prevDetect, prevDeps, prevFetch := detectSupervisor, restartServiceDeps, restartFetchMeta
	prevPoll, prevDeadline := restartPollInterval, restartReadyDeadline

	detectSupervisor = func(context.Context, daemonServiceDependencies) (supervisorType, supervisedService, error) {
		return h.supervisor, h.service, nil
	}
	restartServiceDeps = func() daemonServiceDependencies {
		return daemonServiceDependencies{
			goos:    runtime.GOOS,
			homeDir: func() (string, error) { return t.TempDir(), nil },
			uid:     func() int { return 501 },
			run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				h.mu.Lock()
				h.managerCalls = append(h.managerCalls, name+" "+strings.Join(args, " "))
				h.mu.Unlock()
				return nil, h.managerErr
			},
		}
	}
	restartFetchMeta = func(_ context.Context, addr string) (daemon.Metadata, error) {
		h.mu.Lock()
		h.polledAddrs = append(h.polledAddrs, addr)
		h.mu.Unlock()
		v := h.daemonVersion
		if v == "" {
			v = current()
		}
		return daemon.Metadata{Version: v, Ready: true}, nil
	}
	restartPollInterval = 2 * time.Millisecond
	restartReadyDeadline = 60 * time.Millisecond

	t.Cleanup(func() {
		detectSupervisor, restartServiceDeps, restartFetchMeta = prevDetect, prevDeps, prevFetch
		restartPollInterval, restartReadyDeadline = prevPoll, prevDeadline
	})
}

func (h *updateRestartHarness) calls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.managerCalls...)
}

func (h *updateRestartHarness) addrs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.polledAddrs...)
}

// runInstallRelease drives the real installRelease against a served release
// whose checksums verify, with every target reported as an AXIS install at
// installed. It returns stdout, stderr and the error.
func runInstallRelease(t *testing.T, targets []string, installed, latest string, noRestart bool) (string, string, error) {
	t.Helper()
	prevInspect, prevGet := inspectBinary, updateGetFunc
	t.Cleanup(func() { inspectBinary, updateGetFunc = prevInspect, prevGet })
	inspectBinary = func(path string) (installInfo, error) {
		abs := mustAbs(path)
		return installInfo{Path: abs, Resolved: abs, IsAxis: true, Version: installed}, nil
	}
	archive := buildTestArchive(t, []byte("NEW-BINARY-"+latest))
	name := fmt.Sprintf("axis_%s_%s_%s.tar.gz", latest, runtime.GOOS, runtime.GOARCH)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "checksums") {
			fmt.Fprintln(w, checksumLine(archive, name))
			return
		}
		_, _ = w.Write(archive)
	}))
	t.Cleanup(srv.Close)
	updateGetFunc = srv.Client().Get

	rel := &ghRelease{TagName: "v" + latest}
	rel.Assets = append(rel.Assets,
		ghAsset{Name: name, BrowserDownloadURL: srv.URL + "/asset/" + name},
		ghAsset{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/checksums.txt"},
	)
	cmd := updateCmd()
	cmd.SetContext(context.Background())
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := installRelease(cmd, rel, latest, targets, "", modeAll, noRestart, &errOut, &out)
	return out.String(), errOut.String(), err
}

type probeCtxKey struct{}

func writeOldBinary(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestUpdateRestartsActiveSystemdUserUnitAfterReplace drives installRelease:
// the service binary is replaced, systemctl restarts the unit, and readiness
// is polled on the address the unit was installed with.
func TestUpdateRestartsActiveSystemdUserUnitAfterReplace(t *testing.T) {
	target := writeOldBinary(t, t.TempDir(), "axis")
	h := &updateRestartHarness{
		supervisor: supervisorSystemd,
		service:    supervisedService{execPath: target, addr: "/tmp/custom-axis.sock"},
	}
	h.install(t)

	out, errOut, err := runInstallRelease(t, []string{target}, "0.1.0", "1.0.0", false)
	if err != nil {
		t.Fatalf("installRelease: %v\nout=%s\nerr=%s", err, out, errOut)
	}
	if got, _ := os.ReadFile(target); string(got) == "OLD" {
		t.Fatal("target was not replaced")
	}
	if calls := h.calls(); len(calls) != 1 || calls[0] != "systemctl --user restart "+daemonSystemdUnit {
		t.Fatalf("expected one systemctl restart, got %q", calls)
	}
	addrs := h.addrs()
	if len(addrs) == 0 {
		t.Fatal("readiness was never polled")
	}
	for _, a := range addrs {
		if a != "/tmp/custom-axis.sock" {
			t.Fatalf("polled %q; must poll the unit's --addr", a)
		}
	}
	if !strings.Contains(out, "serving current version") {
		t.Fatalf("expected ready message, got %q", out)
	}
}

// TestUpdateKickstartsLoadedLaunchdAgentAfterReplace runs the real restart
// path for a loaded launchd agent.
func TestUpdateKickstartsLoadedLaunchdAgentAfterReplace(t *testing.T) {
	h := &updateRestartHarness{
		supervisor: supervisorLaunchd,
		service:    supervisedService{execPath: "/opt/homebrew/bin/axis"},
	}
	h.install(t)

	var out bytes.Buffer
	if err := restartAfterUpdate(context.Background(), "127.0.0.1:7777", []string{"/opt/homebrew/bin/axis"}, &out); err != nil {
		t.Fatalf("restartAfterUpdate: %v", err)
	}
	want := "launchctl kickstart -k gui/501/" + daemonLaunchdLabel
	if calls := h.calls(); len(calls) != 1 || calls[0] != want {
		t.Fatalf("expected %q, got %q", want, calls)
	}
	if addrs := h.addrs(); len(addrs) == 0 || addrs[0] != "127.0.0.1:7777" {
		t.Fatalf("with no --addr in the plist, the caller address must be polled; got %q", addrs)
	}
}

// TestUpdateRestartsStandaloneDaemonAfterReplace: with no supervisor the real
// standalone restart (terminate + spawn) runs.
func TestUpdateRestartsStandaloneDaemonAfterReplace(t *testing.T) {
	h := &updateRestartHarness{supervisor: supervisorNone}
	h.install(t)
	rh := &restartHarness{metas: []daemon.Metadata{{Version: "0.0.0-old", Ready: true}, serving()}, pid: 111}
	rh.install(t)

	var out bytes.Buffer
	if err := restartAfterUpdate(context.Background(), "127.0.0.1:1", []string{"/usr/local/bin/axis"}, &out); err != nil {
		t.Fatalf("restartAfterUpdate: %v\n%s", err, out.String())
	}
	if rh.terminates != 1 || rh.spawns != 1 {
		t.Fatalf("expected standalone terminate+spawn, got terminates=%d spawns=%d", rh.terminates, rh.spawns)
	}
	if calls := h.calls(); len(calls) != 0 {
		t.Fatalf("no service manager may be invoked without a supervisor, got %q", calls)
	}
}

// TestUpdateNoRestartFlagSkipsRestart drives installRelease with --no-restart.
func TestUpdateNoRestartFlagSkipsRestart(t *testing.T) {
	target := writeOldBinary(t, t.TempDir(), "axis")
	h := &updateRestartHarness{supervisor: supervisorSystemd, service: supervisedService{execPath: target}}
	h.install(t)

	out, errOut, err := runInstallRelease(t, []string{target}, "0.1.0", "1.0.0", true)
	if err != nil {
		t.Fatalf("installRelease: %v\nout=%s\nerr=%s", err, out, errOut)
	}
	if got, _ := os.ReadFile(target); string(got) == "OLD" {
		t.Fatal("target was not replaced")
	}
	if calls := h.calls(); len(calls) != 0 || strings.Contains(out, "Restarting daemon") {
		t.Fatalf("--no-restart must not restart: calls=%q out=%q", calls, out)
	}
}

// TestUpdateDoesNotRestartWhenNothingUpdated drives installRelease when the
// install is already current.
func TestUpdateDoesNotRestartWhenNothingUpdated(t *testing.T) {
	target := writeOldBinary(t, t.TempDir(), "axis")
	h := &updateRestartHarness{supervisor: supervisorSystemd, service: supervisedService{execPath: target}}
	h.install(t)

	out, errOut, err := runInstallRelease(t, []string{target}, "1.0.0", "1.0.0", false)
	if err != nil {
		t.Fatalf("installRelease: %v\nout=%s\nerr=%s", err, out, errOut)
	}
	if !strings.Contains(out, "Nothing to update") {
		t.Fatalf("expected nothing to update, got %q", out)
	}
	if calls := h.calls(); len(calls) != 0 {
		t.Fatalf("no restart expected, got %q", calls)
	}
}

// TestUpdateDoesNotRestartWhenReplaceFailed drives installRelease with failing
// replacements: all failing aborts with no restart, and a failed service
// binary is never restarted even when another target succeeded.
func TestUpdateDoesNotRestartWhenReplaceFailed(t *testing.T) {
	dir := t.TempDir()
	service := writeOldBinary(t, dir, "axis-service")
	other := writeOldBinary(t, dir, "axis-other")
	h := &updateRestartHarness{supervisor: supervisorSystemd, service: supervisedService{execPath: service}}
	h.install(t)
	prevReplace := installReplaceExecutable
	t.Cleanup(func() { installReplaceExecutable = prevReplace })
	installReplaceExecutable = func(target string, data []byte) error {
		if target == service {
			return fmt.Errorf("simulated permission denied")
		}
		return prevReplace(target, data)
	}

	// All targets fail: error, no restart.
	if _, _, err := runInstallRelease(t, []string{service}, "0.1.0", "1.0.0", false); err == nil {
		t.Fatal("expected failure when every replacement fails")
	}
	if calls := h.calls(); len(calls) != 0 {
		t.Fatalf("no restart expected when nothing was replaced, got %q", calls)
	}

	// Service binary fails, other target succeeds: restart skipped.
	out, errOut, err := runInstallRelease(t, []string{service, other}, "0.1.0", "1.0.0", false)
	if err != nil {
		t.Fatalf("installRelease: %v\nout=%s\nerr=%s", err, out, errOut)
	}
	if calls := h.calls(); len(calls) != 0 {
		t.Fatalf("failed service binary must not be restarted, got %q", calls)
	}
	if !strings.Contains(out, "was not updated; skipping daemon restart") {
		t.Fatalf("expected skip warning, got %q", out)
	}
}

// TestUpdateSkipsRestartWhenServiceExecPathNotUpdated verifies that the
// restart is skipped when the service exec path is not among the replaced
// paths. This tests the actual skip logic in restartSupervisedDaemon by
// setting up a real supervisor detection.
func TestUpdateSkipsRestartWhenServiceExecPathNotUpdated(t *testing.T) {
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
			// Only flag actual restart commands, not is-active checks.
			if name == "systemctl" && len(args) > 0 && args[0] == "restart" {
				restartCalled = true
			}
			return nil, nil
		},
	}

	// Call restartSupervisedDaemon with a path that doesn't match the service exec.
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

// TestUpdateFailsWhenDaemonVersionMismatchAfterRestart verifies that a
// version mismatch after restart is reported as an error.
func TestUpdateFailsWhenDaemonVersionMismatchAfterRestart(t *testing.T) {
	h := &updateRestartHarness{
		supervisor:    supervisorSystemd,
		service:       supervisedService{execPath: "/usr/local/bin/axis"},
		daemonVersion: "0.0.0-old",
	}
	h.install(t)

	var out bytes.Buffer
	err := restartAfterUpdate(context.Background(), "127.0.0.1:1", []string{"/usr/local/bin/axis"}, &out)
	if err == nil {
		t.Fatal("expected error when daemon version mismatch after restart")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Fatalf("expected version mismatch error, got %v", err)
	}
}

// TestUpdateNoDaemonRunningIsNotAnError verifies that no daemon running
// is not treated as an error.
func TestUpdateNoDaemonRunningIsNotAnError(t *testing.T) {
	h := &updateRestartHarness{supervisor: supervisorNone}
	h.install(t)
	// No daemon answers and no listener PID: the standalone path starts one.
	rh := &restartHarness{
		metas:    []daemon.Metadata{{}, serving()},
		metaErrs: []error{fmt.Errorf("connection refused")},
		pid:      0,
	}
	rh.install(t)

	var out bytes.Buffer
	if err := restartAfterUpdate(context.Background(), "127.0.0.1:1", []string{"/usr/local/bin/axis"}, &out); err != nil {
		t.Fatalf("no running daemon must not fail the update: %v\n%s", err, out.String())
	}
	if rh.terminates != 0 || rh.spawns != 1 || !strings.Contains(out.String(), "No daemon responding") {
		t.Fatalf("expected a fresh start only: terminates=%d spawns=%d out=%q", rh.terminates, rh.spawns, out.String())
	}
}

// TestDetectSupervisorLinuxSystemd tests supervisor detection on Linux
// with an active systemd user unit.
func TestDetectSupervisorLinuxSystemd(t *testing.T) {
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

	deps := daemonServiceDependencies{
		goos:    "linux",
		homeDir: func() (string, error) { return home, nil },
		uid:     func() int { return 1000 },
		run: func(probeCtx context.Context, name string, args ...string) ([]byte, error) {
			// The probe must run under the caller's context, not Background.
			if probeCtx.Value(probeCtxKey{}) != "caller" {
				t.Fatalf("%s probe did not receive the caller context", name)
			}
			return []byte("active\n"), nil
		},
	}
	ctx := context.WithValue(context.Background(), probeCtxKey{}, "caller")

	sup, svc, err := detectSupervisor(ctx, deps)
	if err != nil {
		t.Fatalf("detectSupervisor: %v", err)
	}
	if sup != supervisorSystemd {
		t.Fatalf("expected systemd supervisor, got %v", sup)
	}
	if svc.execPath != "/usr/local/bin/axis" || svc.addr != "/tmp/axis.sock" {
		t.Fatalf("expected /usr/local/bin/axis with --addr /tmp/axis.sock, got %+v", svc)
	}
}

// TestDetectSupervisorLinuxNoUnit tests that no supervisor is detected
// when the unit file doesn't exist.
func TestDetectSupervisorLinuxNoUnit(t *testing.T) {
	home := t.TempDir()
	deps := daemonServiceDependencies{
		goos:    "linux",
		homeDir: func() (string, error) { return home, nil },
		uid:     func() int { return 1000 },
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, fmt.Errorf("not found")
		},
	}

	sup, _, err := detectSupervisor(context.Background(), deps)
	if err != nil {
		t.Fatalf("detectSupervisor: %v", err)
	}
	if sup != supervisorNone {
		t.Fatalf("expected no supervisor, got %v", sup)
	}
}

// TestDetectSupervisorLinuxUnitNotActive tests that no supervisor is
// detected when the unit file exists but is not active.
func TestDetectSupervisorLinuxUnitNotActive(t *testing.T) {
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

	deps := daemonServiceDependencies{
		goos:    "linux",
		homeDir: func() (string, error) { return home, nil },
		uid:     func() int { return 1000 },
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			// Simulate systemctl is-active failing.
			return nil, fmt.Errorf("inactive")
		},
	}

	sup, _, err := detectSupervisor(context.Background(), deps)
	if err != nil {
		t.Fatalf("detectSupervisor: %v", err)
	}
	if sup != supervisorNone {
		t.Fatalf("expected no supervisor (unit not active), got %v", sup)
	}
}

// TestDetectSupervisorDarwinLaunchd tests supervisor detection on macOS
// with a loaded launchd agent.
func TestDetectSupervisorDarwinLaunchd(t *testing.T) {
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

	deps := daemonServiceDependencies{
		goos:    "darwin",
		homeDir: func() (string, error) { return home, nil },
		uid:     func() int { return 501 },
		run: func(probeCtx context.Context, name string, args ...string) ([]byte, error) {
			if probeCtx.Value(probeCtxKey{}) != "caller" {
				t.Fatalf("%s probe did not receive the caller context", name)
			}
			return []byte("loaded"), nil
		},
	}
	ctx := context.WithValue(context.Background(), probeCtxKey{}, "caller")

	sup, svc, err := detectSupervisor(ctx, deps)
	if err != nil {
		t.Fatalf("detectSupervisor: %v", err)
	}
	if sup != supervisorLaunchd {
		t.Fatalf("expected launchd supervisor, got %v", sup)
	}
	if svc.execPath != "/opt/homebrew/bin/axis" {
		t.Fatalf("expected exec path /opt/homebrew/bin/axis, got %q", svc.execPath)
	}
}

// TestDetectSupervisorDarwinNoPlist tests that no supervisor is detected
// when the plist doesn't exist.
func TestDetectSupervisorDarwinNoPlist(t *testing.T) {
	home := t.TempDir()
	deps := daemonServiceDependencies{
		goos:    "darwin",
		homeDir: func() (string, error) { return home, nil },
		uid:     func() int { return 501 },
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, fmt.Errorf("not found")
		},
	}

	sup, _, err := detectSupervisor(context.Background(), deps)
	if err != nil {
		t.Fatalf("detectSupervisor: %v", err)
	}
	if sup != supervisorNone {
		t.Fatalf("expected no supervisor, got %v", sup)
	}
}

// TestParseSystemdExecStart round-trips a unit rendered by
// renderDaemonService, including a quoted path and a custom --addr.
func TestParseSystemdExecStart(t *testing.T) {
	unit, err := renderDaemonService("linux", "/opt/my apps/axis", "127.0.0.1:9911", "1m", "/home/op")
	if err != nil {
		t.Fatal(err)
	}
	svc := serviceFromArgs(systemdExecStartArgs(string(unit)))
	if svc.execPath != "/opt/my apps/axis" || svc.addr != "127.0.0.1:9911" {
		t.Fatalf("got %+v from:\n%s", svc, unit)
	}
}

// TestParseLaunchdProgramArguments round-trips a plist rendered by
// renderDaemonService, including XML escaping and a custom --addr.
func TestParseLaunchdProgramArguments(t *testing.T) {
	plist, err := renderDaemonService("darwin", "/Users/op/R&D/axis", "/Users/op/.axis/custom.sock", "1m", "/Users/op")
	if err != nil {
		t.Fatal(err)
	}
	svc := serviceFromArgs(launchdProgramArguments(string(plist)))
	if svc.execPath != "/Users/op/R&D/axis" || svc.addr != "/Users/op/.axis/custom.sock" {
		t.Fatalf("got %+v", svc)
	}
}

// TestRestartViaSupervisorSystemd tests restarting via systemctl.
func TestRestartViaSupervisorSystemd(t *testing.T) {
	var called bool
	deps := daemonServiceDependencies{
		goos:    "linux",
		homeDir: func() (string, error) { return t.TempDir(), nil },
		uid:     func() int { return 1000 },
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			called = true
			if name != "systemctl" {
				t.Fatalf("expected systemctl, got %s", name)
			}
			return nil, nil
		},
	}

	var out bytes.Buffer
	err := restartViaSupervisor(context.Background(), deps, supervisorSystemd, &out)
	if err != nil {
		t.Fatalf("restartViaSupervisor: %v", err)
	}
	if !called {
		t.Fatal("expected systemctl to be called")
	}
}

// TestRestartViaSupervisorLaunchd tests restarting via launchctl.
func TestRestartViaSupervisorLaunchd(t *testing.T) {
	var called bool
	deps := daemonServiceDependencies{
		goos:    "darwin",
		homeDir: func() (string, error) { return t.TempDir(), nil },
		uid:     func() int { return 501 },
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			called = true
			if name != "launchctl" {
				t.Fatalf("expected launchctl, got %s", name)
			}
			return nil, nil
		},
	}

	var out bytes.Buffer
	err := restartViaSupervisor(context.Background(), deps, supervisorLaunchd, &out)
	if err != nil {
		t.Fatalf("restartViaSupervisor: %v", err)
	}
	if !called {
		t.Fatal("expected launchctl to be called")
	}
}

// TestPollDaemonVersionSuccess tests that pollDaemonVersion succeeds when
// the daemon reports the expected version.
func TestPollDaemonVersionSuccess(t *testing.T) {
	prevFetch := restartFetchMeta
	defer func() { restartFetchMeta = prevFetch }()

	restartFetchMeta = func(ctx context.Context, addr string) (daemon.Metadata, error) {
		return daemon.Metadata{Version: daemon.Version, Ready: true}, nil
	}

	var out bytes.Buffer
	err := pollDaemonVersion(context.Background(), "127.0.0.1:1", &out)
	if err != nil {
		t.Fatalf("pollDaemonVersion: %v", err)
	}
}

// TestPollDaemonVersionTimeout tests that pollDaemonVersion times out when
// the daemon never reports the expected version.
func TestPollDaemonVersionTimeout(t *testing.T) {
	prevFetch := restartFetchMeta
	prevDeadline := restartReadyDeadline
	prevPoll := restartPollInterval
	defer func() {
		restartFetchMeta = prevFetch
		restartReadyDeadline = prevDeadline
		restartPollInterval = prevPoll
	}()

	restartFetchMeta = func(ctx context.Context, addr string) (daemon.Metadata, error) {
		return daemon.Metadata{Version: "0.0.0-old", Ready: true}, nil
	}
	restartPollInterval = 2 * time.Millisecond
	restartReadyDeadline = 10 * time.Millisecond

	var out bytes.Buffer
	err := pollDaemonVersion(context.Background(), "127.0.0.1:1", &out)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
