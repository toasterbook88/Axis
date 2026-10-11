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

// Only a load signal makes an entry loaded: Ollama /api/ps or a llama.cpp
// process started with -m. An MLX resident comes from mlx_lm.server's
// /v1/models listing, which does not mean the weights are loaded.
func TestCatalogMarksMLXResidentListedNotLoaded(t *testing.T) {
	snap := &models.ClusterSnapshot{Nodes: []models.NodeFacts{{
		Name: "mac", Status: models.StatusComplete,
		ResidentModels: []models.ResidentModel{
			{Name: "mlx-model", Runtime: "mlx", Port: 8090, Source: "mlx-lm-api"},
			{Name: "gguf-model", Runtime: "llama.cpp", Port: 8080, Source: "llama-server-ps"},
			{Name: "ollama-model", Runtime: "ollama", Port: 11434, Source: "ollama-ps"},
		},
	}}}
	states := map[string]models.ModelCatalogState{}
	for _, e := range Catalog(snap, "test").Entries {
		states[e.Model] = e.State
	}
	if states["mlx-model"] != models.ModelCatalogListed {
		t.Fatalf("mlx-model state = %q, want listed", states["mlx-model"])
	}
	if states["gguf-model"] != models.ModelCatalogLoaded || states["ollama-model"] != models.ModelCatalogLoaded {
		t.Fatalf("states = %v, want llama.cpp and ollama residents loaded", states)
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

func TestCatalogUsesCollectorStateWhenPresent(t *testing.T) {
	snap := &models.ClusterSnapshot{Nodes: []models.NodeFacts{{
		Name: "n", Status: models.StatusComplete,
		ResidentModels: []models.ResidentModel{
			{Name: "warming", Runtime: "llama.cpp", Port: 8080, State: models.ModelCatalogLoading, LoadSignal: "health-loading"},
			{Name: "gone", Runtime: "llama.cpp", Port: 8081, State: models.ModelCatalogDown, LoadSignal: "health-silent"},
			{Name: "ok", Runtime: "llama.cpp", Port: 8082, State: models.ModelCatalogLoaded, LoadSignal: "health-ok", ContextWindow: 4096},
			{Name: "legacy", Runtime: "llama.cpp", Port: 8083},
		},
	}}}
	got := map[string]models.ModelCatalogEntry{}
	for _, e := range Catalog(snap, "test").Entries {
		got[e.Model] = e
	}
	if got["warming"].State != models.ModelCatalogLoading || got["gone"].State != models.ModelCatalogDown {
		t.Fatalf("states = %v / %v", got["warming"].State, got["gone"].State)
	}
	if got["ok"].State != models.ModelCatalogLoaded || got["ok"].ContextWindow != 4096 || got["ok"].LoadSignal != "health-ok" {
		t.Fatalf("ok = %+v", got["ok"])
	}
	if got["legacy"].State != models.ModelCatalogLoaded {
		t.Fatalf("legacy row without state = %q, want #507 mapping loaded", got["legacy"].State)
	}
}

func TestCatalogAddsAppleFoundationModelsEntry(t *testing.T) {
	ready := &models.ClusterSnapshot{Nodes: []models.NodeFacts{{
		Name: "mac", Status: models.StatusComplete,
		AppleFM: &models.AppleFoundationModelsInfo{Available: true, Verified: true, State: models.AppleFMReady, ContextWindow: 8192, Model: "AFM 3 Core", Capabilities: []string{"toolCalling"}},
	}}}
	entries := Catalog(ready, "test").Entries
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	e := entries[0]
	if e.Engine != "apple-foundation-models" || e.State != models.ModelCatalogLoaded || e.Model != "AFM 3 Core" || e.ContextWindow != 8192 || e.Locality != models.ModelLocalityOnNode || e.LoadSignal != "availability" || e.Port != 0 {
		t.Fatalf("apple entry = %+v", e)
	}

	for state, want := range map[string]int{models.AppleFMCold: 1, models.AppleFMUnavailable: 0, models.AppleFMUnknown: 0, "": 0} {
		snap := &models.ClusterSnapshot{Nodes: []models.NodeFacts{{Name: "mac", Status: models.StatusComplete, AppleFM: &models.AppleFoundationModelsInfo{State: state}}}}
		got := Catalog(snap, "test").Entries
		if len(got) != want {
			t.Fatalf("state %q: entries = %+v, want %d", state, got, want)
		}
		if want == 1 && (got[0].State != models.ModelCatalogInstalled || got[0].Model != "apple-foundation-models") {
			t.Fatalf("cold entry = %+v", got[0])
		}
	}
}

func TestCatalogNilSnapshot(t *testing.T) {
	got := Catalog(nil, "none")
	if got.Entries == nil || len(got.Entries) != 0 {
		t.Fatalf("entries = %#v, want empty non-nil slice", got.Entries)
	}
}
