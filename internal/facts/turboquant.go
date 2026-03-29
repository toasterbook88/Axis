package facts

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"al.essio.dev/pkg/shellescape"
	"github.com/toasterbook88/axis/internal/models"
)

func inferTurboQuantSupport(osName, arch string, tools []models.ToolInfo, resources *models.Resources, ollama *models.OllamaInfo) *models.TurboQuantInfo {
	backendSet := map[string]struct{}{}
	capabilitySet := map[string]struct{}{}

	if _, ok := findToolInfo(tools, "mlx_lm"); ok && strings.EqualFold(osName, "darwin") && strings.Contains(strings.ToLower(arch), "arm") {
		backendSet["mlx"] = struct{}{}
		capabilitySet["long-context"] = struct{}{}
		capabilitySet["apple-silicon"] = struct{}{}
	}

	if _, ok := findToolInfo(tools, "llama-cli"); ok {
		backendSet["llama.cpp"] = struct{}{}
		capabilitySet["long-context"] = struct{}{}
		capabilitySet["llama-cli"] = struct{}{}
	}
	if _, ok := findToolInfo(tools, "llama-server"); ok {
		backendSet["llama.cpp"] = struct{}{}
		capabilitySet["long-context"] = struct{}{}
		capabilitySet["llama-server"] = struct{}{}
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
		Verified:     false,
		Backends:     backends,
		Capabilities: capabilities,
	}
}

type turboQuantRunner func(context.Context, string) (string, error)

func detectTurboQuantSupport(ctx context.Context, osName, arch string, tools []models.ToolInfo, resources *models.Resources, ollama *models.OllamaInfo, run turboQuantRunner) *models.TurboQuantInfo {
	info := inferTurboQuantSupport(osName, arch, tools, resources, ollama)
	if info == nil || run == nil {
		return info
	}

	verified, probeCaps := probeTurboQuantCapabilities(ctx, tools, run)
	if !verified && len(probeCaps) == 0 {
		return info
	}

	info.Verified = verified
	info.Capabilities = mergeCapabilities(info.Capabilities, probeCaps)
	return info
}

func findToolInfo(tools []models.ToolInfo, name string) (models.ToolInfo, bool) {
	for _, tool := range tools {
		if strings.EqualFold(tool.Name, name) {
			return tool, true
		}
	}
	return models.ToolInfo{}, false
}

func probeTurboQuantCapabilities(ctx context.Context, tools []models.ToolInfo, run turboQuantRunner) (bool, []string) {
	capabilitySet := map[string]struct{}{}
	verified := false

	if tool, ok := findToolInfo(tools, "mlx_lm"); ok {
		if out, ok := runTurboQuantProbe(ctx, tool, run); ok {
			if mlxProbeVerified(out) {
				verified = true
				capabilitySet["backend-probed"] = struct{}{}
			}
			for _, cap := range probeCapabilities(out, "mlx") {
				capabilitySet[cap] = struct{}{}
			}
		}
	}

	if tool, ok := findToolInfo(tools, "llama-cli"); ok {
		if out, ok := runTurboQuantProbe(ctx, tool, run); ok {
			if llamaProbeVerified(out) {
				verified = true
				capabilitySet["backend-probed"] = struct{}{}
			}
			for _, cap := range probeCapabilities(out, "llama.cpp") {
				capabilitySet[cap] = struct{}{}
			}
		}
	}

	if tool, ok := findToolInfo(tools, "llama-server"); ok {
		if out, ok := runTurboQuantProbe(ctx, tool, run); ok {
			if llamaProbeVerified(out) {
				verified = true
				capabilitySet["backend-probed"] = struct{}{}
			}
			for _, cap := range probeCapabilities(out, "llama.cpp") {
				capabilitySet[cap] = struct{}{}
			}
		}
	}

	caps := make([]string, 0, len(capabilitySet))
	for cap := range capabilitySet {
		caps = append(caps, cap)
	}
	sort.Strings(caps)
	return verified, caps
}

func runTurboQuantProbe(ctx context.Context, tool models.ToolInfo, run turboQuantRunner) (string, bool) {
	path := strings.TrimSpace(tool.Path)
	if path == "" {
		return "", false
	}
	out, err := run(ctx, fmt.Sprintf("%s --help 2>&1", shellescape.Quote(path)))
	if err != nil && strings.TrimSpace(out) == "" {
		return "", false
	}
	return out, true
}

func mlxProbeVerified(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "mlx") && (strings.Contains(lower, "generate") || strings.Contains(lower, "serve") || strings.Contains(lower, "chat"))
}

func llamaProbeVerified(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "llama") && (strings.Contains(lower, "ctx-size") || strings.Contains(lower, "n-gpu-layers") || strings.Contains(lower, "server") || strings.Contains(lower, "flash-attn"))
}

func probeCapabilities(out string, backend string) []string {
	lower := strings.ToLower(out)
	set := map[string]struct{}{}

	if strings.Contains(lower, "generate") || strings.Contains(lower, "prompt") {
		set["generate-mode"] = struct{}{}
	}
	if strings.Contains(lower, "serve") || strings.Contains(lower, "server") || strings.Contains(lower, "http") {
		set["server-mode"] = struct{}{}
	}
	if strings.Contains(lower, "ctx-size") || strings.Contains(lower, "context") || strings.Contains(lower, "context-window") {
		set["ctx-size-flag"] = struct{}{}
	}
	if strings.Contains(lower, "n-gpu-layers") || strings.Contains(lower, "gpu-layers") {
		set["gpu-layers-flag"] = struct{}{}
	}
	if strings.Contains(lower, "flash-attn") || strings.Contains(lower, "flash attention") {
		set["flash-attn-flag"] = struct{}{}
	}
	if strings.Contains(lower, "kv-cache") || strings.Contains(lower, "kv cache") {
		set["kv-cache-controls"] = struct{}{}
	}
	if backend == "mlx" {
		set["mlx-runtime"] = struct{}{}
	}
	if backend == "llama.cpp" {
		set["llama.cpp-runtime"] = struct{}{}
	}

	caps := make([]string, 0, len(set))
	for cap := range set {
		caps = append(caps, cap)
	}
	sort.Strings(caps)
	return caps
}

func mergeCapabilities(base []string, extra []string) []string {
	set := map[string]struct{}{}
	for _, cap := range base {
		if cap != "" {
			set[cap] = struct{}{}
		}
	}
	for _, cap := range extra {
		if cap != "" {
			set[cap] = struct{}{}
		}
	}
	merged := make([]string, 0, len(set))
	for cap := range set {
		merged = append(merged, cap)
	}
	sort.Strings(merged)
	return merged
}
