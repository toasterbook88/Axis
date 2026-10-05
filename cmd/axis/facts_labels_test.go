package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalNodeLabels_MissingFile(t *testing.T) {
	t.Setenv("AXIS_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	labels, err := localNodeLabels("not-a-configured-name")
	if err != nil {
		t.Fatalf("missing config must yield empty labels, got %v", err)
	}
	if len(labels) != 0 {
		t.Fatalf("missing config labels = %v", labels)
	}
}

func TestLocalNodeLabels_StatError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can stat an unreadable directory")
	}
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.Mkdir(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	t.Setenv("AXIS_CONFIG", filepath.Join(blocked, "nodes.yaml"))
	if _, err := localNodeLabels("not-a-configured-name"); err == nil {
		t.Fatal("an unreadable config path must be an error")
	}
}

func TestLocalNodeLabels_UsesLocalEntryWhenNameDiffers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	body := []byte("nodes:\n  - name: builder\n    hostname: 127.0.0.1\n    ssh_user: me\n    labels:\n      os: linux\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AXIS_CONFIG", path)
	labels, err := localNodeLabels("not-a-configured-name")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if labels["os"] != "linux" {
		t.Fatalf("local entry labels = %v, want os=linux", labels)
	}
}
