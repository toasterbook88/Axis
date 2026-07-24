package llmrouter

import (
	"strings"
)

// RoleFromTaskDescription maps free-text task intent to an inference role name.
// Returns "" when the description does not look inference-related.
//
// Explicit patterns win first:
//
//	role=default   role:fast   --role long
func RoleFromTaskDescription(desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return ""
	}
	lower := strings.ToLower(desc)

	// Explicit role= / role: / --role
	for _, prefix := range []string{"role=", "role:", "--role "} {
		if i := strings.Index(lower, prefix); i >= 0 {
			rest := strings.TrimSpace(desc[i+len(prefix):])
			// take first token
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				return strings.Trim(fields[0], `",'`)
			}
		}
	}

	// Specific quality / platform roles before generic inference defaults.
	if strings.Contains(lower, "long context") || strings.Contains(lower, "long-context") {
		return "long"
	}
	if strings.Contains(lower, "fast chat") || strings.Contains(lower, "cheap model") || strings.Contains(lower, "smol") {
		return "fast"
	}
	if strings.Contains(lower, "apple silicon") || strings.Contains(lower, "metal") ||
		strings.Contains(lower, "mlx ") || strings.HasPrefix(lower, "mlx ") || strings.Contains(lower, " mlx") {
		return "metal"
	}

	// Inference-ish tasks → default role
	inferenceHints := []string{
		"local-llm", "llm inference", "ollama", "llama.cpp", "mlx",
		"chat completion", "serve model", "run model", "inference backend",
		"generate with", "prompt the model",
	}
	for _, h := range inferenceHints {
		if strings.Contains(lower, h) {
			return "default"
		}
	}

	return ""
}

// FormatRouteReasoning turns a RoleRouteDecision into placement-style reasoning lines.
func FormatRouteReasoning(dec RoleRouteDecision) []string {
	var lines []string
	if dec.Role != "" {
		lines = append(lines, "inference_role="+dec.Role)
	}
	if dec.Backend != "" {
		lines = append(lines, "inference_backend="+dec.Backend)
	}
	if dec.Model != "" {
		lines = append(lines, "inference_model="+dec.Model)
	}
	if dec.Endpoint != "" {
		lines = append(lines, "inference_endpoint="+dec.Endpoint)
	}
	if dec.Kind != "" {
		lines = append(lines, "inference_kind="+dec.Kind)
	}
	if dec.Node != "" {
		lines = append(lines, "inference_node_hint="+dec.Node)
	}
	lines = append(lines, "inference_healthy="+boolStr(dec.Healthy))
	lines = append(lines, "inference_model_present="+boolStr(dec.ModelPresent))
	return lines
}

func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
