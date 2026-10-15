package modelinventory

import (
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestCatalogAddsPrivateCloudComputeAsCloudProxy(t *testing.T) {
	pcc := &models.ApplePCCInfo{State: models.AppleFMReady, ContextWindow: 32768, Capabilities: []string{"reasoning"}}
	snap := &models.ClusterSnapshot{Nodes: []models.NodeFacts{{
		Name: "mac", Status: models.StatusComplete,
		AppleFM: &models.AppleFoundationModelsInfo{State: models.AppleFMReady, Model: "AFM 3 Core", PCC: pcc},
	}}}
	var got *models.ModelCatalogEntry
	for _, e := range Catalog(snap, "test").Entries {
		if e.Engine == "apple-pcc" {
			got = &e
		}
	}
	if got == nil || got.State != models.ModelCatalogListed || got.Locality != models.ModelLocalityCloudProxy || got.ContextWindow != 32768 || got.Port != 0 {
		t.Fatalf("pcc entry = %+v", got)
	}
	pcc.QuotaLimitReached = true
	for _, e := range Catalog(snap, "test").Entries {
		if e.Engine == "apple-pcc" && (e.State != models.ModelCatalogDown || e.LoadSignal != "quota-limit-reached") {
			t.Fatalf("quota-exhausted pcc = %+v", e)
		}
	}
	pcc.State = models.AppleFMUnavailable
	for _, e := range Catalog(snap, "test").Entries {
		if e.Engine == "apple-pcc" {
			t.Fatalf("unavailable pcc must not be listed: %+v", e)
		}
	}
}
