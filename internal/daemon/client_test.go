package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestFetchSnapshotReadsDaemonEndpoints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/snapshot/meta":
			_ = json.NewEncoder(w).Encode(Metadata{
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

	snap, source, err := FetchSnapshot(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("FetchSnapshot: %v", err)
	}
	if source != "daemon-cache" {
		t.Fatalf("expected daemon-cache source, got %q", source)
	}
	if snap.Summary.TotalNodes != 3 {
		t.Fatalf("expected cached snapshot total nodes 3, got %d", snap.Summary.TotalNodes)
	}
}

func TestFetchMetaReadsDaemonMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/snapshot/meta" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(Metadata{
			Source:             "daemon-cache",
			Ready:              true,
			RefreshIntervalSec: 60,
			Version:            Version,
			CacheAgeSec:        12,
			Stale:              false,
		})
	}))
	defer server.Close()

	meta, err := FetchMeta(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("FetchMeta: %v", err)
	}
	if meta.Version != Version {
		t.Fatalf("expected version %q, got %q", Version, meta.Version)
	}
	if meta.CacheAgeSec != 12 {
		t.Fatalf("expected cache age 12, got %d", meta.CacheAgeSec)
	}
}
