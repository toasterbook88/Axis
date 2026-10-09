package modellife

import (
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestPickServingNodePrefersResidentOverListening(t *testing.T) {
	nodes := []models.NodeFacts{
		{
			Name:   "library",
			Status: models.StatusComplete,
			Ollama: &models.OllamaInfo{Listening: true, Models: []string{"mistral"}},
		},
		{
			Name:   "warm",
			Status: models.StatusComplete,
			ResidentModels: []models.ResidentModel{{
				Name: "mistral:latest", Runtime: "ollama", WarmthScore: 0.2,
			}},
		},
	}
	got, err := PickServingNode(nodes, "mistral")
	if err != nil {
		t.Fatal(err)
	}
	if got.Node != "warm" || !got.Already || got.Runtime != "ollama" {
		t.Fatalf("pick=%#v", got)
	}
	if got.Sentence() != "picked warm: ollama already serving mistral" {
		t.Fatalf("sentence=%q", got.Sentence())
	}
}

func TestPickServingNodeBreaksResidentTiesByWarmthThenName(t *testing.T) {
	nodes := []models.NodeFacts{
		{Name: "b", Status: models.StatusComplete, ResidentModels: []models.ResidentModel{{Name: "mistral", Runtime: "llama.cpp", WarmthScore: 0.9}}},
		{Name: "a", Status: models.StatusComplete, ResidentModels: []models.ResidentModel{{Name: "mistral", Runtime: "llama.cpp", WarmthScore: 0.1}}},
		{Name: "c", Status: models.StatusComplete, ResidentModels: []models.ResidentModel{{Name: "mistral", Runtime: "mlx", WarmthScore: 0.9}}},
	}
	got, err := PickServingNode(nodes, "Mistral")
	if err != nil {
		t.Fatal(err)
	}
	if got.Node != "b" || got.Runtime != "llama.cpp" || !got.Already {
		t.Fatalf("warmth tie pick=%#v", got)
	}
	nodes[0].ResidentModels[0].WarmthScore = 0.4
	got, err = PickServingNode(nodes, "mistral")
	if err != nil {
		t.Fatal(err)
	}
	if got.Node != "c" || got.Runtime != "mlx" {
		t.Fatalf("equal warmth pick=%#v", got)
	}
}

// A down server (process seen, endpoint silent) is not already serving.
func TestPickServingNodeSkipsDownResident(t *testing.T) {
	nodes := []models.NodeFacts{
		{Name: "dead", Status: models.StatusComplete, ResidentModels: []models.ResidentModel{{Name: "mistral", Runtime: "llama.cpp", WarmthScore: 0.9, State: models.ModelCatalogDown}}},
		{Name: "live", Status: models.StatusComplete, ResidentModels: []models.ResidentModel{{Name: "mistral", Runtime: "llama.cpp", State: models.ModelCatalogLoaded}}},
	}
	got, err := PickServingNode(nodes, "mistral")
	if err != nil || got.Node != "live" || !got.Already {
		t.Fatalf("pick=%#v err=%v, want live", got, err)
	}
}

func TestPickServingNodeUsesListeningOllamaLibrary(t *testing.T) {
	nodes := []models.NodeFacts{
		{Name: "partial", Status: models.StatusPartial, Ollama: &models.OllamaInfo{Listening: true, Models: []string{"mistral"}}},
		{Name: "zeta", Status: models.StatusComplete, Ollama: &models.OllamaInfo{Listening: true, Models: []string{"mistral:latest"}}},
		{Name: "alpha", Status: models.StatusComplete, Ollama: &models.OllamaInfo{Listening: true, Models: []string{"other"}}},
		{Name: "beta", Status: models.StatusComplete, Tools: []models.ToolInfo{{Name: "llama-server", Path: "/usr/bin/llama-server"}}},
		{Name: "mid", Status: models.StatusComplete, Ollama: &models.OllamaInfo{Installed: true, Running: true, Models: []string{"mistral"}}},
	}
	got, err := PickServingNode(nodes, "mistral")
	if err != nil {
		t.Fatal(err)
	}
	if got.Node != "zeta" || got.Already || got.Sentence() != "picked zeta: ollama already listening" {
		t.Fatalf("pick=%#v sentence=%q", got, got.Sentence())
	}
}

func TestPickServingNodeRefusesWhenNoRuntimeIsAlreadyThere(t *testing.T) {
	_, err := PickServingNode(nil, "mistral")
	if err == nil || !strings.Contains(err.Error(), `no complete node already has a runtime for "mistral"`) {
		t.Fatalf("err=%v", err)
	}
	_, err = PickServingNode([]models.NodeFacts{{Name: "storage", Status: models.StatusComplete}}, "  ")
	if err == nil || !strings.Contains(err.Error(), "model name is required") {
		t.Fatalf("blank err=%v", err)
	}
}

func TestPickServingNodeIgnoresUnknownResidentRuntime(t *testing.T) {
	nodes := []models.NodeFacts{{
		Name:   "storage",
		Status: models.StatusComplete,
		ResidentModels: []models.ResidentModel{{
			Name: "mistral", Runtime: "custom-engine",
		}},
		Ollama: &models.OllamaInfo{Listening: true, Models: []string{"mistral"}},
	}}
	got, err := PickServingNode(nodes, "mistral")
	if err != nil {
		t.Fatal(err)
	}
	if got.Already || got.Runtime != "ollama" {
		t.Fatalf("pick=%#v", got)
	}
}
