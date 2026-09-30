package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func cachePublicationSnap(ageSec int64) *models.ClusterSnapshot {
	return &models.ClusterSnapshot{
		Summary:     models.ClusterSummary{TotalNodes: 2},
		Publication: &models.PublicationEnvelope{CacheAgeSec: ageSec},
	}
}

func TestLoadCommandSnapshotFreshCacheHit(t *testing.T) {
	cachedSnap := cachePublicationSnap(60)
	liveCalled := false

	read, err := loadCommandSnapshot(
		context.Background(),
		false,
		false,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return cachedSnap, "daemon-cache", nil
		},
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			liveCalled = true
			return nil, "", nil
		},
	)
	if err != nil {
		t.Fatalf("loadCommandSnapshot: %v", err)
	}
	if liveCalled {
		t.Fatal("expected no live sweep on fresh cache hit")
	}
	if read.snap != cachedSnap {
		t.Fatal("expected cached snapshot to be returned")
	}
	if read.source != "daemon-cache" {
		t.Fatalf("source = %q, want daemon-cache", read.source)
	}
	if read.age != "1m0s" {
		t.Fatalf("age = %q, want 1m0s", read.age)
	}
	if len(read.snap.Warnings) != 0 {
		t.Fatalf("expected no warnings on fresh cache hit, got %#v", read.snap.Warnings)
	}
}

func TestLoadCommandSnapshotClocklessPublicationStaysOnCache(t *testing.T) {
	// A successful cache read with no clock is not evidence the publication
	// is older than the threshold; it must stay on the cache path.
	cachedSnap := &models.ClusterSnapshot{
		Summary: models.ClusterSummary{TotalNodes: 2},
	}

	read, err := loadCommandSnapshot(
		context.Background(),
		false,
		false,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return cachedSnap, "daemon-cache", nil
		},
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			t.Fatal("expected no live sweep for clockless publication")
			return nil, "", nil
		},
	)
	if err != nil {
		t.Fatalf("loadCommandSnapshot: %v", err)
	}
	if read.snap != cachedSnap {
		t.Fatal("expected cached snapshot to be returned")
	}
	if read.age != "none" {
		t.Fatalf("age = %q, want none", read.age)
	}
}

func TestLoadCommandSnapshotMissingCacheFallsBackToLive(t *testing.T) {
	liveSnap := &models.ClusterSnapshot{
		Summary: models.ClusterSummary{TotalNodes: 1},
	}

	read, err := loadCommandSnapshot(
		context.Background(),
		false,
		false,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return nil, "", errors.New("socket missing")
		},
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return liveSnap, "live", nil
		},
	)
	if err != nil {
		t.Fatalf("loadCommandSnapshot: %v", err)
	}
	if read.snap != liveSnap {
		t.Fatal("expected live snapshot fallback")
	}
	if read.source != "live-fallback" {
		t.Fatalf("source = %q, want live-fallback", read.source)
	}
	if read.age != "none" {
		t.Fatalf("age = %q, want none for live-because-missing", read.age)
	}
	if len(read.snap.Warnings) != 1 {
		t.Fatalf("expected one cache warning, got %#v", read.snap.Warnings)
	}
	if got := read.snap.Warnings[0].Message; got != "using live snapshot (daemon cache unavailable)" {
		t.Fatalf("warning = %q", got)
	}
}

