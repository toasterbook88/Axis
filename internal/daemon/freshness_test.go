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

func TestFetchSnapshotDoesNotBackfillFreshnessFromMetadata(t *testing.T) {
	t.Setenv(auth.TokenEnvVar, "tok")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/snapshot/meta":
			_ = json.NewEncoder(w).Encode(Metadata{
				Source:        "daemon-cache",
				Ready:         true,
				PublicationID: "pub-1",
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
				Status:      models.SnapshotHealthy,
				Publication: &models.PublicationEnvelope{ID: "pub-1"},
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
	if snap.Freshness != nil {
		t.Fatalf("expected snapshot freshness to remain absent, got %+v", snap.Freshness)
	}
	if len(snap.Warnings) != 0 {
		t.Fatalf("metadata freshness must not add snapshot warnings, got %#v", snap.Warnings)
	}
}

func TestFetchSnapshotUsesSnapshotNativeFreshness(t *testing.T) {
	t.Setenv(auth.TokenEnvVar, "tok")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/snapshot/meta":
			_ = json.NewEncoder(w).Encode(Metadata{
				Source:        "daemon-cache",
				Ready:         true,
				PublicationID: "pub-1",
				Freshness: &models.DiscoveryFreshness{
					Source:  "metadata-must-not-win",
					Warning: "metadata warning must not leak",
				},
			})
		case "/snapshot":
			_ = json.NewEncoder(w).Encode(models.ClusterSnapshot{
				Status:      models.SnapshotHealthy,
				Publication: &models.PublicationEnvelope{ID: "pub-1"},
				Freshness: &models.DiscoveryFreshness{
					Source:  "snapshot-native",
					Warning: "snapshot warning",
				},
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
	if snap.Freshness == nil || snap.Freshness.Source != "snapshot-native" {
		t.Fatalf("expected snapshot-native freshness, got %+v", snap.Freshness)
	}
	if len(snap.Warnings) != 1 || snap.Warnings[0].Kind != "discovery" || snap.Warnings[0].Message != "snapshot warning" {
		t.Fatalf("expected only the snapshot-native discovery warning, got %#v", snap.Warnings)
	}
}

func TestHealthPayloadIncludesDiscoveryFreshness(t *testing.T) {
	payload := HealthPayload(&Metadata{
		Ready:         true,
		PublicationID: "pub-1",
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
	if got := payload["publication_id"]; got != "pub-1" {
		t.Fatalf("publication_id = %#v, want pub-1", got)
	}
}
