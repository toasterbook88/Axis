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

const defaultOllamaEndpoint = DefaultEndpoint

type ModelOption struct {
	Name        string
	Description string
	Cloud       bool
}

type ModelCatalog struct {
	Current            string
	Default            string
	InstalledAvailable bool
	Installed          []string
	RecommendedLocal   []ModelOption
	RecommendedCloud   []ModelOption
}

var recommendedLocalModels = []ModelOption{
	{Name: "qwen3.5:4b", Description: "best small local default for AXIS with tool-use"},
	{Name: "qwen3.5:9b", Description: "stronger local default with tool-use"},
	{Name: "llama3.1:8b", Description: "strong local alternative with tool-use"},
	{Name: "qwen3:1.7b", Description: "older qwen3 fallback with tool-use"},
	{Name: "qwen3:0.6b", Description: "lightweight qwen3 fallback with tool-use"},
	{Name: "qwen2.5-coder:1.5b", Description: "older coding-focused fallback"},
}

var recommendedCloudModels = []ModelOption{
	{Name: "qwen3-coder:480b-cloud", Description: "strongest coding/tool-use cloud option", Cloud: true},
	{Name: "gpt-oss:120b-cloud", Description: "strong general cloud model", Cloud: true},
	{Name: "glm-4.7:cloud", Description: "fast coding-oriented cloud option", Cloud: true},
	{Name: "kimi-k2.5:cloud", Description: "agentic cloud option", Cloud: true},
}

type ollamaTagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

func ResolveDefaultModel(ctx context.Context) string {
	installed, err := listInstalledModels(ctx, defaultOllamaEndpoint)
	if err == nil {
		if best, ok := choosePreferredModel(installed); ok {
			return best
		}
	}
	return recommendedLocalModels[0].Name
}

func BuildModelCatalog(ctx context.Context, current string) ModelCatalog {
	catalog := ModelCatalog{
		Current:          current,
		Default:          recommendedLocalModels[0].Name,
		RecommendedLocal: recommendedLocalModels,
		RecommendedCloud: recommendedCloudModels,
	}

	installed, err := listInstalledModels(ctx, defaultOllamaEndpoint)
	if err == nil {
		catalog.InstalledAvailable = true
		catalog.Installed = installed
		if best, ok := choosePreferredModel(installed); ok {
			catalog.Default = best
		}
	}
	return catalog
}

func FormatModelCatalog(catalog ModelCatalog) string {
	var b strings.Builder
	b.WriteString("AXIS Models\n")
	if catalog.Current != "" {
		fmt.Fprintf(&b, "Current: %s\n", catalog.Current)
	}
	if catalog.Default != "" {
		fmt.Fprintf(&b, "Auto-default: %s\n", catalog.Default)
	}
	b.WriteString("\nRecommended local:\n")
	for _, m := range catalog.RecommendedLocal {
		var labels []string
		if slices.Contains(catalog.Installed, m.Name) {
			labels = append(labels, "installed")
		}
		if m.Name == catalog.Default {
			labels = append(labels, "default")
		}
		label := ""
		if len(labels) > 0 {
			label = " [" + strings.Join(labels, ", ") + "]"
		}
		fmt.Fprintf(&b, "- %s%s: %s\n", m.Name, label, m.Description)
	}

	b.WriteString("\nCloud options (requires `ollama signin`):\n")
	for _, m := range catalog.RecommendedCloud {
		fmt.Fprintf(&b, "- %s: %s\n", m.Name, m.Description)
	}

	if catalog.InstalledAvailable {
		b.WriteString("\nInstalled local models:\n")
		if len(catalog.Installed) == 0 {
			b.WriteString("- none\n")
		} else {
			for _, name := range catalog.Installed {
				fmt.Fprintf(&b, "- %s\n", name)
			}
		}
	} else {
		b.WriteString("\nInstalled local models: unavailable (Ollama daemon not responding)\n")
	}

	b.WriteString("\nUse `/model <tag>` to switch models for this chat session.\n")
	return b.String()
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

	return fmt.Errorf("model %q is not available locally\navailable: %s\nre-run with --model %s or set chat.default_model in ~/.axis/nodes.yaml\nor pull it with: ollama pull %s",
		model, available, suggest, model)
}

// toolCapablePrefixes lists model families known to support Ollama /api/chat
// tool-calling. When no exact recommended model is installed, the fallback
// prefers models whose family prefix matches this list over models that do
// not (e.g. embedding-only or vision-only models).
var toolCapablePrefixes = []string{
	"llama3.1", "llama3.2", "llama3.3",
	"qwen3", "qwen3.5",
	"qwen2.5-coder", "qwen2.5",
	"mistral", "mixtral",
	"phi4", "phi3",
	"gemma3",
}

// nonToolFamilies blocks specific model families that share a prefix with a
// tool-capable family but do not support tool calling (e.g. gemma3n is an
// embedding/vision variant of gemma3).
var nonToolFamilies = []string{
	"gemma3n", // gemma3 supports tools; gemma3n does not
}

func choosePreferredModel(installed []string) (string, bool) {
	// Prefer recommended models in priority order.
	for _, candidate := range recommendedLocalModels {
		if slices.Contains(installed, candidate.Name) {
			return candidate.Name, true
		}
	}
	// Any installed model beats falling back to a hardcoded name that may not
	// be present. Prefer tool-capable families over non-tool families so that
	// the agent default doesn't select an embedding-only or vision-only model.
	if len(installed) > 0 {
		if best := pickToolCapable(installed); best != "" {
			return best, true
		}
		return installed[0], true
	}
	return "", false
}

// pickToolCapable returns the first installed model whose family prefix is
// in toolCapablePrefixes, or "" if none match. It explicitly skips families
// listed in nonToolFamilies to avoid false-positive matches on embedding or
// vision variants.
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

// formatToolCapableSuggestion builds a user-facing hint that lists up to three
// recommended tool-capable models. It avoids hardcoding model names in error
// messages and stays concise even as the recommended list grows.
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
