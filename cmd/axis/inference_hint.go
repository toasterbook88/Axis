package main

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/secrets"
)

// Test seams for AI config / resolve.
var (
	inferenceAIPathFn    = config.DefaultAIConfigPath
	inferenceAILoadFn    = config.LoadAIOrEmpty
	inferenceResolveFn   = llmrouter.ResolveRole
	inferenceRoleMapFn   = llmrouter.RoleFromTaskDescription
	modelFromAIRoleFn    = modelFromAIRole
	inferenceHintTimeout = 3 * time.Second
	// Reuse agent probe seam when available; tests may override.
	inferenceProbeFn = func(url string) bool {
		return probeEndpointFn(url)
	}
)

// appendInferenceRouteHints enriches a placement decision with advisory
// inference-route reasoning when ~/.axis/ai.yaml is present and the task
// maps to a configured role. Offline: SkipProbe true so placement stays
// cache-friendly and respects caller cancellation via ctx.
func appendInferenceRouteHints(ctx context.Context, decision *models.PlacementDecision, taskDesc string) {
	if decision == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, err := inferenceAILoadFn(inferenceAIPathFn())
	if err != nil || cfg == nil || len(cfg.Backends) == 0 {
		return
	}

	role := inferenceRoleMapFn(taskDesc)
	if role == "" {
		return
	}
	if !roleConfigured(cfg, role) {
		// Stay silent when the mapped role is not in the operator's ai.yaml.
		return
	}

	rctx, cancel := context.WithTimeout(ctx, inferenceHintTimeout)
	defer cancel()
	// SkipProbe: placement is otherwise an offline decision; do not add live HTTP.
	dec, err := inferenceResolveFn(rctx, cfg, llmrouter.ResolveRoleOptions{
		Role:               role,
		SkipProbe:          true,
		RequireModelListed: false,
	})
	if err != nil {
		// Configured role but unusable prefer list — still quiet to avoid noise.
		return
	}
	decision.Reasoning = append(decision.Reasoning, llmrouter.FormatRouteReasoning(dec)...)
}

func roleConfigured(cfg *config.AIConfig, role string) bool {
	if cfg == nil {
		return false
	}
	if _, ok := cfg.Roles[role]; ok {
		return true
	}
	for name := range cfg.Roles {
		if strings.EqualFold(name, role) {
			return true
		}
	}
	return false
}

// modelFromAIRole returns the model id for a configured role (config-only, no probe).
// Empty when ai.yaml missing or role unknown.
func modelFromAIRole(role string) string {
	role = strings.TrimSpace(role)
	if role == "" {
		return ""
	}
	cfg, err := inferenceAILoadFn(inferenceAIPathFn())
	if err != nil || cfg == nil {
		return ""
	}
	if r, ok := cfg.Roles[role]; ok {
		return strings.TrimSpace(r.Model)
	}
	for name, r := range cfg.Roles {
		if strings.EqualFold(name, role) {
			return strings.TrimSpace(r.Model)
		}
	}
	return ""
}

// modelChoicesFromAIConfig builds agent catalog entries from ai.yaml roles.
// Resolves prefer order offline, then probes endpoints and marks unreachable
// entries Disabled. Dedupes by (endpoint, model). Resolves API keys for hubs.
func modelChoicesFromAIConfig() []ModelChoice {
	cfg, err := inferenceAILoadFn(inferenceAIPathFn())
	if err != nil || cfg == nil || len(cfg.Roles) == 0 || len(cfg.Backends) == 0 {
		return nil
	}

	names := make([]string, 0, len(cfg.Roles))
	for name := range cfg.Roles {
		names = append(names, name)
	}
	sort.Strings(names)

	ctx, cancel := context.WithTimeout(context.Background(), inferenceHintTimeout)
	defer cancel()

	seen := map[string]bool{}
	var out []ModelChoice
	for _, name := range names {
		dec, err := inferenceResolveFn(ctx, cfg, llmrouter.ResolveRoleOptions{
			Role:      name,
			SkipProbe: true,
		})
		if err != nil || dec.Model == "" || dec.Endpoint == "" {
			continue
		}
		key := strings.ToLower(dec.Endpoint) + "\x00" + strings.ToLower(dec.Model)
		if seen[key] {
			continue
		}
		seen[key] = true

		proto := agent.ProtocolOpenAI
		if dec.Kind == config.AIBackendOllama {
			proto = agent.ProtocolOllama
		}

		sec := agent.BackendRemote
		if llmrouter.EndpointIsClusterLocal(dec.Endpoint) {
			sec = agent.BackendLocal
		}

		disabled := false
		reason := ""
		if !probeAIEndpoint(dec.Kind, dec.Endpoint) {
			disabled = true
			reason = "unreachable"
		}

		// ProviderName encodes backend for API key lookup at BuildBackend time.
		providerName := "ai-role"
		if dec.Backend != "" {
			providerName = "ai-backend:" + dec.Backend
		}

		out = append(out, ModelChoice{
			ID:             "ai-role:" + name,
			Model:          dec.Model,
			Protocol:       proto,
			ProviderName:   providerName,
			ProviderKind:   "local", // non-cloud protocol (matches remote ollama catalog entries)
			Node:           dec.Node,
			Endpoint:       dec.Endpoint,
			SecurityClass:  sec,
			Disabled:       disabled,
			DisabledReason: reason,
		})
	}
	return out
}

func probeAIEndpoint(kind, baseURL string) bool {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return false
	}
	var probeURL string
	switch kind {
	case config.AIBackendOllama:
		if strings.HasSuffix(base, "/v1") {
			base = strings.TrimSuffix(base, "/v1")
		}
		probeURL = base + "/api/tags"
	default:
		if strings.HasSuffix(base, "/v1") {
			probeURL = base + "/models"
		} else {
			probeURL = base + "/v1/models"
		}
	}
	return inferenceProbeFn(probeURL)
}

// apiKeyForAIBackend resolves API key material for an ai.yaml backend by name
// or by matching base_url to endpoint.
func apiKeyForAIBackend(providerName, endpoint string) string {
	cfg, err := inferenceAILoadFn(inferenceAIPathFn())
	if err != nil || cfg == nil {
		return ""
	}
	backendName := ""
	if strings.HasPrefix(providerName, "ai-backend:") {
		backendName = strings.TrimPrefix(providerName, "ai-backend:")
	}
	for _, b := range cfg.Backends {
		if backendName != "" && strings.EqualFold(b.Name, backendName) {
			return resolveBackendKey(b)
		}
		if endpoint != "" && urlsRoughlyEqual(b.BaseURL, endpoint) {
			return resolveBackendKey(b)
		}
	}
	return ""
}

func resolveBackendKey(b config.AIBackendConfig) string {
	key, err := secrets.ResolveOrEmpty(b.APIKeyEnv, b.APIKeyFile)
	if err != nil {
		return ""
	}
	return key
}

func urlsRoughlyEqual(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(strings.TrimSpace(a), "/"), strings.TrimRight(strings.TrimSpace(b), "/"))
}

// ensure probeEndpointFn signature available — defined in agent.go
var _ = http.StatusOK
