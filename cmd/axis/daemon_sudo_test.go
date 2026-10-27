package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/daemon"
)

// sudoRestartHarness simulates `sudo axis update` by an invoking user whose
// home holds (or lacks) the AXIS-managed service. Supervisor detection,
// restartThroughSupervisor and the readiness poll run for real; only the
// service-manager runner, the daemon probes and the standalone spawn are stubbed.
type sudoRestartHarness struct {
	goos      string
	inv       sudoInvoker
	lookupErr error

	mu          sync.Mutex
	calls       []string
	healthAddrs []string
	metaFetches int
	spawns      int
}

func (h *sudoRestartHarness) install(t *testing.T) {
	t.Helper()
	prevLookup, prevDeps := lookupSudoInvoker, restartServiceDeps
	prevHealth, prevMeta, prevSpawn := restartFetchHealth, restartFetchMeta, restartSpawnDaemon
	prevPoll, prevDeadline := restartPollInterval, restartReadyDeadline

	lookupSudoInvoker = func() (sudoInvoker, bool, error) {
		return h.inv, true, h.lookupErr
	}
	restartServiceDeps = func() daemonServiceDependencies {
		return daemonServiceDependencies{
			goos:    h.goos,
			homeDir: func() (string, error) { return "/root", nil },
			uid:     func() int { return 0 },
			run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				h.mu.Lock()
				h.calls = append(h.calls, name+" "+strings.Join(args, " "))
				h.mu.Unlock()
				return nil, nil
			},
		}
	}
	restartFetchHealth = func(_ context.Context, addr string) (daemonIdentity, error) {
		h.mu.Lock()
		h.healthAddrs = append(h.healthAddrs, addr)
		h.mu.Unlock()
		return daemonIdentity{Version: daemon.Version}, nil
	}
	restartFetchMeta = func(context.Context, string) (daemon.Metadata, error) {
		h.mu.Lock()
		h.metaFetches++
		h.mu.Unlock()
		return daemon.Metadata{}, errors.New("root holds root's token, not the owner's")
	}
	restartSpawnDaemon = func(string) (int, error) {
		h.mu.Lock()
		h.spawns++
		h.mu.Unlock()
		return 4242, nil
	}
	restartPollInterval = 2 * time.Millisecond
	restartReadyDeadline = 60 * time.Millisecond

	t.Cleanup(func() {
		lookupSudoInvoker, restartServiceDeps = prevLookup, prevDeps
		restartFetchHealth, restartFetchMeta, restartSpawnDaemon = prevHealth, prevMeta, prevSpawn
		restartPollInterval, restartReadyDeadline = prevPoll, prevDeadline
	})
}

