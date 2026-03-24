package axismcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
)

func TestCurrentSnapshotUsesCacheWhenRequested(t *testing.T) {
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
					TotalNodes: 2,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	snap, err := currentSnapshot(context.Background(), true, server.URL)
	if err != nil {
		t.Fatalf("currentSnapshot: %v", err)
	}
	if snap.Summary.TotalNodes != 2 {
		t.Fatalf("expected cached snapshot total nodes 2, got %d", snap.Summary.TotalNodes)
	}
}