func TestLoadCommandSnapshotStaleFallbackKeepsPublicationAge(t *testing.T) {
	liveSnap := &models.ClusterSnapshot{
		Summary: models.ClusterSummary{TotalNodes: 1},
	}

	read, err := loadCommandSnapshot(
		context.Background(),
		false,
		false,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return cachePublicationSnap(600), "daemon-cache", nil
		},
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return liveSnap, "live", nil
		},
	)
	if err != nil {
		t.Fatalf("loadCommandSnapshot: %v", err)
	}
	if read.snap != liveSnap {
		t.Fatal("expected live snapshot fallback")
	}
	if read.source != "live-fallback" {
		t.Fatalf("source = %q, want live-fallback", read.source)
	}
	// The age field reports the rejected daemon publication, not the fresh
	// live snapshot (normally 0s); this keeps origin metadata consistent
	// with the stale warning.
	if read.age != "10m0s" {
		t.Fatalf("age = %q, want 10m0s (rejected publication age)", read.age)
	}
	if len(read.snap.Warnings) != 1 {
		t.Fatalf("expected one cache warning, got %#v", read.snap.Warnings)
	}
	want := "using live snapshot (daemon cache stale, age 10m0s)"
	if got := read.snap.Warnings[0].Message; got != want {
		t.Fatalf("warning = %q, want %q", got, want)
	}
}

func TestLoadCommandSnapshotLiveSkipsCache(t *testing.T) {
	liveSnap := &models.ClusterSnapshot{
		Summary:   models.ClusterSummary{TotalNodes: 1},
		Timestamp: time.Now(),
	}

	read, err := loadCommandSnapshot(
		context.Background(),
		true,
		false,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			t.Fatal("expected no cache read under --live")
			return nil, "", nil
		},
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return liveSnap, "live", nil
		},
	)
	if err != nil {
		t.Fatalf("loadCommandSnapshot: %v", err)
	}
	if read.snap != liveSnap {
		t.Fatal("expected live snapshot")
	}
	if read.source != "live" {
		t.Fatalf("source = %q, want live", read.source)
	}
	if len(read.snap.Warnings) != 0 {
		t.Fatalf("expected no warnings on --live, got %#v", read.snap.Warnings)
	}
}

func TestLoadCommandSnapshotCachedOnlyStaleFailsClosed(t *testing.T) {
	_, err := loadCommandSnapshot(
		context.Background(),
		false,
		true,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return cachePublicationSnap(600), "daemon-cache", nil
		},
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			t.Fatal("expected no live sweep under --cached-only")
			return nil, "", nil
		},
	)
	if err == nil {
		t.Fatal("expected stale cached-only failure")
	}
	want := "daemon cache stale: publication age 10m0s exceeds 5m0s"
	if got := err.Error(); got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestLoadCommandSnapshotCachedOnlyMissingFailsClosed(t *testing.T) {
	_, err := loadCommandSnapshot(
		context.Background(),
		false,
		true,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return nil, "", errors.New("socket missing")
		},
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			t.Fatal("expected no live sweep under --cached-only")
			return nil, "", nil
		},
	)
	if err == nil {
		t.Fatal("expected missing cached-only failure")
	}
	if got := err.Error(); !strings.Contains(got, "daemon cache unavailable") || !strings.Contains(got, "socket missing") {
		t.Fatalf("error = %q, want daemon cache unavailable wrapping socket missing", got)
	}
}

func TestStatusWatchCachedOnlyFailsClosedOnError(t *testing.T) {
	previousFetch := fetchStatusSnapshot
	previousLive := loadStatusLiveSnapshot
	t.Cleanup(func() {
		fetchStatusSnapshot = previousFetch
		loadStatusLiveSnapshot = previousLive
	})
	fetchStatusSnapshot = func(context.Context, string) (*models.ClusterSnapshot, string, error) {
		return nil, "", errors.New("socket missing")
	}
	loadStatusLiveSnapshot = func(context.Context) (*models.ClusterSnapshot, string, error) {
		t.Fatal("expected no live sweep under --cached-only")
		return nil, "", nil
	}

	cmd := statusCmd()
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"--watch", "--watch-interval", "1ms", "--cached-only"})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected cached-only watch error, got nil (stderr=%q)", errOut.String())
	}
	if !strings.Contains(err.Error(), "daemon cache unavailable") {
		t.Fatalf("error = %q, want daemon cache unavailable", err.Error())
	}
	if !strings.Contains(errOut.String(), "daemon cache unavailable") {
		t.Fatalf("expected rendered error on stderr, got %q", errOut.String())
	}
}
