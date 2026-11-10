package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

type bootStartReply struct {
	out string
	err error
}

// bootStartDeps returns deps with a managed service file under a temp HOME
// (unless marker is false) and a run stub answering by command line.
func bootStartDeps(t *testing.T, goos string, managed bool, replies map[string]bootStartReply) (daemonServiceDependencies, *[]string) {
	t.Helper()
	home := t.TempDir()
	path, err := daemonServicePath(goos, home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "# unrelated service\n"
	if managed {
		content = "# " + daemonServiceMarker + ".\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls []string
	return daemonServiceDependencies{
		goos:       goos,
		homeDir:    func() (string, error) { return home, nil },
		executable: func() (string, error) { return "/opt/axis/bin/axis", nil },
		uid:        func() int { return 1000 },
		userName:   func() (string, error) { return "operator", nil },
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			line := strings.Join(append([]string{name}, args...), " ")
			calls = append(calls, line)
			r, ok := replies[line]
			if !ok {
				return nil, nil
			}
			return []byte(r.out), r.err
		},
	}, &calls
}

const (
	isEnabledCmd = "systemctl --user is-enabled axis.service"
	lingerCmd    = "loginctl show-user 1000 --property=Linger --value"
	autoLoginCmd = "defaults read /Library/Preferences/com.apple.loginwindow autoLoginUser"
)

var errExit1 = errors.New("exit status 1")

func TestDaemonBootStartCheck(t *testing.T) {
	for _, tc := range []struct {
		name        string
		goos        string
		managed     bool
		replies     map[string]bootStartReply
		wantNil     bool
		wantStatus  string
		wantMessage string
		wantFix     string
	}{
		{name: "linux linger on", goos: "linux", managed: true,
			replies:    map[string]bootStartReply{isEnabledCmd: {out: "enabled\n"}, lingerCmd: {out: "yes\n"}},
			wantStatus: "pass", wantMessage: "starts at boot"},
		{name: "linux linger off", goos: "linux", managed: true,
			replies:    map[string]bootStartReply{isEnabledCmd: {out: "enabled\n"}, lingerCmd: {out: "no\n"}},
			wantStatus: "warn", wantMessage: "login only", wantFix: "loginctl enable-linger $USER"},
		{name: "linux unit disabled", goos: "linux", managed: true,
			replies: map[string]bootStartReply{isEnabledCmd: {out: "disabled\n", err: errExit1}},
			wantNil: true},
		{name: "linux loginctl error", goos: "linux", managed: true,
			replies:    map[string]bootStartReply{isEnabledCmd: {out: "enabled\n"}, lingerCmd: {out: "Failed to get user: User ID 1000 is not logged in or lingering", err: errExit1}},
			wantStatus: "warn", wantMessage: "could not read lingering state"},
		{name: "linux loginctl odd output", goos: "linux", managed: true,
			replies:    map[string]bootStartReply{isEnabledCmd: {out: "enabled\n"}, lingerCmd: {out: "maybe\n"}},
			wantStatus: "warn", wantMessage: "could not read lingering state"},
		{name: "linux unmanaged file", goos: "linux", managed: false, wantNil: true},
		{name: "darwin auto-login for this user", goos: "darwin", managed: true,
			replies:    map[string]bootStartReply{autoLoginCmd: {out: "operator\n"}},
			wantStatus: "pass", wantMessage: "automatic login is set for this user"},
		{name: "darwin auto-login for another user", goos: "darwin", managed: true,
			replies:    map[string]bootStartReply{autoLoginCmd: {out: "someone-else\n"}},
			wantStatus: "warn", wantMessage: "not this user"},
		{name: "darwin no auto-login", goos: "darwin", managed: true,
			replies:    map[string]bootStartReply{autoLoginCmd: {out: "The domain/default pair of (/Library/Preferences/com.apple.loginwindow, autoLoginUser) does not exist\n", err: errExit1}},
			wantStatus: "warn", wantMessage: "starts at login only", wantFix: "LaunchDaemon"},
		{name: "darwin defaults other error", goos: "darwin", managed: true,
			replies:    map[string]bootStartReply{autoLoginCmd: {err: errors.New("exec: \"defaults\": executable file not found")}},
			wantStatus: "warn", wantMessage: "could not read automatic-login state"},
		{name: "darwin unmanaged file", goos: "darwin", managed: false, wantNil: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, calls := bootStartDeps(t, tc.goos, tc.managed, tc.replies)
			got := daemonBootStartCheck(context.Background(), deps)
			if !tc.managed && len(*calls) != 0 {
				t.Fatalf("unmanaged service ran commands: %q", *calls)
			}
			if tc.wantNil {
				if got != nil {
					t.Fatalf("got %+v, want nil", got)
				}
				return
			}
			if got == nil || got.Status != tc.wantStatus || !strings.Contains(got.Message, tc.wantMessage) || !strings.Contains(got.Fix, tc.wantFix) {
				t.Fatalf("got %+v, want status %q message ~%q fix ~%q", got, tc.wantStatus, tc.wantMessage, tc.wantFix)
			}
		})
	}
}

