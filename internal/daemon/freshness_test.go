package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/models"
)

func TestFetchSnapshotBackfillsFreshnessFromMetadata(t *testing.T) {
	t.Setenv(auth.TokenEnvVar, "tok")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/snapshot/meta":
			_ = json.NewEncoder(w).Encode(Metadata{
				Source: "daemon-cache",
				Ready:  true,
				Freshness: &models.DiscoveryFreshness{
					Source:           "beacon-registry",
					ExpectedWindowMS: 2250,
					ObservedWindowMS: 500,
					CompletedWindow:  false,
					Warning:          "results may miss peer nodes",
				},
			})
		case "/snapshot":
			_ = json.NewEncoder(w).Encode(models.ClusterSnapshot{
				Status: models.SnapshotHealthy,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	snap, _, err := FetchSnapshot(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("FetchSnapshot() error = %v", err)
	}
	if snap.Freshness == nil || snap.Freshness.Source != "beacon-registry" {
		t.Fatalf("expected freshness from metadata, got %+v", snap.Freshness)
	}
	if len(snap.Warnings) != 1 || snap.Warnings[0].Kind != "discovery" {
		t.Fatalf("expected discovery warning from freshness, got %#v", snap.Warnings)
	}
}

func TestHealthPayloadIncludesDiscoveryFreshness(t *testing.T) {
	payload := HealthPayload(&Metadata{
		Ready: true,
		Freshness: &models.DiscoveryFreshness{
			Source:           "beacon-registry",
			ExpectedWindowMS: 2250,
			ObservedWindowMS: 2250,
			CompletedWindow:  true,
		},
	})

	raw, ok := payload["discovery_freshness"]
	if !ok {
		t.Fatal("expected discovery_freshness in health payload")
	}
	freshness, ok := raw.(*models.DiscoveryFreshness)
	if !ok || freshness.Source != "beacon-registry" {
		t.Fatalf("unexpected discovery_freshness payload: %#v", raw)
	}
}
