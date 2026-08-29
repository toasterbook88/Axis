package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/config"
)

const testSock = "/tmp/axis-test/axis.sock"

func ownedSocket() doctorSocketState {
	return doctorSocketState{
		Path:       testSock,
		Unix:       true,
		Exists:     true,
		IsSocket:   true,
		Live:       true,
		LockPath:   testSock + ".lock",
		LockExists: true,
	}
}

func freeMesh() doctorMeshPortState {
	return doctorMeshPortState{Port: 42426, Enabled: true}
}

// findCheck returns the check with the given name, failing if it is absent.
func findCheck(t *testing.T, checks []DoctorCheck, name string) DoctorCheck {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	var names []string
	for _, c := range checks {
		names = append(names, c.Name)
	}
	t.Fatalf("check %q not found; have %v", name, names)
	return DoctorCheck{}
}

func hasCheck(checks []DoctorCheck, name string) bool {
	for _, c := range checks {
		if c.Name == name {
			return true
		}
	}
	return false
}

func TestDaemonOwnershipHealthySingleDaemon(t *testing.T) {
	procs := []doctorDaemonProc{{PID: 2265, Exe: "/usr/local/bin/axis", Addr: testSock}}
	checks := daemonOwnershipChecks(procs, nil, ownedSocket(), freeMesh())

	for name, want := range map[string]string{
		"Daemon processes":  "pass",
		"Daemon executable": "pass",
		"Daemon socket":     "pass",
		"Mesh UDP port":     "pass",
	} {
		if got := findCheck(t, checks, name).Status; got != want {
			t.Errorf("%s: status = %q, want %q", name, got, want)
		}
	}
	if msg := findCheck(t, checks, "Daemon processes").Message; !strings.Contains(msg, "2265") {
		t.Errorf("expected owner pid in message, got %q", msg)
	}
}

func TestDaemonOwnershipTwoDaemonsFail(t *testing.T) {
	procs := []doctorDaemonProc{
		{PID: 2265, Exe: "/usr/local/bin/axis", Addr: testSock},
		{PID: 9001, Exe: "/home/dev/axis/axis", Addr: testSock},
	}
	check := findCheck(t, daemonOwnershipChecks(procs, nil, ownedSocket(), freeMesh()), "Daemon processes")
	if check.Status != "fail" {
		t.Fatalf("status = %q, want fail", check.Status)
	}
	if !strings.Contains(check.Message, "2265") || !strings.Contains(check.Message, "9001") {
		t.Errorf("expected both pids in message, got %q", check.Message)
	}
}

// A daemon on a different address is not a duplicate owner.
func TestDaemonOwnershipIgnoresOtherAddresses(t *testing.T) {
	procs := []doctorDaemonProc{
		{PID: 2265, Exe: "/usr/local/bin/axis", Addr: testSock},
		{PID: 9001, Exe: "/usr/local/bin/axis", Addr: "127.0.0.1:42425"},
	}
	check := findCheck(t, daemonOwnershipChecks(procs, nil, ownedSocket(), freeMesh()), "Daemon processes")
	if check.Status != "pass" {
		t.Fatalf("status = %q, want pass (%s)", check.Status, check.Message)
	}
	if strings.Contains(check.Message, "9001") {
		t.Errorf("daemon on another address should not be reported: %q", check.Message)
	}
}

func TestDaemonOwnershipDeletedExecutableFails(t *testing.T) {
	procs := []doctorDaemonProc{{PID: 2265, Exe: "/usr/local/bin/axis", Deleted: true, Addr: testSock}}
	check := findCheck(t, daemonOwnershipChecks(procs, nil, ownedSocket(), freeMesh()), "Daemon executable")
	if check.Status != "fail" {
		t.Fatalf("status = %q, want fail", check.Status)
	}
	if !strings.Contains(check.Message, "deleted") || !strings.Contains(check.Message, "2265") {
		t.Errorf("unexpected message %q", check.Message)
	}
	if check.Fix == "" {
		t.Error("expected a remediation hint")
	}
}

