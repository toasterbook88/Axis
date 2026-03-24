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
)

func TestRefreshStoresSnapshotAndMeta(t *testing.T) {
	d := New(30*time.Second, func(ctx context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{
			Timestamp: time.Unix(1700000000, 0).UTC(),
			Status:    models.SnapshotHealthy,
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

	meta := d.Meta()
	if !meta.Ready {
		t.Fatal("expected daemon to be ready")
	}
	if meta.Source != "daemon-cache" {
		t.Fatalf("expected source daemon-cache, got %q", meta.Source)
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
