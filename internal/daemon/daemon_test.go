package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

func TestRefreshStoresSnapshotAndMeta(t *testing.T) {
	d := New(30*time.Second, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{
			Timestamp: time.Unix(1700000000, 0).UTC(),
			Status:    models.SnapshotHealthy,
			Nodes: []models.NodeFacts{
				{
					Name:   "alpha",
					Status: models.StatusComplete,
					Resources: &models.Resources{
						RAMTotalMB: 8192,
						RAMFreeMB:  4096,
					},
				},
			},
			Summary: models.ClusterSummary{
				TotalNodes:     1,
				ReachableNodes: 1,
				TotalRAMMB:     8192,
				TotalFreeRAMMB: 4096,
			},
		}, nil
	})

	path := filepath.Join(t.TempDir(), "snapshot.json")
	d.SetSnapshotPath(path)

	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	snap, ok := d.Snapshot()
	if !ok {
		t.Fatal("expected cached snapshot")
	}
	if snap.Summary.TotalFreeRAMMB != 4096 {
		t.Fatalf("expected free ram 4096, got %d", snap.Summary.TotalFreeRAMMB)
	}
	if snap.Summary.TotalAllocatableMB != 4096 {
		t.Fatalf("expected allocatable ram 4096, got %d", snap.Summary.TotalAllocatableMB)
	}

	meta := d.Meta()
	if !meta.Ready {
		t.Fatal("expected daemon to be ready")
	}
	if meta.Source != "daemon-cache" {
		t.Fatalf("expected source daemon-cache, got %q", meta.Source)
	}
	if meta.Version != Version {
		t.Fatalf("expected version %q, got %q", Version, meta.Version)
	}
	if meta.RefreshIntervalSec != 30 {
		t.Fatalf("expected refresh interval sec 30, got %d", meta.RefreshIntervalSec)
	}
	if meta.CollectedAt.IsZero() {
		t.Fatal("expected collected_at to be populated")
	}
	if meta.NextRefreshAt.IsZero() {
		t.Fatal("expected next_refresh_at to be populated")
	}
	if meta.LastError != "" {
		t.Fatalf("expected empty last_error, got %q", meta.LastError)
	}
	if meta.CacheAgeSec < 0 {
		t.Fatalf("expected non-negative cache age, got %d", meta.CacheAgeSec)
	}
	if meta.Stale {
		t.Fatal("expected fresh metadata immediately after refresh")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot file: %v", err)
	}
	var persisted models.ClusterSnapshot
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("unmarshal snapshot file: %v", err)
	}
	if persisted.Summary.TotalNodes != 1 {
		t.Fatalf("expected persisted total nodes 1, got %d", persisted.Summary.TotalNodes)
	}
}

func TestRefreshFailurePreservesPreviousSnapshot(t *testing.T) {
	calls := 0
	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		calls++
		if calls == 1 {
			return &models.ClusterSnapshot{
				Status: models.SnapshotHealthy,
				Summary: models.ClusterSummary{
					TotalFreeRAMMB: 2048,
				},
			}, nil
		}
		return nil, context.DeadlineExceeded
	})
	d.SetSnapshotPath(filepath.Join(t.TempDir(), "snapshot.json"))

	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if err := d.Refresh(context.Background()); err == nil {
		t.Fatal("expected second refresh to fail")
	}

	snap, ok := d.Snapshot()
	if !ok {
		t.Fatal("expected previous snapshot to remain available")
	}
	if snap.Summary.TotalFreeRAMMB != 2048 {
		t.Fatalf("expected preserved free ram 2048, got %d", snap.Summary.TotalFreeRAMMB)
	}

	meta := d.Meta()
	if !meta.Ready {
		t.Fatal("expected cache to remain ready after failed refresh")
	}
	if !strings.Contains(meta.LastError, "deadline exceeded") {
		t.Fatalf("expected deadline exceeded in last_error, got %q", meta.LastError)
	}
}

func TestInvalidateClearsSnapshotAndRemovesPersistedFile(t *testing.T) {
	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Summary: models.ClusterSummary{
				TotalNodes: 1,
			},
		}, nil
	})

	path := filepath.Join(t.TempDir(), "snapshot.json")
	d.SetSnapshotPath(path)

	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected persisted snapshot file: %v", err)
	}

	d.Invalidate()

	if _, ok := d.Snapshot(); ok {
		t.Fatal("expected snapshot to be cleared")
	}

	meta := d.Meta()
	if meta.Ready {
		t.Fatal("expected cache to be marked not ready")
	}
	if !meta.CollectedAt.IsZero() {
		t.Fatal("expected collected_at to be cleared")
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected snapshot file to be removed, got %v", err)
	}
}

