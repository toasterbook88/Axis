package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveAtomicCreatesSecureConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".axis", "nodes.yaml")
	cfg := testSaveConfig("node-a")

	result, err := SaveAtomic(path, cfg)
	if err != nil {
		t.Fatalf("SaveAtomic: %v", err)
	}
	if !result.Changed || result.BackupPath != "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("mode = %o, want 600", got)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Nodes[0].Name != "node-a" {
		t.Fatalf("unexpected config: %+v", loaded)
	}
}

func TestSaveAtomicBacksUpBeforeReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.yaml")
	if _, err := SaveAtomic(path, testSaveConfig("node-a")); err != nil {
		t.Fatal(err)
	}

	result, err := SaveAtomic(path, testSaveConfig("node-b"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.BackupPath == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	backup, err := os.ReadFile(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(backup), "name: node-a") {
		t.Fatalf("backup does not contain old config:\n%s", backup)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Nodes[0].Name != "node-b" {
		t.Fatalf("new config not installed: %+v", loaded)
	}
}

func TestSaveAtomicValidatesBeforeMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.yaml")
	original := []byte("sentinel\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}

	_, err := SaveAtomic(path, &Config{})
	if err == nil {
		t.Fatal("expected validation error")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Fatalf("file mutated on validation failure: %q", got)
	}
}

func TestNextBackupPathUsesBaseWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	now := mustParseTime(t, "2026-07-14T12:00:00Z")
	got := nextBackupPath(path, now)
	want := path + ".bak-20260714T120000Z"
	if got != want {
		t.Fatalf("nextBackupPath = %q, want %q", got, want)
	}
}
