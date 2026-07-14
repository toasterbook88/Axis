package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestDaemonReadsLockFreeDuringRefresh(t *testing.T) {
	var sequence atomic.Int64
	d := New(time.Hour, func(context.Context) (*models.ClusterSnapshot, error) {
		n := sequence.Add(1)
		return &models.ClusterSnapshot{
			Timestamp: time.Unix(n, 0).UTC(),
			Status:    models.SnapshotHealthy,
			Nodes: []models.NodeFacts{{
				Name:   "test-node",
				Status: models.StatusComplete,
			}},
		}, nil
	})
	d.SetSnapshotPath(filepath.Join(t.TempDir(), "snapshot.json"))

	if err := d.RefreshNow(context.Background()); err != nil {
		t.Fatalf("initial RefreshNow: %v", err)
	}

	const readers = 8
	const readsPerReader = 1000
	var wg sync.WaitGroup
	errs := make(chan string, readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var lastRefreshCount int64
			for j := 0; j < readsPerReader; j++ {
				snapshot, ok := d.Snapshot()
				if !ok || snapshot == nil {
					errs <- "Snapshot returned nil after initial refresh"
					return
				}
				snapshot.Nodes[0].Name = "mutated-copy"
				next, ok := d.Snapshot()
				if !ok || next.Nodes[0].Name != "test-node" {
					errs <- "Snapshot clone was not independent"
					return
				}

				meta := d.Meta()
				if meta.RefreshCount < lastRefreshCount {
					errs <- "Meta RefreshCount regressed"
					return
				}
				lastRefreshCount = meta.RefreshCount
			}
		}()
	}

	for i := 0; i < 100; i++ {
		if err := d.RefreshNow(context.Background()); err != nil {
			t.Fatalf("RefreshNow %d: %v", i, err)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	if got := d.Meta().RefreshCount; got < 101 {
		t.Fatalf("expected at least 101 refreshes, got %d", got)
	}
}

func TestRefreshPublishesSnapshotWhenSkillsLoadFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".axis"), 0o755); err != nil {
		t.Fatalf("create axis directory: %v", err)
	}

	var sequence atomic.Int64
	d := New(time.Hour, func(context.Context) (*models.ClusterSnapshot, error) {
		n := sequence.Add(1)
		return &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{{Name: "node-" + string(rune('0'+n))}},
		}, nil
	})
	d.SetSnapshotPath("")

	if err := d.RefreshNow(context.Background()); err != nil {
		t.Fatalf("initial RefreshNow: %v", err)
	}

	skillsPath := filepath.Join(home, ".axis", "skills.json")
	if err := os.Mkdir(skillsPath, 0o755); err != nil {
		t.Fatalf("make skills path unreadable to Load: %v", err)
	}
	if err := d.RefreshNow(context.Background()); err == nil {
		t.Fatal("expected RefreshNow to fail when skills.json is a directory")
	}

	snapshot, ok := d.Snapshot()
	if !ok || snapshot == nil {
		t.Fatal("expected fresh snapshot to remain published after skills failure")
	}
	if got := snapshot.Nodes[0].Name; got != "node-2" {
		t.Fatalf("expected newly collected snapshot node-2, got %q", got)
	}
}

func BenchmarkSnapshotUnderRefresh(b *testing.B) {
	var sequence atomic.Int64
	d := New(time.Hour, func(context.Context) (*models.ClusterSnapshot, error) {
		n := sequence.Add(1)
		return &models.ClusterSnapshot{
			Timestamp: time.Unix(n, 0).UTC(),
			Status:    models.SnapshotHealthy,
		}, nil
	})
	d.SetSnapshotPath("")
	if err := d.RefreshNow(context.Background()); err != nil {
		b.Fatalf("initial RefreshNow: %v", err)
	}

	stop := make(chan struct{})
	var refreshWG sync.WaitGroup
	refreshWG.Add(1)
	go func() {
		defer refreshWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = d.RefreshNow(context.Background())
			}
		}
	}()
	b.Cleanup(func() {
		close(stop)
		refreshWG.Wait()
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = d.Snapshot()
		_ = d.Meta()
	}
}
