package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/toasterbook88/axis/internal/daemon"
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

func TestFetchCachedSnapshotReadsDaemonEndpoints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/snapshot/meta":
			_ = json.NewEncoder(w).Encode(daemon.Metadata{
				Source:             "daemon-cache",
				Ready:              true,
				RefreshIntervalSec: 60,
			})
		case "/snapshot":
			_ = json.NewEncoder(w).Encode(models.ClusterSnapshot{
				Status: models.SnapshotHealthy,
				Summary: models.ClusterSummary{
					TotalNodes: 3,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	snap, source, err := fetchCachedSnapshot(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetchCachedSnapshot: %v", err)
	}
	if source != "daemon-cache" {
		t.Fatalf("expected daemon-cache source, got %q", source)
	}
	if snap.Summary.TotalNodes != 3 {
		t.Fatalf("expected cached snapshot total nodes 3, got %d", snap.Summary.TotalNodes)
	}
}
