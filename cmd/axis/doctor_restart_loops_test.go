package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParseRestartCounters(t *testing.T) {
	out := "Id=legacy-worker.service\nNRestarts=32751\nActiveState=activating\nSubState=auto-restart\n\n" +
		"Id=sshd.service\nNRestarts=0\nActiveState=active\nSubState=running\n\n" +
		"Id=no-counter.service\nActiveState=inactive\nSubState=dead\n"
	got := parseRestartCounters("system", out)
	want := []unitRestarts{
		{Scope: "system", Unit: "legacy-worker.service", Restarts: 32751, Active: "activating", Sub: "auto-restart"},
		{Scope: "system", Unit: "sshd.service", Restarts: 0, Active: "active", Sub: "running"},
		{Scope: "system", Unit: "no-counter.service", Restarts: 0, Active: "inactive", Sub: "dead"},
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
		{Scope: "user", Unit: "tunnel.service", Restarts: 640, Active: "active", Sub: "running"},
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
