package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
)

// DefaultEndpoint is the local Ollama HTTP endpoint used when none is configured.
const DefaultEndpoint = "http://127.0.0.1:11434"

const defaultOllamaEndpoint = DefaultEndpoint

type modelOption struct {
	Name string
}

var recommendedLocalModels = []modelOption{
	{Name: "qwen3.5:4b"},
	{Name: "qwen3.5:9b"},
	{Name: "llama3.1:8b"},
	{Name: "qwen3:1.7b"},
	{Name: "qwen3:0.6b"},
	{Name: "qwen2.5-coder:1.5b"},
}

type ollamaTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

func ResolveDefaultModel(ctx context.Context) string {
	installed, err := listInstalledModels(ctx, defaultOllamaEndpoint)
	if err == nil {
		if best, ok := ChoosePreferredModel(installed); ok {
			return best
		}
	}
	return recommendedLocalModels[0].Name
}

func listInstalledModels(ctx context.Context, endpoint string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/tags", nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models status: %s", resp.Status)
	}

	var tags ollamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, err
	}

	models := make([]string, 0, len(tags.Models))
	for _, m := range tags.Models {
		if strings.TrimSpace(m.Name) != "" {
			models = append(models, m.Name)
		}
	}
	sort.Strings(models)
	return models, nil
}

func formatMissingModelError(model string, installed []string) error {
	if len(installed) == 0 {
		return fmt.Errorf("model %q is not available locally; run: ollama pull %s", model, model)
	}

	suggest := installed[0]
	available := strings.Join(installed, ", ")
	if len(installed) > 4 {
		available = strings.Join(installed[:4], ", ") + fmt.Sprintf(" (+%d more)", len(installed)-4)
	}

	return fmt.Errorf("model %q is not available locally\navailable: %s\nre-run with --model %s or set agent.default_model in ~/.axis/nodes.yaml\nor pull it with: ollama pull %s",
		model, available, suggest, model)
}

var toolCapablePrefixes = []string{
	"llama3.1", "llama3.2", "llama3.3",
	"qwen3", "qwen3.5",
	"qwen2.5-coder", "qwen2.5",
	"mistral", "mixtral",
	"phi4", "phi3",
	"gemma3",
}

var nonToolFamilies = []string{
	"gemma3n",
}

func ChoosePreferredModel(installed []string) (string, bool) {
	for _, candidate := range recommendedLocalModels {
		if slices.Contains(installed, candidate.Name) {
			return candidate.Name, true
		}
	}
	if len(installed) > 0 {
		if best := pickToolCapable(installed); best != "" {
			return best, true
		}
		return installed[0], true
	}
	return "", false
}

func pickToolCapable(installed []string) string {
	for _, name := range installed {
		base := name
		if idx := strings.LastIndex(name, ":"); idx >= 0 {
			base = name[:idx]
		}
		blocked := false
		for _, bad := range nonToolFamilies {
			if strings.HasPrefix(base, bad) {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		for _, prefix := range toolCapablePrefixes {
			if base == prefix || strings.HasPrefix(base, prefix+"-") {
				return name
			}
		}
	}
	return ""
}

func formatToolCapableSuggestion() string {
	var names []string
	for i, opt := range recommendedLocalModels {
		if i >= 3 {
			break
		}
		names = append(names, opt.Name)
	}
	if len(names) == 0 {
		return "try a tool-capable model"
	}
	if len(names) == 1 {
		return fmt.Sprintf("try %s", names[0])
	}
	last := names[len(names)-1]
	rest := strings.Join(names[:len(names)-1], ", ")
	return fmt.Sprintf("try a tool-capable model such as %s, or %s", rest, last)
}

// IsModelToolCapable reports whether model is known to support tool calling.
func IsModelToolCapable(model string) bool {
	if model == "" {
		return false
	}
	return pickToolCapable([]string{model}) != ""
}