func TestDaemonBootStartCheckNoServiceRunsNothing(t *testing.T) {
	var calls int
	deps := daemonServiceDependencies{
		goos:    "linux",
		homeDir: func() (string, error) { return t.TempDir(), nil },
		uid:     func() int { return 1000 },
		run: func(context.Context, string, ...string) ([]byte, error) {
			calls++
			return nil, nil
		},
	}
	if got := daemonBootStartCheck(context.Background(), deps); got != nil || calls != 0 {
		t.Fatalf("got %+v with %d calls, want nil and 0", got, calls)
	}
}

func TestDaemonBootStartCheckTimesOut(t *testing.T) {
	prev := bootStartProbeTimeout
	bootStartProbeTimeout = 20 * time.Millisecond
	defer func() { bootStartProbeTimeout = prev }()
	deps, _ := bootStartDeps(t, "linux", true, nil)
	deps.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" {
			return []byte("enabled\n"), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	start := time.Now()
	got := daemonBootStartCheck(context.Background(), deps)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("check took %v, want bounded by the probe timeout", elapsed)
	}
	if got == nil || got.Status != "warn" || !strings.Contains(got.Message, "could not read lingering state") {
		t.Fatalf("got %+v, want could-not-read warning", got)
	}
}

func TestDaemonServiceInstallPrintsBootStartWarning(t *testing.T) {
	for _, tc := range []struct {
		name     string
		goos     string
		replies  map[string]bootStartReply
		wantWarn string
	}{
		{name: "linux linger off", goos: "linux",
			replies:  map[string]bootStartReply{isEnabledCmd: {out: "enabled\n"}, lingerCmd: {out: "no\n"}},
			wantWarn: "Fix: loginctl enable-linger $USER"},
		{name: "linux linger on", goos: "linux",
			replies: map[string]bootStartReply{isEnabledCmd: {out: "enabled\n"}, lingerCmd: {out: "yes\n"}}},
		{name: "linux loginctl error", goos: "linux",
			replies:  map[string]bootStartReply{isEnabledCmd: {out: "enabled\n"}, lingerCmd: {err: errExit1}},
			wantWarn: "Warning: could not read lingering state"},
		{name: "darwin no auto-login", goos: "darwin",
			replies:  map[string]bootStartReply{autoLoginCmd: {out: "does not exist", err: errExit1}},
			wantWarn: "Warning: LaunchAgent starts at login only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, _ := bootStartDeps(t, tc.goos, true, tc.replies)
			var out bytes.Buffer
			if err := runDaemonServiceInstall(context.Background(), &out, "/tmp/axis.sock", "1m", deps); err != nil {
				t.Fatalf("install must not fail on the boot-start check: %v", err)
			}
			if !strings.Contains(out.String(), "AXIS daemon user service installed:") {
				t.Fatalf("missing success line:\n%s", out.String())
			}
			if tc.wantWarn == "" {
				if strings.Contains(out.String(), "Warning:") && strings.Contains(out.String(), "login") {
					t.Fatalf("unexpected boot-start warning:\n%s", out.String())
				}
				return
			}
			if !strings.Contains(out.String(), tc.wantWarn) {
				t.Fatalf("output lacks %q:\n%s", tc.wantWarn, out.String())
			}
		})
	}
}

func TestDoctorShowsBootStartCheck(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "nodes.yaml")
	defer stubDoctorConfigPath(t, func() string { return tmpFile })()
	defer stubDoctorOwnershipHealthy(t)()
	defer stubDoctorConfigLoader(t, func(string) (*config.Config, error) {
		return &config.Config{Nodes: []config.NodeConfig{{Name: "alpha", Hostname: "10.0.0.1", SSHUser: "axis"}}}, nil
	})()
	defer stubDoctorSSHChecker(t, func(context.Context, config.NodeConfig) error { return nil })()
	defer stubStatusCachedLoader(t, func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{Nodes: []models.NodeFacts{{Name: "alpha"}}}, "cached", nil
	})()
	doctorProbeBootStart = func(context.Context) *DoctorCheck {
		return &DoctorCheck{Name: "Daemon boot start", Status: "warn", Message: "user service starts at login only (lingering off)", Fix: "loginctl enable-linger $USER"}
	}

	stdout, _, _ := captureProcessOutput(t, func() error {
		cmd := doctorCmd()
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		cmd.SetArgs(nil)
		return cmd.Execute()
	})
	stdout = stripANSI(stdout)
	if !strings.Contains(stdout, "Daemon boot start") || !strings.Contains(stdout, "loginctl enable-linger $USER") {
		t.Fatalf("doctor output lacks the boot-start check:\n%s", stdout)
	}
}
