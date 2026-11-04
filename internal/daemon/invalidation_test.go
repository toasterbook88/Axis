package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestInvalidateDiscardsInFlightCollection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	var calls, hooks atomic.Int32
	d := New(time.Hour, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		name := "fresh"
		if calls.Add(1) == 1 {
			name = "old"
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &models.ClusterSnapshot{Nodes: []models.NodeFacts{{Name: name, Status: models.StatusComplete}}}, nil
	})
	path := filepath.Join(t.TempDir(), "snapshot.json")
	d.SetSnapshotPath(path)
	d.AddOnSnapshotChanged(func(*models.ClusterSnapshot, string) { hooks.Add(1) })
	done := make(chan error, 1)
	go func() { done <- d.RefreshNow(ctx) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("collector did not start")
	}
	d.Invalidate()
	close(release)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "invalidated") {
			t.Errorf("refresh error=%v, want invalidated collection", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("collector did not finish")
	}
	if _, ok := d.Snapshot(); ok {
		t.Error("invalidated collection republished a snapshot")
	}
	if d.Meta().Ready {
		t.Error("invalidated collection marked cache ready")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("invalidated collection persisted cache: %v", err)
	}
	if n := hooks.Load(); n != 0 {
		t.Errorf("invalidated collection emitted %d hooks", n)
	}
	if err := d.RefreshNow(ctx); err != nil {
		t.Fatalf("fresh refresh: %v", err)
	}
	if snap, ok := d.Snapshot(); !ok || snap.Nodes[0].Name != "fresh" {
		t.Fatalf("fresh refresh not published: %+v", snap)
	}
	if !d.Meta().Ready || hooks.Load() != 1 {
		t.Error("fresh refresh did not restore readiness and hooks")
	}
	if data, err := os.ReadFile(path); err != nil || !strings.Contains(string(data), "fresh") {
		t.Errorf("fresh cache not persisted: %v", err)
	}
}

func TestInvalidateSerializesWithCachePersistence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := New(time.Hour, func(context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{Nodes: []models.NodeFacts{{Name: "old", Status: models.StatusComplete}}}, nil
	})
	path := filepath.Join(t.TempDir(), "snapshot.json")
	d.SetSnapshotPath(path)
	entered, release := make(chan struct{}), make(chan struct{})
	previous := d.writeSnapshotCache
	d.writeSnapshotCache = func(path string, snap *models.ClusterSnapshot) error {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return previous(path, snap)
	}
	defer func() { d.writeSnapshotCache = previous }()
	refreshed := make(chan error, 1)
	go func() { refreshed <- d.RefreshNow(ctx) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("persistence did not start")
	}
	read := make(chan bool, 1)
	go func() {
		snap, ok := d.Snapshot()
		read <- ok && snap != nil && d.Meta().Ready
	}()
	select {
	case ready := <-read:
		if !ready {
			t.Error("readers lost the current publication during persistence")
		}
	case <-time.After(3 * time.Second):
		t.Error("snapshot readers blocked on persistence")
	}
	invalidating, invalidated := make(chan struct{}), make(chan struct{})
	go func() { close(invalidating); d.Invalidate(); close(invalidated) }()
	<-invalidating
	select {
	case <-invalidated:
		t.Error("invalidation completed while an old cache write was outstanding")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-refreshed:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not finish")
	}
	select {
	case <-invalidated:
	case <-time.After(3 * time.Second):
		t.Fatal("invalidation did not finish")
	}
	if _, ok := d.Snapshot(); ok || d.Meta().Ready {
		t.Error("cache remained ready after invalidation")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("late write restored invalidated cache: %v", err)
	}
}
