package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/reservation"
)

func TestReservationsListEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cmd := reservationsListCmd()
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd.SetArgs(nil)
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("list empty: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "No active reservations") {
		t.Fatalf("expected 'No active reservations', got:\n%s", stdout)
	}
}

func TestReservationsListJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
	ledger.SetNodeCapacity("node-a", 16384)
	_, err := ledger.Reserve(reservation.Entry{
		ID:           "exec-1",
		Node:         "node-a",
		OwnerExecID:  "task-1",
		OwnerSurface: "guarded-exec",
		RAMMB:        4096,
	})
	if err != nil {
		t.Fatalf("reserve failed: %v", err)
	}

	cmd := reservationsListCmd()
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{"--format", "json"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("list json: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	var entries []reservation.Entry
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("unmarshal list json: %v\noutput: %s", err, stdout)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ID != "exec-1" {
		t.Fatalf("expected exec-1, got %s", entries[0].ID)
	}
	_ = stderr
}

func TestReservationsListNDJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
	ledger.SetNodeCapacity("node-a", 16384)
	ledger.Reserve(reservation.Entry{ID: "exec-1", Node: "node-a", RAMMB: 1024})
	ledger.Reserve(reservation.Entry{ID: "exec-2", Node: "node-a", RAMMB: 2048})

	cmd := reservationsListCmd()
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{"--format", "ndjson"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("list ndjson: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 ndjson lines, got %d:\n%s", len(lines), stdout)
	}
	for i, line := range lines {
		var e reservation.Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %d unmarshal: %v", i, err)
		}
	}
	_ = stderr
}

func TestReservationsInspectFound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
	ledger.SetNodeCapacity("node-a", 16384)
	ledger.Reserve(reservation.Entry{
		ID:           "exec-1",
		Node:         "node-a",
		OwnerExecID:  "task-1",
		OwnerSurface: "guarded-exec",
		RAMMB:        4096,
		Description:  "test task",
	})

	cmd := reservationsInspectCmd()
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{"exec-1"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("inspect found: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "exec-1") {
		t.Fatalf("expected ID in output, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "test task") {
		t.Fatalf("expected description in output, got:\n%s", stdout)
	}
	_ = stderr
}

func TestReservationsInspectJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
	ledger.SetNodeCapacity("node-a", 16384)
	ledger.Reserve(reservation.Entry{ID: "exec-1", Node: "node-a", RAMMB: 1024})

	cmd := reservationsInspectCmd()
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{"--format", "json", "exec-1"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("inspect json: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	var e reservation.Entry
	if err := json.Unmarshal([]byte(stdout), &e); err != nil {
		t.Fatalf("unmarshal inspect json: %v\noutput: %s", err, stdout)
	}
	if e.ID != "exec-1" {
		t.Fatalf("expected exec-1, got %s", e.ID)
	}
	_ = stderr
}

func TestReservationsInspectNotFound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cmd := reservationsInspectCmd()
	_, _, err := captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{"missing-id"})
		return cmd.Execute()
	})
	if err == nil {
		t.Fatal("expected error for missing reservation")
	}
	code := ExitCode(err)
	if code != ExitErrGeneric {
		t.Fatalf("expected exit code %d, got %d", ExitErrGeneric, code)
	}
}

func TestReservationsReleaseSuccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
	ledger.SetNodeCapacity("node-a", 16384)
	ledger.Reserve(reservation.Entry{
		ID:       "exec-1",
		Node:     "node-a",
		RAMMB:    1024,
		OwnerPID: os.Getpid(),
	})

	cmd := reservationsReleaseCmd()
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{"exec-1"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("release success: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "Released reservation exec-1") {
		t.Fatalf("expected release confirmation, got:\n%s", stdout)
	}

	// Verify it was removed
	ledger2 := reservation.NewLedger(reservation.DefaultLimits(), nil)
	if err := ledger2.Load(); err != nil {
		t.Fatalf("reload ledger: %v", err)
	}
	if len(ledger2.Entries()) != 0 {
		t.Fatalf("expected 0 entries after release, got %d", len(ledger2.Entries()))
	}
	_ = stderr
}

func TestReservationsReleaseJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
	ledger.SetNodeCapacity("node-a", 16384)
	ledger.Reserve(reservation.Entry{
		ID:       "exec-1",
		Node:     "node-a",
		RAMMB:    1024,
		OwnerPID: os.Getpid(),
	})

	cmd := reservationsReleaseCmd()
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{"--format", "json", "exec-1"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("release json: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("unmarshal release json: %v\noutput: %s", err, stdout)
	}
	if result["success"] != true {
		t.Fatalf("expected success=true, got %v", result["success"])
	}
	_ = stderr
}

func TestReservationsReleaseNotFound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cmd := reservationsReleaseCmd()
	_, _, err := captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{"missing-id"})
		return cmd.Execute()
	})
	if err == nil {
		t.Fatal("expected error for missing reservation")
	}
	code := ExitCode(err)
	if code != ExitErrGeneric {
		t.Fatalf("expected exit code %d, got %d", ExitErrGeneric, code)
	}
}

func TestReservationsReleaseOwnedByOtherPID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
	ledger.SetNodeCapacity("node-a", 16384)
	ledger.Reserve(reservation.Entry{
		ID:       "exec-1",
		Node:     "node-a",
		RAMMB:    1024,
		OwnerPID: 99999,
	})

	cmd := reservationsReleaseCmd()
	_, _, err := captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{"exec-1"})
		return cmd.Execute()
	})
	if err == nil {
		t.Fatal("expected error for foreign-owned reservation")
	}
	code := ExitCode(err)
	if code != ExitErrGeneric {
		t.Fatalf("expected exit code %d, got %d", ExitErrGeneric, code)
	}
}

func TestReservationsReleaseForce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
	ledger.SetNodeCapacity("node-a", 16384)
	ledger.Reserve(reservation.Entry{
		ID:       "exec-1",
		Node:     "node-a",
		RAMMB:    1024,
		OwnerPID: 99999,
	})

	cmd := reservationsReleaseCmd()
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{"--force", "exec-1"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("release force: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "Released reservation exec-1") {
		t.Fatalf("expected release confirmation, got:\n%s", stdout)
	}
	_ = stderr
}

func TestReservationsListText(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	now := time.Now().UTC().Truncate(time.Second)
	ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
	ledger.SetNodeCapacity("node-a", 16384)
	entry := reservation.Entry{
		ID:            "exec-1",
		Node:          "node-a",
		OwnerExecID:   "task-1",
		OwnerSurface:  "guarded-exec",
		RAMMB:         4096,
		CreatedAt:     now,
		LastHeartbeat: now,
	}
	ledger.Reserve(entry)

	cmd := reservationsListCmd()
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd.SetArgs(nil)
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("list text: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "exec-1") {
		t.Fatalf("expected exec-1 in text output, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "node-a") {
		t.Fatalf("expected node-a in text output, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "4096") {
		t.Fatalf("expected 4096 in text output, got:\n%s", stdout)
	}
	_ = stderr
}