func TestDaemonOwnershipUnresolvedExecutableWarns(t *testing.T) {
	procs := []doctorDaemonProc{{PID: 2265, Addr: testSock}}
	check := findCheck(t, daemonOwnershipChecks(procs, nil, ownedSocket(), freeMesh()), "Daemon executable")
	if check.Status != "warn" {
		t.Fatalf("status = %q, want warn", check.Status)
	}
}

func TestDaemonOwnershipStaleSocketWarns(t *testing.T) {
	sock := ownedSocket()
	sock.Live = false
	checks := daemonOwnershipChecks(nil, nil, sock, freeMesh())
	check := findCheck(t, checks, "Daemon socket")
	if check.Status != "warn" {
		t.Fatalf("status = %q, want warn", check.Status)
	}
	if !strings.Contains(check.Message, "stale") {
		t.Errorf("unexpected message %q", check.Message)
	}
	// Nothing is running, so there is no executable to report on.
	if hasCheck(checks, "Daemon executable") {
		t.Error("expected no executable check without a daemon process")
	}
}

func TestDaemonOwnershipMissingSocketWarns(t *testing.T) {
	sock := doctorSocketState{Path: testSock, Unix: true, LockPath: testSock + ".lock"}
	check := findCheck(t, daemonOwnershipChecks(nil, nil, sock, freeMesh()), "Daemon socket")
	if check.Status != "warn" {
		t.Fatalf("status = %q, want warn", check.Status)
	}
	if !strings.Contains(check.Message, "no socket") {
		t.Errorf("unexpected message %q", check.Message)
	}
}

func TestDaemonOwnershipNonSocketFileFails(t *testing.T) {
	sock := ownedSocket()
	sock.IsSocket = false
	sock.Live = false
	check := findCheck(t, daemonOwnershipChecks(nil, nil, sock, freeMesh()), "Daemon socket")
	if check.Status != "fail" {
		t.Fatalf("status = %q, want fail", check.Status)
	}
}

// A live socket with no lock file means the owner predates #366 and cannot
// exclude a second daemon — the operator needs to see that.
func TestDaemonOwnershipLiveSocketWithoutLockWarns(t *testing.T) {
	sock := ownedSocket()
	sock.LockExists = false
	procs := []doctorDaemonProc{{PID: 2265, Exe: "/usr/local/bin/axis", Addr: testSock}}
	check := findCheck(t, daemonOwnershipChecks(procs, nil, sock, freeMesh()), "Daemon socket")
	if check.Status != "warn" {
		t.Fatalf("status = %q, want warn", check.Status)
	}
	if !strings.Contains(check.Message, "advisory locking") {
		t.Errorf("unexpected message %q", check.Message)
	}
}

func TestDaemonOwnershipLiveSocketWithNoProcessWarns(t *testing.T) {
	check := findCheck(t, daemonOwnershipChecks(nil, nil, ownedSocket(), freeMesh()), "Daemon socket")
	if check.Status != "warn" {
		t.Fatalf("status = %q, want warn", check.Status)
	}
	if !strings.Contains(check.Message, "no matching axis daemon process") {
		t.Errorf("unexpected message %q", check.Message)
	}
}

func TestDaemonOwnershipUnsupportedPlatformWarnsWithoutClaiming(t *testing.T) {
	checks := daemonOwnershipChecks(nil, errOwnershipProbeUnsupported, ownedSocket(), freeMesh())
	if got := findCheck(t, checks, "Daemon processes").Status; got != "warn" {
		t.Fatalf("status = %q, want warn", got)
	}
	// Process state is unknown, so a live socket must not be downgraded for
	// having "no process".
	if got := findCheck(t, checks, "Daemon socket").Status; got != "pass" {
		t.Errorf("socket status = %q, want pass when process state is unknown", got)
	}
}

func TestDaemonOwnershipProbeErrorWarns(t *testing.T) {
	checks := daemonOwnershipChecks(nil, errors.New("boom"), ownedSocket(), freeMesh())
	check := findCheck(t, checks, "Daemon processes")
	if check.Status != "warn" || !strings.Contains(check.Message, "boom") {
		t.Fatalf("unexpected check %+v", check)
	}
}

