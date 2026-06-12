package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/state"
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

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Second, "5s"},
		{45 * time.Second, "45s"},
		{1 * time.Minute, "1m0s"},
		{90 * time.Second, "1m30s"},
		{45 * time.Minute, "45m0s"},
		{1 * time.Hour, "1h0m"},
		{90 * time.Minute, "1h30m"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.d)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestTruncateID(t *testing.T) {
	if got := truncateID("abc", 5); got != "abc" {
		t.Errorf("truncateID(abc, 5) = %q, want abc", got)
	}
	if got := truncateID("abcdef", 3); got != "abc" {
		t.Errorf("truncateID(abcdef, 3) = %q, want abc", got)
	}
	if got := truncateID("abcdefgh", 6); got != "abc..." {
		t.Errorf("truncateID(abcdefgh, 6) = %q, want abc...", got)
	}
}

func TestReservationsDoctorHealthy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	// Create a clean layout: create .axis directory
	err := os.MkdirAll(filepath.Join(home, ".axis"), 0755)
	if err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "localhost"
	}

	// 1. Setup healthy ledger (only 1 valid active entry)
	now := time.Now().UTC()
	type diskFormat struct {
		Entries []*reservation.Entry `json:"entries"`
	}
	df := diskFormat{
		Entries: []*reservation.Entry{
			{
				ID:            "valid-1",
				Node:          hostname,
				RAMMB:         1024,
				CreatedAt:     now,
				LastHeartbeat: now,
			},
		},
	}
	ledgerData, _ := json.Marshal(df)
	_ = os.WriteFile(filepath.Join(home, ".axis", "ledger.json"), ledgerData, 0644)

	// 2. Setup matching state.json
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			hostname: {
				ReservedMB: 1024,
			},
		},
	}
	stateData, _ := json.Marshal(st)
	_ = os.WriteFile(filepath.Join(home, ".axis", "state.json"), stateData, 0644)

	// 3. Setup matching snapshot.json (capacity is 4096MB, which is > 1024MB)
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name:     hostname,
				Hostname: hostname,
				Resources: &models.Resources{
					RAMTotalMB: 4096,
				},
			},
		},
	}
	snapData, _ := json.Marshal(snap)
	_ = os.WriteFile(filepath.Join(home, ".axis", "snapshot.json"), snapData, 0644)

	// 4. Setup nodes.yaml config
	cfgData := fmt.Sprintf("nodes:\n  - name: %s\n    hostname: %s\n", hostname, hostname)
	_ = os.WriteFile(filepath.Join(home, ".axis", "nodes.yaml"), []byte(cfgData), 0644)

	// Run doctor command in text mode
	cmd := reservationsDoctorCmd()
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{"--format", "text"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("doctor healthy failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	if !strings.Contains(stdout, "No issues found") {
		t.Errorf("expected healthy message, got:\n%s", stdout)
	}

	// Run doctor command in json mode
	cmd = reservationsDoctorCmd()
	stdout, stderr, err = captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{"--format", "json"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("doctor healthy json failed: %v", err)
	}

	var res struct {
		Healthy  bool            `json:"healthy"`
		Findings []DoctorFinding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("unmarshal json result failed: %v\noutput: %s", err, stdout)
	}
	if !res.Healthy {
		t.Errorf("expected healthy=true, got false")
	}
	if len(res.Findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(res.Findings))
	}
}

