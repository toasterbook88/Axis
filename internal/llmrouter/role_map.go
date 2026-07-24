package llmrouter

import (
	"net"
	"net/url"
	"strings"
)

// RoleFromTaskDescription maps free-text task intent to an inference role name.
// Returns "" when the description does not look inference-related.
//
// Explicit patterns win first:
//
//	role=default   role:fast   --role long
//
// All slicing is performed on the lowercased string so multi-byte ToLower
// expansions cannot panic or mis-slice the original input.
func RoleFromTaskDescription(desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return ""
	}
	lower := strings.ToLower(desc)

	// Explicit role= / role: / --role — slice lower only (byte-safe after ToLower).
	for _, prefix := range []string{"role=", "role:", "--role "} {
		if i := strings.Index(lower, prefix); i >= 0 {
			rest := strings.TrimSpace(lower[i+len(prefix):])
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				return strings.Trim(fields[0], `",'`)
			}
		}
	}

	// Specific quality / platform roles before generic inference defaults.
	// Avoid bare "metal" (sheet metal, fabrication); require stronger signals.
	if strings.Contains(lower, "long context") || strings.Contains(lower, "long-context") {
		return "long"
	}
	if strings.Contains(lower, "fast chat") || strings.Contains(lower, "cheap model") || strings.Contains(lower, "smol") {
		return "fast"
	}
	if strings.Contains(lower, "apple silicon") || strings.Contains(lower, "mlx") {
		return "metal"
	}

	// Inference-ish tasks → default role
	inferenceHints := []string{
		"local-llm", "llm inference", "ollama", "llama.cpp",
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
// Raw endpoints are omitted (operators paste placement JSON into public issues);
// a coarse class is emitted instead.
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
		lines = append(lines, "inference_endpoint_class="+EndpointReachClass(dec.Endpoint))
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

// EndpointReachClass classifies a base URL for evidence/security policy.
// Returns "loopback", "private", or "remote".
func EndpointReachClass(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Hostname() == "" {
		return "remote"
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return "loopback"
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// Unresolved hostname: treat as remote (fail closed for evidence).
		return "remote"
	}
	if ip.IsLoopback() {
		return "loopback"
	}
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return "private"
	}
	return "remote"
}

// EndpointIsClusterLocal reports whether a base URL is loopback or private LAN.
// Used to set BackendLocal vs BackendRemote for evidence disclosure.
func EndpointIsClusterLocal(baseURL string) bool {
	c := EndpointReachClass(baseURL)
	return c == "loopback" || c == "private"
}

func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
