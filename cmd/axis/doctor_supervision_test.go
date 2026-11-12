package main

import (
	"strings"
	"testing"
)

func TestParseSystemdCgroup(t *testing.T) {
	for _, tc := range []struct {
		name, content, want string
	}{
		{"unified", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/axis.service\n",
			"/user.slice/user-1000.slice/user@1000.service/app.slice/axis.service"},
		{"legacy v1", "12:pids:/user.slice\n1:name=systemd:/system.slice/axis.service\n",
			"/system.slice/axis.service"},
		{"hybrid prefers unified", "1:name=systemd:/user.slice/session-4.scope\n0::/system.slice/axis.service\n",
			"/system.slice/axis.service"},
		{"container namespace", "0::/\n", "/"},
		{"no systemd hierarchy", "3:cpu,cpuacct:/foo\n", ""},
		{"empty", "", ""},
	} {
		if got := parseSystemdCgroup(tc.content); got != tc.want {
			t.Errorf("%s: parseSystemdCgroup = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestDaemonServiceUnit(t *testing.T) {
	for cgroup, want := range map[string]string{
		"/user.slice/user-1000.slice/user@1000.service/app.slice/axis.service": "axis.service",
		"/system.slice/axis-daemon.service":                                    "axis-daemon.service",
		"/user.slice/user-1000.slice/session-5.scope":                          "",
		"/user.slice/user-1000.slice/user@1000.service/init.scope":             "",
		"/": "",
		"":  "",
	} {
		if got := daemonServiceUnit(cgroup); got != want {
			t.Errorf("daemonServiceUnit(%q) = %q, want %q", cgroup, got, want)
		}
	}
}

func TestDaemonSupervisionCheck(t *testing.T) {
	const userUnit = "/user.slice/user-1000.slice/user@1000.service/app.slice/axis.service"
	for _, tc := range []struct {
		name       string
		owners     []doctorDaemonProc
		wantOK     bool
		wantStatus string
		wantIn     []string
	}{
		{"user service", []doctorDaemonProc{{PID: 10, Cgroup: userUnit}}, true, "pass", []string{"pid 10", "axis.service"}},
		{"system service", []doctorDaemonProc{{PID: 11, Cgroup: "/system.slice/axis-daemon.service"}}, true, "pass", []string{"axis-daemon.service"}},
		{"hand-started in a login session", []doctorDaemonProc{{PID: 12, Cgroup: "/user.slice/user-1000.slice/session-5.scope"}},
			true, "warn", []string{"pid 12", "session-5.scope", "reboot"}},
		{"inside an unrelated service", []doctorDaemonProc{{PID: 13, Cgroup: "/system.slice/ssh.service"}},
			true, "warn", []string{"pid 13", "inside ssh.service"}},
		{"one of two unsupervised", []doctorDaemonProc{{PID: 14, Cgroup: userUnit}, {PID: 15, Cgroup: "/user.slice/user-1000.slice/session-2.scope"}},
			true, "warn", []string{"pid 15"}},
		{"container namespace hides the unit", []doctorDaemonProc{{PID: 16, Cgroup: "/"}}, false, "", nil},
		{"cgroup not read", []doctorDaemonProc{{PID: 17}}, false, "", nil},
		{"no owners", nil, false, "", nil},
	} {
		check, ok := daemonSupervisionCheck(tc.owners)
		if ok != tc.wantOK {
			t.Errorf("%s: ok = %v, want %v (check %+v)", tc.name, ok, tc.wantOK, check)
			continue
		}
		if !ok {
			continue
		}
		if check.Status != tc.wantStatus {
			t.Errorf("%s: status = %q, want %q", tc.name, check.Status, tc.wantStatus)
		}
		for _, want := range tc.wantIn {
			if !strings.Contains(check.Message, want) {
				t.Errorf("%s: message %q missing %q", tc.name, check.Message, want)
			}
		}
		if tc.wantStatus == "warn" && !strings.Contains(check.Fix, "axis daemon service install") {
			t.Errorf("%s: fix %q must name axis daemon service install", tc.name, check.Fix)
		}
	}
}

// TestDaemonOwnershipReportsUnsupervisedDaemon is the regression for a daemon
// started by hand on a host with no AXIS service: doctor used to report it as
// healthy, with nothing saying it would not survive a reboot.
func TestDaemonOwnershipReportsUnsupervisedDaemon(t *testing.T) {
	procs := []doctorDaemonProc{{PID: 2265, Exe: "/usr/local/bin/axis", Addr: testSock, Cgroup: "/user.slice/user-1000.slice/session-5.scope"}}
	check := findCheck(t, daemonOwnershipChecks(procs, nil, ownedSocket(), freeMesh()), "Daemon supervision")
	if check.Status != "warn" {
		t.Fatalf("status = %q, want warn", check.Status)
	}
}

func TestDaemonOwnershipSkipsSupervisionWithoutCgroup(t *testing.T) {
	procs := []doctorDaemonProc{{PID: 2265, Exe: "/usr/local/bin/axis", Addr: testSock}}
	if hasCheck(daemonOwnershipChecks(procs, nil, ownedSocket(), freeMesh()), "Daemon supervision") {
		t.Fatal("supervision must not be reported when the cgroup was not observed")
	}
}
