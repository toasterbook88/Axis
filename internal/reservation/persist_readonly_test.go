package reservation

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadReadOnlyPreservesStaleEntriesAndFileBytes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().UTC().Add(-time.Hour)
	data, err := json.MarshalIndent(diskFormat{Entries: []*Entry{{
		ID:            "exec-1",
		Node:          "ghost",
		RAMMB:         1024,
		CreatedAt:     stale,
		LastHeartbeat: stale,
	}}}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), data, 0o600); err != nil {
		t.Fatal(err)
	}

	ledger := NewLedger(DefaultLimits(), nil)
	if err := ledger.LockFile(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer ledger.UnlockFile()
	if err := ledger.LoadReadOnly(); err != nil {
		t.Fatal(err)
	}

	entries := ledger.Entries()
	if len(entries) != 1 || entries[0].ID != "exec-1" {
		t.Fatalf("read-only load reclaimed the stale entry: %+v", entries)
	}
	after, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, after) {
		t.Errorf("read-only load rewrote ledger.json\n got: %s\nwant: %s", after, data)
	}
}

func TestHistoricalDeviceHoldLedgerLoadsWithoutLosingReservations(t *testing.T) {
	axisHome := t.TempDir()
	t.Setenv("AXIS_HOME", axisHome)
	legacy := []byte(`{
		"entries": [{
			"id": "exec-1",
			"node": "alpha",
			"owner_exec_id": "task-1",
			"owner_surface": "guarded-exec",
			"ram_mb": 1024,
			"created_at": "2026-10-10T00:00:00Z",
			"last_heartbeat": "2026-10-10T00:00:00Z"
		}],
		"device_holds": [{
			"id": "hold-1",
			"node": "alpha",
			"gpu_index": 0,
			"mib": 4096,
			"owner": "legacy",
			"expires_at": "2026-10-11T00:00:00Z"
		}]
	}`)
	if err := os.WriteFile(Path(), legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	ledger := NewLedger(DefaultLimits(), nil)
	if err := ledger.LoadReadOnly(); err != nil {
		t.Fatalf("LoadReadOnly historical ledger: %v", err)
	}
	entries := ledger.Entries()
	if len(entries) != 1 || entries[0].ID != "exec-1" || entries[0].RAMMB != 1024 {
		t.Fatalf("historical reservations were not preserved: %+v", entries)
	}

	if err := ledger.Save(); err != nil {
		t.Fatalf("Save ledger after historical load: %v", err)
	}
	migrated, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(migrated), "device_holds") {
		t.Fatalf("removed device holds survived the next ledger write: %s", migrated)
	}
	if !strings.Contains(string(migrated), `"id": "exec-1"`) || !strings.Contains(string(migrated), `"ram_mb": 1024`) {
		t.Fatalf("reservation was lost while dropping historical device holds: %s", migrated)
	}
}

func TestLoadReadOnlyDoesNotQuarantineCorruptLedger(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte("{not-json")
	if err := os.WriteFile(Path(), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	ledger := NewLedger(DefaultLimits(), nil)
	if err := ledger.LoadReadOnly(); err == nil {
		t.Fatal("expected corrupt-ledger error")
	}
	after, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("read-only load quarantined the ledger: %v", err)
	}
	if !bytes.Equal(raw, after) {
		t.Errorf("corrupt ledger changed\n got: %s\nwant: %s", after, raw)
	}
	matches, err := filepath.Glob(Path() + ".corrupt-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("read-only load created quarantine files: %v", matches)
	}
}

func TestLoadDoesNotQuarantineCorruptLedger(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte("{not-json")
	if err := os.WriteFile(Path(), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	ledger := NewLedger(DefaultLimits(), nil)
	if err := ledger.Load(); err == nil {
		t.Fatal("expected corrupt-ledger error")
	}
	after, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("load moved the authoritative ledger aside: %v", err)
	}
	if !bytes.Equal(raw, after) {
		t.Errorf("corrupt ledger changed\n got: %s\nwant: %s", after, raw)
	}
	matches, err := filepath.Glob(Path() + ".corrupt-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("load created quarantine files: %v", matches)
	}
}