func TestReservationsDoctorFindingsAndFix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	err := os.MkdirAll(filepath.Join(home, ".axis"), 0755)
	if err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "localhost"
	}

	// Mock processAlive helper
	oldProcessAlive := reservationsDoctorProcessAlive
	reservationsDoctorProcessAlive = func(pid int) bool {
		if pid == 999999 {
			return false
		}
		return true
	}
	defer func() {
		reservationsDoctorProcessAlive = oldProcessAlive
	}()

	// Setup doctor findings:
	// - expired-1: expired (ExpiresAt in the past)
	// - stale-1: stale (LastHeartbeat 5 minutes ago)
	// - orphaned-1: local, OwnerPID = 999999 (not alive)
	// - valid-1: valid active entry (RAMMB = 1024)
	now := time.Now().UTC()
	type diskFormat struct {
		Entries []*reservation.Entry `json:"entries"`
	}
	df := diskFormat{
		Entries: []*reservation.Entry{
			{
				ID:            "expired-1",
				Node:          hostname,
				RAMMB:         1024,
				ExpiresAt:     now.Add(-1 * time.Minute),
				CreatedAt:     now.Add(-10 * time.Minute),
				LastHeartbeat: now,
			},
			{
				ID:            "stale-1",
				Node:          hostname,
				RAMMB:         1024,
				CreatedAt:     now.Add(-10 * time.Minute),
				LastHeartbeat: now.Add(-5 * time.Minute),
			},
			{
				ID:            "orphaned-1",
				Node:          hostname,
				RAMMB:         1024,
				OwnerPID:      999999,
				CreatedAt:     now,
				LastHeartbeat: now,
			},
			{
				ID:            "valid-1",
				Node:          hostname,
				RAMMB:         1024,
				CreatedAt:     now,
				LastHeartbeat: now,
			},
		},
	}
	ledgerData, _ := json.Marshal(df)
	_ = os.WriteFile(filepath.Join(home, ".axis", "ledger.json"), ledgerData, 0644)

	// State.json: Node ReservedMB is 9999 (but ledger total has 4096 initially -> drift)
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			hostname: {
				ReservedMB: 9999,
			},
		},
	}
	stateData, _ := json.Marshal(st)
	_ = os.WriteFile(filepath.Join(home, ".axis", "state.json"), stateData, 0644)

	// Snapshot.json: RAMTotalMB is 512 (but ledger total has 4096 initially -> leak)
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name:     hostname,
				Hostname: hostname,
				Resources: &models.Resources{
					RAMTotalMB: 512,
				},
			},
		},
	}
	snapData, _ := json.Marshal(snap)
	_ = os.WriteFile(filepath.Join(home, ".axis", "snapshot.json"), snapData, 0644)

	cfgData := fmt.Sprintf("nodes:\n  - name: %s\n    hostname: %s\n", hostname, hostname)
	_ = os.WriteFile(filepath.Join(home, ".axis", "nodes.yaml"), []byte(cfgData), 0644)

	// 1. Run doctor without --fix
	cmd := reservationsDoctorCmd()
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{"--format", "json"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("doctor findings json failed: %v\nstderr: %s", err, stderr)
	}

	var res struct {
		Healthy  bool            `json:"healthy"`
		Findings []DoctorFinding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("unmarshal json findings failed: %v\noutput: %s", err, stdout)
	}

	if res.Healthy {
		t.Errorf("expected healthy=false, got true")
	}

	// Verify all expected categories are present
	foundTypes := make(map[string]bool)
	for _, f := range res.Findings {
		foundTypes[f.Type] = true
	}
	for _, tName := range []string{"expired", "stale", "orphaned", "drift", "leak"} {
		if !foundTypes[tName] {
			t.Errorf("missing expected finding type: %s", tName)
		}
	}

	// Run in text format to ensure it prints the table
	cmdText := reservationsDoctorCmd()
	stdoutText, _, _ := captureProcessOutput(t, func() error {
		cmdText.SetArgs([]string{"--format", "text"})
		return cmdText.Execute()
	})
	for _, tName := range []string{"EXPIRED", "STALE", "ORPHANED", "DRIFT", "LEAK"} {
		if !strings.Contains(stdoutText, tName) {
			t.Errorf("expected text output to contain %q, got:\n%s", tName, stdoutText)
		}
	}

	// 2. Run doctor with --fix
	cmdFix := reservationsDoctorCmd()
	stdoutFix, _, err := captureProcessOutput(t, func() error {
		cmdFix.SetArgs([]string{"--fix", "--format", "json"})
		return cmdFix.Execute()
	})
	if err != nil {
		t.Fatalf("doctor fix json failed: %v", err)
	}

	var resFix struct {
		Healthy  bool            `json:"healthy"`
		Findings []DoctorFinding `json:"findings"`
		Fixed    []DoctorFinding `json:"fixed"`
	}
	if err := json.Unmarshal([]byte(stdoutFix), &resFix); err != nil {
		t.Fatalf("unmarshal json fix failed: %v\noutput: %s", err, stdoutFix)
	}

	if len(resFix.Fixed) != 3 {
		t.Errorf("expected 3 fixed entries, got %d", len(resFix.Fixed))
	}

	fixedIDs := make(map[string]bool)
	for _, f := range resFix.Fixed {
		fixedIDs[f.EntryID] = true
	}
	for _, id := range []string{"expired-1", "stale-1", "orphaned-1"} {
		if !fixedIDs[id] {
			t.Errorf("expected %s to be fixed", id)
		}
	}

	// Verify entries were actually removed from the ledger on disk
	ledger2 := reservation.NewLedger(reservation.DefaultLimits(), nil)
	if err := ledger2.Load(); err != nil {
		t.Fatalf("failed to reload ledger: %v", err)
	}
	entries := ledger2.Entries()
	if len(entries) != 1 {
		t.Errorf("expected 1 remaining entry in ledger, got %d", len(entries))
	}
	if entries[0].ID != "valid-1" {
		t.Errorf("expected remaining entry to be valid-1, got %s", entries[0].ID)
	}
}
