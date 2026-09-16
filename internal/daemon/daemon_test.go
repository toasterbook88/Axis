package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
)

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
	if snap.Publication == nil || meta.PublicationID != snap.Publication.ID {
		t.Fatalf("failed refresh detached metadata from preserved publication: meta=%q snapshot=%+v", meta.PublicationID, snap.Publication)
	}
}

func TestRefreshLedgerLoadFailurePreservesPreviousSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	freeRAM := int64(4096)
	d := New(time.Minute, func(context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Nodes: []models.NodeFacts{{
				Name:      "alpha",
				Status:    models.StatusComplete,
				Resources: &models.Resources{RAMTotalMB: 8192, RAMFreeMB: freeRAM},
			}},
			Summary: models.ClusterSummary{TotalFreeRAMMB: freeRAM},
		}, nil
	})
	d.SetSnapshotPath("")

	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if err := os.WriteFile(reservation.Path(), []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	freeRAM = 2048
	if err := d.Refresh(context.Background()); err == nil {
		t.Fatal("expected refresh to fail when ledger cannot load")
	}

	snap, ok := d.Snapshot()
	if !ok {
		t.Fatal("expected previous snapshot to remain available")
	}
	if got := snap.Summary.TotalFreeRAMMB; got != 4096 {
		t.Fatalf("previous snapshot replaced after ledger failure: got free RAM %d, want 4096", got)
	}
	if got := d.Meta().LastError; !strings.Contains(got, "load reservation ledger") {
		t.Fatalf("expected ledger failure in metadata, got %q", got)
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
	if got := d.Meta().LastRefreshTrigger; got != "manual" {
		t.Fatalf("expected RefreshNow trigger manual, got %q", got)
	}
}

func TestRefreshWithTriggerStoresExplicitTrigger(t *testing.T) {
	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{Status: models.SnapshotHealthy}, nil
	})
	d.SetSnapshotPath("")

	if err := d.RefreshWithTrigger(context.Background(), execution.StateChangeExecutionFinished); err != nil {
		t.Fatalf("RefreshWithTrigger: %v", err)
	}
	if got := d.Meta().LastRefreshTrigger; got != execution.StateChangeExecutionFinished {
		t.Fatalf("expected explicit execution trigger, got %q", got)
	}
}

func TestMetaIncludesReservedMB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{Status: models.SnapshotHealthy}, nil
	})
	d.ledger.SetNodeCapacity("alpha", 8192)
	d.ledger.SetNodeCapacity("beta", 8192)
	d.ledger.Reserve(reservation.Entry{
		ID:          "exec-a",
		Node:        "alpha",
		OwnerExecID: "exec-a",
		RAMMB:       512,
	})
	d.ledger.Reserve(reservation.Entry{
		ID:          "exec-b",
		Node:        "beta",
		OwnerExecID: "exec-b",
		RAMMB:       256,
	})
	meta := d.Meta()
	if meta.ReservedMB != 768 {
		t.Fatalf("expected reserved_mb 768, got %d", meta.ReservedMB)
	}
}

func TestWatchConfigInvalidatesCacheWhenConfigDisappears(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configPath := filepath.Join(home, "nodes.yaml")
	if err := os.WriteFile(configPath, []byte("nodes:\n  - name: alpha\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	prevPoll := watchConfigPollInterval
	watchConfigPollInterval = 10 * time.Millisecond
	defer func() { watchConfigPollInterval = prevPoll }()

	d := New(time.Minute, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		if _, err := os.Stat(configPath); err != nil {
			return nil, err
		}
		return &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Summary: models.ClusterSummary{
				TotalNodes: 1,
			},
		}, nil
	})
	d.SetSnapshotPath("")

	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	if !d.Meta().Ready {
		t.Fatal("expected initial cache readiness")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.WatchConfig(ctx, configPath)
	time.Sleep(3 * watchConfigPollInterval)

	if err := os.Remove(configPath); err != nil {
		t.Fatalf("remove config: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		meta := d.Meta()
		if !meta.Ready && meta.LastRefreshTrigger == "config-change" && strings.Contains(meta.LastError, "no such file or directory") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("expected config removal to invalidate cache, got meta=%+v", d.Meta())
}

func TestCanReserveUsesReservableRAMCap(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Nodes: []models.NodeFacts{
			{
				Name: "alpha",
				Resources: &models.Resources{
					RAMTotalMB: 8192,
					RAMFreeMB:  3072,
				},
			},
		},
	}
	snap.Nodes[0].RAMReservedMB = 2048

	if !CanReserve(snap, "alpha", 1024) {
		t.Fatal("expected reservation to fit under cap")
	}
	if CanReserve(snap, "alpha", 1025) {
		t.Fatal("expected reservation to exceed live reservable cap")
	}
}