func TestMeshPortCheck(t *testing.T) {
	owner := []doctorDaemonProc{{PID: 2265, Exe: "/usr/local/bin/axis", Addr: testSock}}
	tests := []struct {
		name    string
		state   doctorMeshPortState
		owners  []doctorDaemonProc
		known   bool
		want    string
		wantSub string
	}{
		{"free", doctorMeshPortState{Port: 42426, Enabled: true}, owner, true, "pass", "free"},
		{"bound by our daemon", doctorMeshPortState{Port: 42426, Enabled: true, Bound: true}, owner, true, "pass", "consistent"},
		{"bound by a stranger", doctorMeshPortState{Port: 42426, Enabled: true, Bound: true}, nil, true, "warn", "another process"},
		{"disabled", doctorMeshPortState{Port: 42426}, nil, true, "pass", "mesh disabled"},
		{"probe error", doctorMeshPortState{Port: 42426, Enabled: true, Err: errors.New("nope")}, owner, true, "warn", "probe failed"},
		{
			"two daemons",
			doctorMeshPortState{Port: 42426, Enabled: true, Bound: true},
			append(owner, doctorDaemonProc{PID: 9001, Addr: testSock}),
			true, "warn", "ambiguous",
		},
		{"bound, process state unknown", doctorMeshPortState{Port: 42426, Enabled: true, Bound: true}, nil, false, "pass", "consistent"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := meshPortCheck(tc.state, tc.owners, tc.known)
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q (%s)", got.Status, tc.want, got.Message)
			}
			if !strings.Contains(got.Message, tc.wantSub) {
				t.Errorf("message %q missing %q", got.Message, tc.wantSub)
			}
		})
	}
}

func TestDaemonServeAddr(t *testing.T) {
	tests := []struct {
		name     string
		argv     []string
		wantAddr string
		wantOK   bool
	}{
		{
			"daemon start with explicit addr",
			[]string{"/home/cranium/axis/axis", "daemon", "start", "--addr", "/home/cranium/.axis/axis.sock", "--refresh", "1m0s"},
			"/home/cranium/.axis/axis.sock", true,
		},
		{
			"serve with --addr=",
			[]string{"/usr/local/bin/axis", "serve", "--addr=127.0.0.1:42425"},
			"127.0.0.1:42425", true,
		},
		{"serve default addr", []string{"axis", "serve"}, api.DefaultAddr(), true},
		{"serve behind a global flag", []string{"axis", "--no-color", "serve"}, api.DefaultAddr(), true},
		{"mcp serve binds nothing", []string{"/home/cranium/.local/bin/axis", "mcp", "serve", "--cached"}, "", false},
		{"daemon status is not a daemon", []string{"axis", "daemon", "status"}, "", false},
		{"unrelated binary", []string{"/usr/bin/axisd", "serve"}, "", false},
		{"bare axis", []string{"axis"}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			addr, ok := daemonServeAddr(tc.argv)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && addr != tc.wantAddr {
				t.Errorf("addr = %q, want %q", addr, tc.wantAddr)
			}
		})
	}
}

func TestSplitDeletedExe(t *testing.T) {
	if path, deleted := splitDeletedExe("/usr/local/bin/axis (deleted)"); !deleted || path != "/usr/local/bin/axis" {
		t.Errorf("got (%q, %v)", path, deleted)
	}
	if path, deleted := splitDeletedExe("/usr/local/bin/axis"); deleted || path != "/usr/local/bin/axis" {
		t.Errorf("got (%q, %v)", path, deleted)
	}
}

func TestSplitCmdline(t *testing.T) {
	got := splitCmdline([]byte("axis\x00serve\x00--addr\x00/tmp/s.sock\x00"))
	want := []string{"axis", "serve", "--addr", "/tmp/s.sock"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %v, want %v", got, want)
	}
}

