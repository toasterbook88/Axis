package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/persist"
)

// writeSnapshot marshals snap and writes it to <home>/snapshot.json,
// returning the path. The caller controls freshness via snap.Timestamp.
func writeSnapshot(t *testing.T, home string, snap *models.ClusterSnapshot) string {
	t.Helper()
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	path := filepath.Join(home, "snapshot.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	return path
}

func TestLoadDaemonSnapshotMissingCache(t *testing.T) {
	t.Setenv(persist.AxisHomeEnv, t.TempDir())

	snap, _, err := loadDaemonSnapshot()
	if err == nil {
		t.Fatal("expected error for missing snapshot cache, got nil (truth-plane: must not fabricate empty state)")
	}
	if snap != nil {
		t.Fatalf("expected nil snapshot on error, got %+v", snap)
	}
}

func TestLoadDaemonSnapshotUsesAxisHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv(persist.AxisHomeEnv, home)

	ts := time.Date(2026, 8, 24, 10, 30, 0, 0, time.UTC)
	writeSnapshot(t, home, &models.ClusterSnapshot{
		Timestamp: ts,
		Nodes: []models.NodeFacts{
			{Name: "node-a", Status: models.StatusComplete},
		},
	})

	snap, freshness, err := loadDaemonSnapshot()
	if err != nil {
		t.Fatalf("loadDaemonSnapshot: %v", err)
	}
	if len(snap.Nodes) != 1 || snap.Nodes[0].Name != "node-a" {
		t.Fatalf("unexpected snapshot nodes: %+v", snap.Nodes)
	}
	if !freshness.Equal(ts) {
		t.Fatalf("freshness = %v, want snapshot timestamp %v", freshness, ts)
	}
}

func TestLoadDaemonSnapshotZeroTimestampFallsBackToMtime(t *testing.T) {
	home := t.TempDir()
	t.Setenv(persist.AxisHomeEnv, home)

	path := writeSnapshot(t, home, &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{},
	})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}

	_, freshness, err := loadDaemonSnapshot()
	if err != nil {
		t.Fatalf("loadDaemonSnapshot: %v", err)
	}
	if freshness.IsZero() {
		t.Fatal("expected mtime fallback for zero snapshot timestamp, got zero time")
	}
	if !freshness.Equal(info.ModTime()) {
		t.Fatalf("freshness = %v, want file mtime %v", freshness, info.ModTime())
	}
}

func TestLoadDaemonSnapshotCorruptCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv(persist.AxisHomeEnv, home)

	if err := os.WriteFile(filepath.Join(home, "snapshot.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt snapshot: %v", err)
	}

	snap, _, err := loadDaemonSnapshot()
	if err == nil {
		t.Fatal("expected error for corrupt snapshot cache, got nil")
	}
	if snap != nil {
		t.Fatalf("expected nil snapshot on error, got %+v", snap)
	}
}