func TestRefreshNowStoresSnapshotImmediately(t *testing.T) {
	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Summary: models.ClusterSummary{
				TotalNodes: 2,
			},
		}, nil
	})
	d.SetSnapshotPath(filepath.Join(t.TempDir(), "snapshot.json"))

	if err := d.RefreshNow(context.Background()); err != nil {
		t.Fatalf("RefreshNow: %v", err)
	}

	snap, ok := d.Snapshot()
	if !ok {
		t.Fatal("expected snapshot after RefreshNow")
	}
	if snap.Summary.TotalNodes != 2 {
		t.Fatalf("expected total nodes 2, got %d", snap.Summary.TotalNodes)
	}

	if !d.Meta().Ready {
		t.Fatal("expected daemon to be ready after RefreshNow")
	}
}

func TestMetaIncludesReservedMB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 512, LastPlacedAt: time.Now().UTC(), ActiveTasks: 1, ActiveExecs: []string{"exec-a"}},
			"beta":  {ReservedMB: 256, LastPlacedAt: time.Now().UTC(), ActiveTasks: 1, ActiveExecs: []string{"exec-b"}},
		},
	}
	if err := st.Save(); err != nil {
		t.Fatalf("state save: %v", err)
	}

	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{Status: models.SnapshotHealthy}, nil
	})
	meta := d.Meta()
	if meta.ReservedMB != 768 {
		t.Fatalf("expected reserved_mb 768, got %d", meta.ReservedMB)
	}
}

func TestMetaMarksStaleSnapshots(t *testing.T) {
	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{Status: models.SnapshotHealthy}, nil
	})
	d.collectedAt = time.Now().UTC().Add(-6 * time.Minute)

	meta := d.Meta()
	if !meta.Stale {
		t.Fatal("expected metadata to be stale")
	}
	if meta.CacheAgeSec < 360 {
		t.Fatalf("expected cache age >= 360s, got %d", meta.CacheAgeSec)
	}
}

func TestRefreshInjectsReservationViewIntoSnapshot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 768, LastPlacedAt: time.Now().UTC(), ActiveTasks: 1, ActiveExecs: []string{"exec-a"}},
		},
	}
	if err := st.Save(); err != nil {
		t.Fatalf("state save: %v", err)
	}

	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Nodes: []models.NodeFacts{
				{
					Name:   "alpha",
					Status: models.StatusComplete,
					Resources: &models.Resources{
						RAMTotalMB: 8192,
						RAMFreeMB:  4096,
					},
				},
			},
		}, nil
	})
	path := filepath.Join(t.TempDir(), "snapshot.json")
	d.SetSnapshotPath(path)

	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	snap, ok := d.Snapshot()
	if !ok {
		t.Fatal("expected cached snapshot")
	}
	if got := snap.Nodes[0].Resources.RAMReservedMB; got != 768 {
		t.Fatalf("expected reserved RAM 768, got %d", got)
	}
	if got := snap.Nodes[0].Resources.RAMAllocatableMB; got != 3328 {
		t.Fatalf("expected allocatable RAM 3328, got %d", got)
	}
	if got := snap.Summary.TotalReservedMB; got != 768 {
		t.Fatalf("expected summary reserved RAM 768, got %d", got)
	}
	if got := snap.Summary.TotalAllocatableMB; got != 3328 {
		t.Fatalf("expected summary allocatable RAM 3328, got %d", got)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot file: %v", err)
	}
	var persisted models.ClusterSnapshot
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("unmarshal snapshot file: %v", err)
	}
	if got := persisted.Nodes[0].Resources.RAMAllocatableMB; got != 3328 {
		t.Fatalf("expected persisted allocatable RAM 3328, got %d", got)
	}
	if got := persisted.Summary.TotalAllocatableMB; got != 3328 {
		t.Fatalf("expected persisted summary allocatable RAM 3328, got %d", got)
	}
}

func TestCanReserveUsesNodeRAMCap(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "alpha",
				Resources: &models.Resources{
					RAMTotalMB: 8192,
				},
			},
		},
	}
	st := &state.ClusterState{
		Nodes: map[string]state.NodeState{
			"alpha": {ReservedMB: 2048},
		},
	}

	if !CanReserve(snap, st, "alpha", 1024) {
		t.Fatal("expected reservation to fit under cap")
	}
	if CanReserve(snap, st, "alpha", 6145) {
		t.Fatal("expected reservation to exceed cap")
	}
}
