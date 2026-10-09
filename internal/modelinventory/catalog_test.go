package modelinventory

import (
	"slices"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func catalogTestSnapshot(observed time.Time) *models.ClusterSnapshot {
	return &models.ClusterSnapshot{
		Timestamp:   observed,
		Publication: &models.PublicationEnvelope{ID: "pub-cat-1"},
		Nodes: []models.NodeFacts{
			{
				Name:        "node-b",
				Status:      models.StatusComplete,
				CollectedAt: observed.Add(-time.Second),
				Ollama: &models.OllamaInfo{
					Installed: true,
					Models:    []string{"coder:7b", "embed:latest", "big:cloud"},
					Catalog: []models.OllamaModelEntry{
						{Name: "coder:7b", Capabilities: []string{"completion", "tools"}, Family: "qwen2", ParameterSize: "7B", Quantization: "Q4_K_M", SizeBytes: 4_000_000_000},
						{Name: "embed:latest", Capabilities: []string{"embedding"}},
						{Name: "big:cloud", RemoteHost: "https://cloud.example.com:443", Capabilities: []string{"completion"}},
					},
				},
				ResidentModels: []models.ResidentModel{
					{Name: "coder:7b", Runtime: "ollama", Processor: "gpu", Port: 11434},
				},
			},
			{
				Name:        "node-a",
				Status:      models.StatusComplete,
				CollectedAt: observed.Add(-2 * time.Second),
				ResidentModels: []models.ResidentModel{
					{Name: "bitnet-2B", Runtime: "llama.cpp", Port: 8080},
				},
			},
			{
				Name:   "node-c",
				Status: models.StatusUnreachable,
				Error:  "ssh: unable to authenticate",
			},
			{
				// Older collector: names only, no catalog.
				Name:        "node-d",
				Status:      models.StatusComplete,
				CollectedAt: observed.Add(-3 * time.Second),
				Ollama:      &models.OllamaInfo{Installed: true, Models: []string{"tiny:1b"}},
			},
		},
	}
}

func TestCatalogMergesLoadedAndInstalledPerNode(t *testing.T) {
	observed := time.Date(2026, 10, 8, 22, 0, 0, 0, time.UTC)
	got := Catalog(catalogTestSnapshot(observed), "daemon-cache")

	if got.Source != "daemon-cache" || got.PublicationID != "pub-cat-1" || !got.ObservedAt.Equal(observed) {
		t.Fatalf("authority = %q %q %s", got.Source, got.PublicationID, got.ObservedAt)
	}

	type key struct{ node, model string }
	byKey := map[key]models.ModelCatalogEntry{}
	for _, e := range got.Entries {
		k := key{e.Node, e.Model}
		if _, dup := byKey[k]; dup {
			t.Fatalf("duplicate entry for %v: %+v", k, got.Entries)
		}
		byKey[k] = e
	}
	if len(got.Entries) != 5 {
		t.Fatalf("entries = %+v, want 5 (bitnet, coder, embed, big, tiny)", got.Entries)
	}

	// Loaded and installed facts for one model merge into one loaded entry
	// that keeps the installed metadata and the lifecycle instance ID.
	coder := byKey[key{"node-b", "coder:7b"}]
	if coder.State != models.ModelCatalogLoaded || coder.Engine != "ollama" || coder.Port != 11434 {
		t.Fatalf("coder = %+v, want loaded ollama on 11434", coder)
	}
	if !slices.Equal(coder.Capabilities, []string{"completion", "tools"}) || coder.ParameterSize != "7B" || coder.Quantization != "Q4_K_M" {
		t.Fatalf("coder metadata = %+v, want installed-catalog details kept", coder)
	}
	inv := FromSnapshot(catalogTestSnapshot(observed), "daemon-cache")
	if coder.InstanceID == "" || !slices.ContainsFunc(inv.Instances, func(i models.ModelInstance) bool { return i.ID == coder.InstanceID }) {
		t.Fatalf("coder instance_id %q does not match the inventory", coder.InstanceID)
	}

	if e := byKey[key{"node-b", "embed:latest"}]; e.State != models.ModelCatalogInstalled || !slices.Equal(e.Capabilities, []string{"embedding"}) {
		t.Fatalf("embed = %+v, want installed embedding model", e)
	}
	big := byKey[key{"node-b", "big:cloud"}]
	if big.Locality != models.ModelLocalityCloudProxy || big.RemoteHost != "https://cloud.example.com:443" {
		t.Fatalf("big = %+v, want cloud-proxy with remote host", big)
	}
	if coder.Locality != models.ModelLocalityOnNode {
		t.Fatalf("coder locality = %q, want on-node", coder.Locality)
	}

	bitnet := byKey[key{"node-a", "bitnet-2B"}]
	if bitnet.State != models.ModelCatalogLoaded || bitnet.Engine != "llama.cpp" || bitnet.Port != 8080 || len(bitnet.Capabilities) != 0 {
		t.Fatalf("bitnet = %+v, want loaded llama.cpp, capabilities unknown", bitnet)
	}

	// A node without a catalog still lists its names, capabilities unknown.
	if tiny := byKey[key{"node-d", "tiny:1b"}]; tiny.State != models.ModelCatalogInstalled || tiny.Locality != models.ModelLocalityOnNode || len(tiny.Capabilities) != 0 {
		t.Fatalf("tiny = %+v, want installed with unknown capabilities", tiny)
	}

	if len(got.Unobserved) != 1 || got.Unobserved[0].Node != "node-c" || got.Unobserved[0].Status != models.StatusUnreachable || got.Unobserved[0].Reason != "ssh: unable to authenticate" {
		t.Fatalf("unobserved = %+v, want node-c with its reason", got.Unobserved)
	}

	for i := 1; i < len(got.Entries); i++ {
		a, b := got.Entries[i-1], got.Entries[i]
		if a.Node > b.Node || (a.Node == b.Node && a.Model > b.Model) {
			t.Fatalf("entries not sorted by node, model: %+v", got.Entries)
		}
	}
}

// The catalog is a pure function of the snapshot: same input, same bytes,
// whichever node computes it.
func TestCatalogIsDeterministic(t *testing.T) {
	observed := time.Date(2026, 10, 8, 22, 0, 0, 0, time.UTC)
	a := Catalog(catalogTestSnapshot(observed), "daemon-cache")
	for range 20 {
		b := Catalog(catalogTestSnapshot(observed), "daemon-cache")
		if !slices.EqualFunc(a.Entries, b.Entries, func(x, y models.ModelCatalogEntry) bool {
			return x.Node == y.Node && x.Model == y.Model && x.Engine == y.Engine && x.State == y.State && x.InstanceID == y.InstanceID
		}) {
			t.Fatalf("catalog order differs between runs:\n%+v\n%+v", a.Entries, b.Entries)
		}
	}
}

func TestCatalogNilSnapshot(t *testing.T) {
	got := Catalog(nil, "none")
	if got.Entries == nil || len(got.Entries) != 0 {
		t.Fatalf("entries = %#v, want empty non-nil slice", got.Entries)
	}
}
