package main

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderLaunchAgentUsesExactArguments(t *testing.T) {
	got, err := renderDaemonService("darwin", "/usr/local/bin/axis", "/Users/operator/.axis/axis.sock", "1m", "/Users/operator")
	if err != nil {
		t.Fatalf("renderDaemonService: %v", err)
	}
	for _, want := range []string{
		"<string>/usr/local/bin/axis</string>",
		"<string>daemon</string>",
		"<string>start</string>",
		"<string>/Users/operator/.axis/axis.sock</string>",
		"<string>1m</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<key>HOME</key><string>/Users/operator</string>",
		"/Users/operator/Library/Logs/Axis/daemon.stdout.log",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("launch agent missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(string(got), "sh -c") {
		t.Fatalf("launch agent must not invoke a shell:\n%s", got)
	}
	decoder := xml.NewDecoder(strings.NewReader(string(got)))
	for {
		if _, err := decoder.Token(); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("launch agent is not valid XML: %v\n%s", err, got)
		}
	}
}

func TestRenderSystemdUserServiceUsesExactArguments(t *testing.T) {
	got, err := renderDaemonService("linux", "/usr/local/bin/axis", "/home/operator/.axis/axis.sock", "1m", "/home/operator")
	if err != nil {
		t.Fatalf("renderDaemonService: %v", err)
	}
	for _, want := range []string{
		"ExecStart=/usr/local/bin/axis daemon start --addr /home/operator/.axis/axis.sock --refresh 1m",
		"Restart=on-failure",
		"WantedBy=default.target",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("systemd unit missing %q:\n%s", want, got)
		}
	}
}

func TestInstallDaemonServiceWritesAndActivatesSystemdUserUnit(t *testing.T) {
	home := t.TempDir()
	var calls [][]string
	deps := daemonServiceDependencies{
		goos:       "linux",
		homeDir:    func() (string, error) { return home, nil },
		executable: func() (string, error) { return "/opt/axis/bin/axis", nil },
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{name}, args...))
			return nil, nil
		},
	}

	path, err := installDaemonService(context.Background(), "/tmp/axis.sock", "2m", deps)
	if err != nil {
		t.Fatalf("installDaemonService: %v", err)
	}
	wantPath := filepath.Join(home, ".config", "systemd", "user", "axis.service")
	if path != wantPath {
		t.Fatalf("path = %q, want %q", path, wantPath)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("mode = %o, want 644", got)
	}
	if len(calls) != 3 || strings.Join(calls[0], " ") != "systemctl --user daemon-reload" || strings.Join(calls[1], " ") != "systemctl --user enable axis.service" || strings.Join(calls[2], " ") != "systemctl --user restart axis.service" {
		t.Fatalf("unexpected activation calls: %#v", calls)
	}
}

func TestInstallDaemonServiceRefusesUnmanagedFile(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "systemd", "user", "axis.service")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const custom = "[Service]\nExecStart=/custom/axis\n"
	if err := os.WriteFile(path, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	deps := daemonServiceDependencies{
		goos:       "linux",
		homeDir:    func() (string, error) { return home, nil },
		executable: func() (string, error) { return "/opt/axis/bin/axis", nil },
		run: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("service manager must not run for unmanaged file")
			return nil, nil
		},
	}

	if _, err := installDaemonService(context.Background(), "/tmp/axis.sock", "1m", deps); err == nil || !strings.Contains(err.Error(), "not managed by AXIS") {
		t.Fatalf("expected unmanaged-file refusal, got %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != custom {
		t.Fatalf("custom service changed:\n%s", got)
	}
}

func TestRenderDaemonServiceRejectsUnsupportedPlatform(t *testing.T) {
	if _, err := renderDaemonService("windows", "axis.exe", "axis.sock", "1m", "C:\\Users\\operator"); err == nil {
		t.Fatal("expected unsupported platform error")
	}
}

func TestInstallDaemonServiceRejectsInvalidRefreshBeforeWriting(t *testing.T) {
	home := t.TempDir()
	deps := daemonServiceDependencies{
		goos:       "linux",
		homeDir:    func() (string, error) { return home, nil },
		executable: func() (string, error) { return "/opt/axis/bin/axis", nil },
	}
	if _, err := installDaemonService(context.Background(), "/tmp/axis.sock", "never", deps); err == nil {
		t.Fatal("expected invalid refresh error")
	}
	path, _ := daemonServicePath("linux", home)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid install wrote service, stat err=%v", err)
	}
}

func TestDaemonServiceInstallPropagatesWriterFailureAfterInstalling(t *testing.T) {
	home := t.TempDir()
	deps := daemonServiceDependencies{
		goos:       "linux",
		homeDir:    func() (string, error) { return home, nil },
		executable: func() (string, error) { return "/opt/axis/bin/axis", nil },
		run:        func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
	}
	wantErr := errors.New("writer unavailable")

	err := runDaemonServiceInstall(context.Background(), rejectingOutputWriter{err: wantErr}, "/tmp/axis.sock", "1m", deps)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want writer failure", err)
	}
	path, _ := daemonServicePath("linux", home)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("service was not installed before reporting failure: %v", err)
	}
}

func TestDaemonServiceStatusPreservesManagerAndWriterFailures(t *testing.T) {
	managerErr := errors.New("manager unavailable")
	writerErr := errors.New("writer unavailable")
	deps := daemonServiceDependencies{
		goos: "linux",
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("service failed\n"), managerErr
		},
	}

	err := runDaemonServiceStatus(context.Background(), rejectingOutputWriter{err: writerErr}, deps)
	if !errors.Is(err, managerErr) || !errors.Is(err, writerErr) {
		t.Fatalf("error = %v, want manager and writer failures", err)
	}
}

func TestDaemonServiceUninstallPropagatesWriterFailureAfterRemoving(t *testing.T) {
	home := t.TempDir()
	deps := daemonServiceDependencies{
		goos:       "linux",
		homeDir:    func() (string, error) { return home, nil },
		executable: func() (string, error) { return "/opt/axis/bin/axis", nil },
		run:        func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
	}
	if _, err := installDaemonService(context.Background(), "/tmp/axis.sock", "1m", deps); err != nil {
		t.Fatalf("fixture install: %v", err)
	}
	wantErr := errors.New("writer unavailable")

	err := runDaemonServiceUninstall(context.Background(), rejectingOutputWriter{err: wantErr}, deps)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want writer failure", err)
	}
	path, _ := daemonServicePath("linux", home)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("service was not removed before reporting failure: %v", err)
	}
}
