package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseRestartCounters(t *testing.T) {
	out := "Id=legacy-worker.service\nNRestarts=32751\nActiveState=activating\nSubState=auto-restart\n\n" +
		"Id=sshd.service\nNRestarts=0\nActiveState=active\nSubState=running\n\n" +
		"Id=no-counter.service\nActiveState=inactive\nSubState=dead\n"
	got := parseRestartCountersAt("system", out, -1)
	want := []unitRestarts{
		{Scope: "system", Unit: "legacy-worker.service", Restarts: 32751, Active: "activating", Sub: "auto-restart", ActiveFor: -1},
		{Scope: "system", Unit: "sshd.service", Restarts: 0, Active: "active", Sub: "running", ActiveFor: -1},
		{Scope: "system", Unit: "no-counter.service", Restarts: 0, Active: "inactive", Sub: "dead", ActiveFor: -1},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestRestartLoopCheckUnsupportedReportsNothing(t *testing.T) {
	if got := restartLoopCheck(nil, errOwnershipProbeUnsupported); got != nil {
		t.Fatalf("expected no check without systemd, got %+v", got)
	}
}

func TestRestartLoopCheckProbeErrorWarns(t *testing.T) {
	got := restartLoopCheck(nil, errors.New("system manager: exit status 1"))
	if len(got) != 1 || got[0].Status != "warn" || !strings.Contains(got[0].Message, "exit status 1") {
		t.Fatalf("unexpected %+v", got)
	}
}

func TestRestartLoopCheckBelowThresholdPasses(t *testing.T) {
	units := []unitRestarts{{Scope: "user", Unit: "flaky-tunnel.service", Restarts: restartLoopThreshold, Active: "active", Sub: "running"}}
	got := restartLoopCheck(units, nil)
	if len(got) != 1 || got[0].Status != "pass" {
		t.Fatalf("a unit at the threshold must pass, got %+v", got)
	}
}

// A unit that can never start stays activating, never failed; the counter
// is what exposes it, worst first.
func TestRestartLoopCheckReportsWorstFirstWithJournalHint(t *testing.T) {
	units := []unitRestarts{
		{Scope: "system", Unit: "sshd.service", Restarts: 0, Active: "active", Sub: "running"},
		{Scope: "user", Unit: "tunnel.service", Restarts: 640, Active: "active", Sub: "running", ActiveFor: restartLoopRecentWindow / 2},
		{Scope: "user", Unit: "worker.service", Restarts: 198412, Active: "activating", Sub: "auto-restart"},
		{Scope: "system", Unit: "legacy-daemon.service", Restarts: 28909, Active: "activating", Sub: "auto-restart"},
	}
	got := restartLoopCheck(units, nil)
	if len(got) != 1 || got[0].Status != "warn" {
		t.Fatalf("expected one warn, got %+v", got)
	}
	msg := got[0].Message
	want := "user worker.service: 198412 restarts (activating/auto-restart); system legacy-daemon.service: 28909 restarts (activating/auto-restart); user tunnel.service: 640 restarts (active/running)"
	if msg != want {
		t.Fatalf("message:\n got %q\nwant %q", msg, want)
	}
	if !strings.Contains(got[0].Fix, "journalctl --user -u worker.service") {
		t.Fatalf("fix should point at the worst unit's user journal, got %q", got[0].Fix)
	}
}

func TestRestartLoopCheckBoundsTheList(t *testing.T) {
	var units []unitRestarts
	for i := 0; i < restartLoopReportLimit+3; i++ {
		units = append(units, unitRestarts{Scope: "system", Unit: fmt.Sprintf("u%d.service", i), Restarts: 100 + i, Active: "activating", Sub: "auto-restart"})
	}
	got := restartLoopCheck(units, nil)
	if !strings.HasSuffix(got[0].Message, "; and 3 more") {
		t.Fatalf("expected the overflow count, got %q", got[0].Message)
	}
	if strings.Count(got[0].Message, " restarts (") != restartLoopReportLimit {
		t.Fatalf("expected %d listed units, got %q", restartLoopReportLimit, got[0].Message)
	}
	if !strings.Contains(got[0].Fix, "journalctl -u u7.service") {
		t.Fatalf("system scope fix should use the system journal, got %q", got[0].Fix)
	}
}

// Only units looping now are flagged: a high lifetime count on a unit that
// has stayed up past restartLoopRecentWindow is history, not a loop.
func TestRestartLoopCheckFlagsOnlyCurrentLoops(t *testing.T) {
	old := restartLoopRecentWindow + time.Hour
	recent := restartLoopRecentWindow / 2
	cases := []struct {
		name string
		u    unitRestarts
		flag bool
	}{
		{"healthy with high old count", unitRestarts{Restarts: 640, Active: "active", Sub: "running", ActiveFor: old}, false},
		{"healthy with unknown activation time", unitRestarts{Restarts: 640, Active: "active", Sub: "running", ActiveFor: -1}, false},
		{"currently auto-restarting", unitRestarts{Restarts: 198412, Active: "activating", Sub: "auto-restart", ActiveFor: -1}, true},
		{"failed after restarts", unitRestarts{Restarts: 50, Active: "failed", Sub: "failed", ActiveFor: -1}, true},
		{"recently re-activated", unitRestarts{Restarts: 640, Active: "active", Sub: "running", ActiveFor: recent}, true},
		{"below threshold auto-restarting", unitRestarts{Restarts: restartLoopThreshold, Active: "activating", Sub: "auto-restart", ActiveFor: -1}, false},
		{"below threshold recently active", unitRestarts{Restarts: 3, Active: "active", Sub: "running", ActiveFor: recent}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.u.Scope, tc.u.Unit = "system", "x.service"
			got := restartLoopCheck([]unitRestarts{tc.u}, nil)
			if flagged := got[0].Status == "warn"; flagged != tc.flag {
				t.Fatalf("flagged=%v want %v (%+v)", flagged, tc.flag, got)
			}
		})
	}
}

func TestParseRestartCountersActiveFor(t *testing.T) {
	out := "Id=a.service\nNRestarts=30\nActiveState=active\nSubState=running\nActiveEnterTimestampMonotonic=1000000\n\n" +
		"Id=b.service\nNRestarts=30\nActiveState=activating\nSubState=auto-restart\nActiveEnterTimestampMonotonic=0\n"
	got := parseRestartCountersAt("system", out, 61*time.Second)
	if got[0].ActiveFor != 60*time.Second {
		t.Fatalf("a.service ActiveFor = %v, want 60s", got[0].ActiveFor)
	}
	if got[1].ActiveFor != -1 {
		t.Fatalf("b.service never entered active, want -1, got %v", got[1].ActiveFor)
	}
}
