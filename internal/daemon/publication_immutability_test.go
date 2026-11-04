package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestDiscoveryUpdatesDoNotMutatePublishedSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := New(time.Hour, func(context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{
			Warnings: []models.Warning{{Kind: "discovery", Message: "previous listener failure"}, {Kind: "other", Message: "keep"}},
		}, nil
	})
	d.SetSnapshotPath(filepath.Join(t.TempDir(), "snapshot.json"))
	if err := d.RefreshNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A lock-free reader can hold this publication across any later update.
	previous := d.snapshotPublished.Load()
	if len(previous.Warnings) != 2 {
		t.Fatalf("initial warnings = %+v", previous.Warnings)
	}
	configPath := filepath.Join(t.TempDir(), "nodes.yaml")
	if err := os.WriteFile(configPath, []byte("nodes: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	written := make(chan struct{}, 1)
	persist := d.writeSnapshotCache
	d.writeSnapshotCache = func(path string, snap *models.ClusterSnapshot) error {
		err := persist(path, snap)
		written <- struct{}{}
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		wait, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		d.WaitStopped(wait)
	})
	d.WatchDiscovery(ctx, configPath)
	select {
	case <-written:
	case <-time.After(3 * time.Second):
		t.Fatal("discovery did not publish the cleared warning")
	}
	current, ok := d.Snapshot()
	if !ok || len(current.Warnings) != 1 || current.Warnings[0].Kind != "other" {
		t.Fatalf("current warnings = %+v", current)
	}
	if len(previous.Warnings) != 2 || previous.Warnings[0].Kind != "discovery" {
		t.Fatalf("discovery changed an already published snapshot: %+v", previous.Warnings)
	}
}