func writeManagedUnit(t *testing.T, home, execStart string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	unit := "# Managed by AXIS.\n[Service]\nExecStart=" + execStart + "\n"
	if err := os.WriteFile(filepath.Join(dir, daemonSystemdUnit), []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateUnderSudoRestartsInvokerUserUnit: the invoking user's systemd
// user manager is addressed explicitly, and readiness is read from /health on
// the unit's address.
func TestUpdateUnderSudoRestartsInvokerUserUnit(t *testing.T) {
	home := t.TempDir()
	writeManagedUnit(t, home, "/usr/local/bin/axis daemon start --addr /srv/op/axis.sock --refresh 1m")
	h := &sudoRestartHarness{goos: "linux", inv: sudoInvoker{name: "op", uid: 1000, home: home}}
	h.install(t)

	var out bytes.Buffer
	if err := restartAfterUpdate(context.Background(), "/root/.axis/axis.sock", []string{"/usr/local/bin/axis"}, &out); err != nil {
		t.Fatalf("restartAfterUpdate: %v\n%s", err, out.String())
	}
	want := []string{
		"systemctl --user --machine=op@ is-active --quiet " + daemonSystemdUnit,
		"systemctl --user --machine=op@ restart " + daemonSystemdUnit,
	}
	if got := h.calls; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("service-manager calls:\n got %q\nwant %q", got, want)
	}
	if len(h.healthAddrs) == 0 || h.healthAddrs[0] != "/srv/op/axis.sock" {
		t.Fatalf("expected /health poll on the unit's --addr, got %q", h.healthAddrs)
	}
	if h.metaFetches != 0 || h.spawns != 0 {
		t.Fatalf("sudo path must not use root's token or spawn: metaFetches=%d spawns=%d", h.metaFetches, h.spawns)
	}
	if !strings.Contains(out.String(), "serving current version") {
		t.Fatalf("expected ready message, got %q", out.String())
	}
}

// A managed unit without --addr is polled on the invoker's default socket,
// never on root's.
func TestUpdateUnderSudoPollsInvokerDefaultSocket(t *testing.T) {
	home := t.TempDir()
	writeManagedUnit(t, home, "/usr/local/bin/axis daemon start")
	h := &sudoRestartHarness{goos: "linux", inv: sudoInvoker{name: "op", uid: 1000, home: home}}
	h.install(t)

	var out bytes.Buffer
	if err := restartAfterUpdate(context.Background(), "/root/.axis/axis.sock", []string{"/usr/local/bin/axis"}, &out); err != nil {
		t.Fatalf("restartAfterUpdate: %v\n%s", err, out.String())
	}
	if want := filepath.Join(home, ".axis", "axis.sock"); len(h.healthAddrs) == 0 || h.healthAddrs[0] != want {
		t.Fatalf("expected poll on %s, got %q", want, h.healthAddrs)
	}
}

// Without an active service for the invoker, the update must not start
// `axis serve` as root: that daemon would contend with the user's for the
// mesh port and outlive the update unsupervised.
func TestUpdateUnderSudoWithoutInvokerServiceNeverSpawnsRootDaemon(t *testing.T) {
	h := &sudoRestartHarness{goos: "linux", inv: sudoInvoker{name: "op", uid: 1000, home: t.TempDir()}}
	h.install(t)

	var out bytes.Buffer
	if err := restartAfterUpdate(context.Background(), "/root/.axis/axis.sock", []string{"/usr/local/bin/axis"}, &out); err != nil {
		t.Fatalf("a skipped restart must not fail the update: %v", err)
	}
	if h.spawns != 0 || h.metaFetches != 0 {
		t.Fatalf("no root daemon may be started: spawns=%d metaFetches=%d", h.spawns, h.metaFetches)
	}
	if msg := out.String(); !strings.Contains(msg, "not starting a daemon as root") || !strings.Contains(msg, "Restart it as op") {
		t.Fatalf("expected an actionable skip message, got %q", msg)
	}
}

func TestUpdateUnderSudoLookupFailureNeverSpawnsRootDaemon(t *testing.T) {
	h := &sudoRestartHarness{goos: "linux", lookupErr: errors.New("look up sudo user op: unknown userid 1000")}
	h.install(t)

	var out bytes.Buffer
	if err := restartAfterUpdate(context.Background(), "/root/.axis/axis.sock", []string{"/usr/local/bin/axis"}, &out); err != nil {
		t.Fatalf("a skipped restart must not fail the update: %v", err)
	}
	if h.spawns != 0 || len(h.calls) != 0 {
		t.Fatalf("expected no restart attempt: spawns=%d calls=%q", h.spawns, h.calls)
	}
	if !strings.Contains(out.String(), "unknown userid") {
		t.Fatalf("expected the lookup error in the warning, got %q", out.String())
	}
}

// launchd needs no manager rewrite: root may address the invoker's GUI domain.
func TestUpdateUnderSudoKickstartsInvokerLaunchdAgent(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	plist, err := renderDaemonService("darwin", "/usr/local/bin/axis", "/Users/op/.axis/axis.sock", "1m", home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, daemonLaunchdLabel+".plist"), plist, 0o644); err != nil {
		t.Fatal(err)
	}
	h := &sudoRestartHarness{goos: "darwin", inv: sudoInvoker{name: "op", uid: 501, home: home}}
	h.install(t)

	var out bytes.Buffer
	if err := restartAfterUpdate(context.Background(), "/var/root/.axis/axis.sock", []string{"/usr/local/bin/axis"}, &out); err != nil {
		t.Fatalf("restartAfterUpdate: %v\n%s", err, out.String())
	}
	want := "launchctl kickstart -k gui/501/" + daemonLaunchdLabel
	if len(h.calls) != 2 || h.calls[1] != want {
		t.Fatalf("expected %q after the print probe, got %q", want, h.calls)
	}
	if len(h.healthAddrs) == 0 || h.healthAddrs[0] != "/Users/op/.axis/axis.sock" {
		t.Fatalf("expected poll on the plist's --addr, got %q", h.healthAddrs)
	}
}

func TestForSudoInvokerLeavesOtherCommandsAlone(t *testing.T) {
	var got []string
	deps := daemonServiceDependencies{
		goos: "linux",
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			got = append(got, name+" "+strings.Join(args, " "))
			return nil, nil
		},
	}.forSudoInvoker(sudoInvoker{name: "op", uid: 1000, home: "/home/op"})

	_, _ = deps.run(context.Background(), "systemctl", "is-active", "docker.service")
	_, _ = deps.run(context.Background(), "launchctl", "print", "gui/1000")
	want := []string{"systemctl is-active docker.service", "launchctl print gui/1000"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got %q, want %q", got, want)
	}
	if home, _ := deps.homeDir(); home != "/home/op" || deps.uid() != 1000 {
		t.Fatalf("expected invoker home and uid, got %q %d", home, deps.uid())
	}
}

func TestLookupSudoInvokerOutsideRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	t.Setenv("SUDO_USER", "op")
	t.Setenv("SUDO_UID", "1000")
	if _, ok, err := lookupSudoInvoker(); ok || err != nil {
		t.Fatalf("a non-root process is never under sudo: ok=%v err=%v", ok, err)
	}
}
