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
		false,
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
	if len(snap.Warnings) != 0 {
		t.Fatalf("expected no warnings on cached hit, got %#v", snap.Warnings)
	}
}

func TestCollectStatusSnapshotFallsBackToLiveWhenCacheFails(t *testing.T) {
	liveSnap := &models.ClusterSnapshot{
		Summary: models.ClusterSummary{TotalNodes: 1},
	}

	snap, source, err := collectStatusSnapshot(
		context.Background(),
		true,
		false,
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
	if source != "live-fallback" {
		t.Fatalf("expected live-fallback source, got %q", source)
	}
	if len(snap.Warnings) != 1 {
		t.Fatalf("expected one cache warning, got %#v", snap.Warnings)
	}
	if snap.Warnings[0].Kind != "cache" {
		t.Fatalf("warning kind = %q, want cache", snap.Warnings[0].Kind)
	}
	if got := snap.Warnings[0].Message; got != "daemon cache unavailable; fell back to live snapshot: context deadline exceeded" {
		t.Fatalf("warning message = %q", got)
	}
}

func TestCollectStatusSnapshotCachedOnlyFailsWhenCacheFails(t *testing.T) {
	snap, source, err := collectStatusSnapshot(
		context.Background(),
		false,
		true,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return nil, "", context.DeadlineExceeded
		},
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			t.Fatal("expected no live fallback in cached-only mode")
			return nil, "", nil
		},
	)
	if err == nil {
		t.Fatal("expected cached-only cache failure")
	}
	if snap != nil {
		t.Fatalf("expected nil snapshot on cached-only failure, got %#v", snap)
	}
	if source != "" {
		t.Fatalf("expected empty source on cached-only failure, got %q", source)
	}
	if got := err.Error(); got != "daemon cache unavailable: context deadline exceeded" {
		t.Fatalf("unexpected cached-only error: %q", got)
	}
}
