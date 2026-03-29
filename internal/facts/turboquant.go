package facts

import (
	"sort"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

func inferTurboQuantSupport(osName, arch string, tools []models.ToolInfo, resources *models.Resources, ollama *models.OllamaInfo) *models.TurboQuantInfo {
	backendSet := map[string]struct{}{}
	capabilitySet := map[string]struct{}{}
	verified := false

	if tool, ok := findToolInfo(tools, "mlx_lm"); ok && strings.EqualFold(osName, "darwin") && strings.Contains(strings.ToLower(arch), "arm") {
		backendSet["mlx"] = struct{}{}
		capabilitySet["long-context"] = struct{}{}
		capabilitySet["apple-silicon"] = struct{}{}
		if toolProbeVerified(tool) {
			verified = true
			capabilitySet["backend-probed"] = struct{}{}
		}
	}

	if tool, ok := findToolInfo(tools, "llama-cli"); ok {
		backendSet["llama.cpp"] = struct{}{}
		capabilitySet["long-context"] = struct{}{}
		capabilitySet["llama-cli"] = struct{}{}
		if toolProbeVerified(tool) {
			verified = true
			capabilitySet["backend-probed"] = struct{}{}
		}
	}
	if tool, ok := findToolInfo(tools, "llama-server"); ok {
		backendSet["llama.cpp"] = struct{}{}
		capabilitySet["long-context"] = struct{}{}
		capabilitySet["llama-server"] = struct{}{}
		if toolProbeVerified(tool) {
			verified = true
			capabilitySet["backend-probed"] = struct{}{}
		}
	}

	if len(backendSet) == 0 {
		return nil
	}

	if resources != nil && len(resources.GPUs) > 0 {
		capabilitySet["gpu-offload-candidate"] = struct{}{}
	} else {
		capabilitySet["cpu-fallback"] = struct{}{}
	}
	if ollama != nil && ollama.Installed {
		capabilitySet["ollama-present"] = struct{}{}
	}

	backends := make([]string, 0, len(backendSet))
	for backend := range backendSet {
		backends = append(backends, backend)
	}
	sort.Strings(backends)

	capabilities := make([]string, 0, len(capabilitySet))
	for capability := range capabilitySet {
		capabilities = append(capabilities, capability)
	}
	sort.Strings(capabilities)

	return &models.TurboQuantInfo{
		Supported:    true,
		Verified:     verified,
		Backends:     backends,
		Capabilities: capabilities,
	}
}

func findToolInfo(tools []models.ToolInfo, name string) (models.ToolInfo, bool) {
	for _, tool := range tools {
		if strings.EqualFold(tool.Name, name) {
			return tool, true
		}
	}
	return models.ToolInfo{}, false
}

func toolProbeVerified(tool models.ToolInfo) bool {
	if strings.TrimSpace(tool.Path) == "" {
		return false
	}
	return strings.TrimSpace(tool.Version) != ""
}
