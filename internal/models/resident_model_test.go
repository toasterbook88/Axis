package models

import (
	"encoding/json"
	"strings"
	"testing"
)

// Older payloads have no load-state fields; they must decode unchanged and
// re-encode without the new keys.
func TestResidentModelNewFieldsAreOptional(t *testing.T) {
	var r ResidentModel
	if err := json.Unmarshal([]byte(`{"name":"m","runtime":"llama.cpp","port":8080}`), &r); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"state", "load_signal", "context_window", "provenance"} {
		if strings.Contains(string(out), `"`+key+`"`) {
			t.Fatalf("encoded %s contains %q for a row without it", out, key)
		}
	}
}

func TestResidentModelCarriesLoadState(t *testing.T) {
	in := `{"name":"m","runtime":"llama.cpp","state":"loading","load_signal":"health-loading","context_window":4096,"provenance":{"state":"GET /health 503"}}`
	var r ResidentModel
	if err := json.Unmarshal([]byte(in), &r); err != nil {
		t.Fatal(err)
	}
	if r.State != ModelCatalogLoading || r.LoadSignal != "health-loading" || r.ContextWindow != 4096 || r.Provenance["state"] != "GET /health 503" {
		t.Fatalf("decoded %+v", r)
	}
}

func TestResidentModelServingRequiresLoadedStateOrLegacyRuntimeEvidence(t *testing.T) {
	tests := []struct {
		name    string
		model   ResidentModel
		serving bool
	}{
		{name: "explicit loaded", model: ResidentModel{Runtime: "mlx", State: ModelCatalogLoaded}, serving: true},
		{name: "loading", model: ResidentModel{Runtime: "llama.cpp", State: ModelCatalogLoading}},
		{name: "listed", model: ResidentModel{Runtime: "mlx", State: ModelCatalogListed}},
		{name: "down", model: ResidentModel{Runtime: "llama.cpp", State: ModelCatalogDown}},
		{name: "legacy ollama", model: ResidentModel{Runtime: "ollama"}, serving: true},
		{name: "legacy llama", model: ResidentModel{Runtime: "llama.cpp"}, serving: true},
		{name: "legacy mlx", model: ResidentModel{Runtime: "mlx"}},
		{name: "unknown runtime", model: ResidentModel{Runtime: "custom"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.model.Serving(); got != tc.serving {
				t.Fatalf("Serving() = %v, want %v", got, tc.serving)
			}
		})
	}
}

func TestAppleFMInfoCarriesFacts(t *testing.T) {
	in := `{"available":true,"verified":true,"state":"ready","context_window":8192,"model":"AFM 3 Core","capabilities":["toolCalling","vision"]}`
	var a AppleFoundationModelsInfo
	if err := json.Unmarshal([]byte(in), &a); err != nil {
		t.Fatal(err)
	}
	if a.State != AppleFMReady || a.ContextWindow != 8192 || a.Model != "AFM 3 Core" || len(a.Capabilities) != 2 {
		t.Fatalf("decoded %+v", a)
	}
}
