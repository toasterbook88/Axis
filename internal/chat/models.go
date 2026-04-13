package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
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
	{Name: "qwen3:1.7b", Description: "best small local default for AXIS"},
	{Name: "qwen3:0.6b", Description: "lighter local fallback with tool-use support"},
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
	return models, nil
}

func choosePreferredModel(installed []string) (string, bool) {
	// Prefer recommended models in priority order.
	for _, candidate := range recommendedLocalModels {
		if slices.Contains(installed, candidate.Name) {
			return candidate.Name, true
		}
	}
	// Any installed model beats falling back to a hardcoded name that may not
	// be present. This handles clusters where the operator has pulled their own
	// preferred models (e.g. qwen3:4b, llama3.2:latest).
	if len(installed) > 0 {
		return installed[0], true
	}
	return "", false
}
