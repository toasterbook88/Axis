package main

import (
	"context"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestCollectStatusSnapshotPrefersCacheWhenAvailable(t *testing.T) {
	cachedSnap := &models.ClusterSnapshot{
		Summary: models.ClusterSummary{TotalNodes: 2},
	}
	liveSnap := &models.ClusterSnapshot{
		Summary: models.ClusterSummary{TotalNodes: 1},
	}

	snap, source, err := collectStatusSnapshot(
		context.Background(),
		true,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return cachedSnap, "daemon-cache", nil
		},
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return liveSnap, "live", nil
		},
	)
	if err != nil {
		t.Fatalf("collectStatusSnapshot: %v", err)
	}
	if snap != cachedSnap {
		t.Fatal("expected cached snapshot to be returned")
	}
	if source != "daemon-cache" {
		t.Fatalf("expected daemon-cache source, got %q", source)
	}
}

func TestCollectStatusSnapshotFallsBackToLiveWhenCacheFails(t *testing.T) {
	liveSnap := &models.ClusterSnapshot{
		Summary: models.ClusterSummary{TotalNodes: 1},
	}

	snap, source, err := collectStatusSnapshot(
		context.Background(),
		true,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return nil, "", context.DeadlineExceeded
		},
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return liveSnap, "live", nil
		},
	)
	if err != nil {
		t.Fatalf("collectStatusSnapshot: %v", err)
	}
	if snap != liveSnap {
		t.Fatal("expected live snapshot fallback")
	}
	if source != "live" {
		t.Fatalf("expected live source, got %q", source)
	}
}
