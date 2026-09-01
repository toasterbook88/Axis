package reservation

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