// probeDaemonSocket must distinguish a live listener, a leftover socket file,
// a non-socket squatter, and a missing path — against real files.
func TestProbeDaemonSocketAgainstRealPaths(t *testing.T) {
	dir := t.TempDir()

	missing := filepath.Join(dir, "missing.sock")
	if st := doctorProbeSocket(missing); st.Exists || st.Live || !st.Unix {
		t.Errorf("missing: %+v", st)
	}

	liveAddr := filepath.Join(dir, "live.sock")
	ln, err := net.Listen("unix", liveAddr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	st := doctorProbeSocket(liveAddr)
	if !st.Exists || !st.IsSocket || !st.Live {
		t.Errorf("live: %+v", st)
	}
	if st.LockPath != api.LockPathFor(liveAddr) {
		t.Errorf("lock path = %q", st.LockPath)
	}
	if st.LockExists {
		t.Error("expected no lock file")
	}

	// Closing the listener without unlinking leaves exactly the stale
	// socket file the daemon has to reason about.
	staleAddr := filepath.Join(dir, "stale.sock")
	staleLn, err := net.Listen("unix", staleAddr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if ul, ok := staleLn.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	_ = staleLn.Close()
	if st := doctorProbeSocket(staleAddr); !st.Exists || !st.IsSocket || st.Live {
		t.Errorf("stale: %+v", st)
	}

	regular := filepath.Join(dir, "regular.sock")
	if err := os.WriteFile(regular, nil, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if st := doctorProbeSocket(regular); !st.Exists || st.IsSocket || st.Live {
		t.Errorf("regular file: %+v", st)
	}
}

func TestMeshUDPPortDerivation(t *testing.T) {
	if got := meshUDPPort(nil); got != 42426 {
		t.Errorf("nil config: got %d", got)
	}
	if got := meshUDPPort(&config.Config{}); got != 42426 {
		t.Errorf("empty config: got %d", got)
	}
	cfg := &config.Config{Discovery: &config.DiscoveryConfig{Enabled: true, UDPPort: 43000}}
	if got := meshUDPPort(cfg); got != 43000 {
		t.Errorf("configured port: got %d", got)
	}
}

func TestProbeMeshUDPPortDetectsBinding(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()
	port := pc.LocalAddr().(*net.UDPAddr).Port

	cfg := &config.Config{Discovery: &config.DiscoveryConfig{Enabled: true, UDPPort: port}}
	if st := doctorProbeMeshPort(cfg); !st.Bound {
		t.Errorf("expected bound port to be detected: %+v", st)
	}

	disabled := &config.Config{Discovery: &config.DiscoveryConfig{Enabled: false, UDPPort: port}}
	if st := doctorProbeMeshPort(disabled); st.Enabled || st.Bound {
		t.Errorf("mesh disabled: %+v", st)
	}
}

// The probe must not report the doctor process itself, and must survive a
// /proc it cannot fully read.
func TestListDaemonProcsExcludesSelf(t *testing.T) {
	procs, err := doctorProbeDaemonProcs()
	if errors.Is(err, errOwnershipProbeUnsupported) {
		t.Skip("process enumeration unsupported on this platform")
	}
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	for _, p := range procs {
		if p.PID == os.Getpid() {
			t.Errorf("probe reported the calling process: %+v", p)
		}
	}
}

func TestDoctorReportsDuplicateDaemons(t *testing.T) {
	restore := minimalDoctorStubs(t)
	defer restore()

	restoreProcs := stubDoctorDaemonProcs(t, func() ([]doctorDaemonProc, error) {
		return []doctorDaemonProc{
			{PID: 2265, Exe: "/usr/local/bin/axis", Addr: api.DefaultAddr()},
			{PID: 9001, Exe: "/usr/local/bin/axis", Deleted: true, Addr: api.DefaultAddr()},
		}, nil
	})
	defer restoreProcs()

	stdout, _, err := captureProcessOutput(t, func() error {
		cmd := doctorCmd()
		cmd.SetArgs(nil)
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("doctor Execute: %v", err)
	}
	stdout = stripANSI(stdout)
	for _, want := range []string{"Daemon processes", "2 axis daemons", "Daemon executable", "deleted executable"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("doctor output missing %q:\n%s", want, stdout)
		}
	}
}
