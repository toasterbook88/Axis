package modellife

import (
	"fmt"
	"sort"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// ServingPick is the one complete node whose facts already show a runtime for
// the named model. Already means the model is resident and must not be started
// again. Otherwise the runtime is an Ollama server that is already listening
// and lists the model.
type ServingPick struct {
	Node    string
	Runtime string
	Model   string
	Already bool
	warmth  float64
}

// Sentence is the operator line for a pick.
func (p ServingPick) Sentence() string {
	if p.Already {
		return fmt.Sprintf("picked %s: %s already serving %s", p.Node, p.Runtime, p.Model)
	}
	return fmt.Sprintf("picked %s: %s already listening", p.Node, p.Runtime)
}

// PickServingNode chooses one complete node that already has a runtime for model.
// A resident model outranks an Ollama server that only lists it. Equal resident
// warmth then breaks by node name. A node that only has a llama-server binary,
// or an Ollama server that does not list the model, is not a candidate.
func PickServingNode(nodes []models.NodeFacts, model string) (ServingPick, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return ServingPick{}, fmt.Errorf("model name is required")
	}
	var resident []ServingPick
	var listening []ServingPick
	for _, node := range nodes {
		if node.Status != models.StatusComplete || strings.TrimSpace(node.Name) == "" {
			continue
		}
		if pick, ok := residentServingPick(node, model); ok {
			resident = append(resident, pick)
			continue
		}
		if pick, ok := ollamaListeningPick(node, model); ok {
			listening = append(listening, pick)
		}
	}
	if len(resident) > 0 {
		sort.SliceStable(resident, func(i, j int) bool {
			if resident[i].warmth != resident[j].warmth {
				return resident[i].warmth > resident[j].warmth
			}
			return resident[i].Node < resident[j].Node
		})
		return resident[0], nil
	}
	if len(listening) > 0 {
		sort.SliceStable(listening, func(i, j int) bool {
			return listening[i].Node < listening[j].Node
		})
		return listening[0], nil
	}
	return ServingPick{}, fmt.Errorf("no complete node already has a runtime for %q", model)
}

func residentServingPick(node models.NodeFacts, model string) (ServingPick, bool) {
	var found ServingPick
	ok := false
	for _, res := range node.ResidentModels {
		if res.Down() || !knownServingRuntime(res.Runtime) || !servingNameMatches(res.Runtime, res.Name, model) {
			continue
		}
		if ok && res.WarmthScore <= found.warmth {
			continue
		}
		found = ServingPick{
			Node:    node.Name,
			Runtime: res.Runtime,
			Model:   model,
			Already: true,
			warmth:  res.WarmthScore,
		}
		ok = true
	}
	return found, ok
}

func ollamaListeningPick(node models.NodeFacts, model string) (ServingPick, bool) {
	if node.Ollama == nil || !node.Ollama.Listening {
		return ServingPick{}, false
	}
	for _, listed := range node.Ollama.Models {
		if ollamaNameMatches(listed, model) {
			return ServingPick{
				Node:    node.Name,
				Runtime: models.EngineOllama,
				Model:   model,
			}, true
		}
	}
	return ServingPick{}, false
}

func knownServingRuntime(runtime string) bool {
	switch runtime {
	case models.EngineOllama, models.EngineLlamaCpp, models.EngineMLX, "apple-foundation-models":
		return true
	default:
		return false
	}
}

func servingNameMatches(runtime, listed, requested string) bool {
	if runtime == models.EngineOllama {
		return ollamaNameMatches(listed, requested)
	}
	return strings.EqualFold(strings.TrimSpace(listed), strings.TrimSpace(requested))
}
