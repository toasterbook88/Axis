package modelinventory

import (
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

// Confirmed loopback endpoints are listed, never loaded; candidates named no
// model and never reach the catalog.
func TestCatalogListsConfirmedEndpointsOnly(t *testing.T) {
	snap := &models.ClusterSnapshot{Nodes: []models.NodeFacts{{
		Name:   "node-a",
		Status: models.StatusComplete,
		LocalEndpoints: &models.LocalEndpoints{
			Confirmed: []models.LocalEndpoint{
				{Port: 8000, Bind: "loopback", Dialect: "openai", Engine: "vllm", Models: []models.LocalEndpointModel{{Name: "qwen3-8b", ContextWindow: 32768}}},
				{Port: 11500, Bind: "any", Dialect: "ollama", Engine: "ollama", Models: []models.LocalEndpointModel{{Name: "big:cloud", RemoteHost: "https://cloud.example.com"}}},
			},
			Candidates: []models.LocalEndpointCandidate{{Port: 4000, Reason: "auth-required"}},
		},
	}}}
	got := Catalog(snap, "test").Entries
	if len(got) != 2 {
		t.Fatalf("entries = %+v, want the two confirmed models only", got)
	}
	cloud, vllm := got[0], got[1]
	if vllm.Model != "qwen3-8b" || vllm.Engine != "vllm" || vllm.State != models.ModelCatalogListed || vllm.LoadSignal != "none" ||
		vllm.Port != 8000 || !vllm.LoopbackOnly || vllm.ContextWindow != 32768 || vllm.InstanceID != "" {
		t.Fatalf("vllm entry = %+v", vllm)
	}
	if cloud.Engine != "ollama" || cloud.Port != 11500 || cloud.LoopbackOnly || cloud.Locality != models.ModelLocalityCloudProxy || cloud.State != models.ModelCatalogListed {
		t.Fatalf("second ollama entry = %+v", cloud)
	}
}

// A generic OpenAI-compatible listing does not say where inference runs.
func TestCatalogGenericEndpointLocalityIsUnknown(t *testing.T) {
	snap := &models.ClusterSnapshot{Nodes: []models.NodeFacts{{
		Name: "node-a", Status: models.StatusComplete,
		LocalEndpoints: &models.LocalEndpoints{Confirmed: []models.LocalEndpoint{
			{Port: 4000, Bind: "loopback", Dialect: "openai", Engine: "openai-compatible", Models: []models.LocalEndpointModel{{Name: "router/auto"}}},
		}},
	}}}
	got := Catalog(snap, "test").Entries
	if len(got) != 1 || got[0].Locality != models.ModelLocalityUnknown {
		t.Fatalf("entries = %+v, want locality unknown", got)
	}
}

// owned_by and /api/tags are self-declared; only a matching process name
// earns on-node locality.
func TestCatalogEndpointLocalityNeedsMatchingProcess(t *testing.T) {
	snap := &models.ClusterSnapshot{Nodes: []models.NodeFacts{{
		Name: "node-a", Status: models.StatusComplete,
		LocalEndpoints: &models.LocalEndpoints{Confirmed: []models.LocalEndpoint{
			{Port: 9000, Bind: "loopback", Dialect: "openai", Engine: "vllm", Process: "litellm", Models: []models.LocalEndpointModel{{Name: "claims-vllm"}}},
			{Port: 9001, Bind: "loopback", Dialect: "ollama", Engine: "ollama", Process: "python3", Models: []models.LocalEndpointModel{{Name: "claims-ollama"}}},
			{Port: 8000, Bind: "loopback", Dialect: "openai", Engine: "vllm", Process: "vllm", Models: []models.LocalEndpointModel{{Name: "real-vllm"}}},
		}},
	}}}
	want := map[string]models.ModelLocality{"claims-vllm": models.ModelLocalityUnknown, "claims-ollama": models.ModelLocalityUnknown, "real-vllm": models.ModelLocalityOnNode}
	for _, e := range Catalog(snap, "test").Entries {
		if e.Locality != want[e.Model] {
			t.Fatalf("%s locality = %s, want %s", e.Model, e.Locality, want[e.Model])
		}
	}
}
