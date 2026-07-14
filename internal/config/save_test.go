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

func TestSaveAtomicSkipsSemanticNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.yaml")
	cfg := testSaveConfig("node-a")
	if _, err := SaveAtomic(path, cfg); err != nil {
		t.Fatal(err)
	}

	result, err := SaveAtomic(path, testSaveConfig("node-a"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || result.BackupPath != "" {
		t.Fatalf("expected no-op, got %+v", result)
	}
	matches, err := filepath.Glob(path + ".bak-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("unexpected backups: %v", matches)
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

func testSaveConfig(name string) *Config {
	return &Config{Nodes: []NodeConfig{{
		Name:       name,
		Hostname:   "localhost",
		SSHUser:    "operator",
		Role:       "primary",
		TimeoutSec: 10,
	}}}
}
