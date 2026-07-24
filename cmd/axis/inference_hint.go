package main

import (
	"context"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
	"github.com/toasterbook88/axis/internal/models"
)

// Test seams for AI config / resolve.
var (
	inferenceAIPathFn    = config.DefaultAIConfigPath
	inferenceAILoadFn    = config.LoadAIOrEmpty
	inferenceResolveFn   = llmrouter.ResolveRole
	inferenceRoleMapFn   = llmrouter.RoleFromTaskDescription
	modelFromAIRoleFn    = modelFromAIRole
	inferenceHintTimeout = 3 * time.Second
)

// appendInferenceRouteHints enriches a placement decision with advisory
// inference-route reasoning when ~/.axis/ai.yaml is present and the task
// maps to a role (or forceRole is set).
func appendInferenceRouteHints(decision *models.PlacementDecision, taskDesc, forceRole string) {
	if decision == nil {
		return
	}
	cfg, err := inferenceAILoadFn(inferenceAIPathFn())
	if err != nil || cfg == nil || len(cfg.Backends) == 0 {
		return
	}

	role := strings.TrimSpace(forceRole)
	if role == "" {
		role = inferenceRoleMapFn(taskDesc)
	}
	if role == "" {
		return
	}

	// Role must exist in config (case-insensitive handled by ResolveRole).
	ctx, cancel := context.WithTimeout(context.Background(), inferenceHintTimeout)
	defer cancel()
	// Prefer non-strict listing for placement hints so missing tags don't hide the node decision.
	dec, err := inferenceResolveFn(ctx, cfg, llmrouter.ResolveRoleOptions{
		Role:               role,
		RequireModelListed: false,
	})
	if err != nil {
		decision.Reasoning = append(decision.Reasoning,
			"inference_route: unavailable ("+err.Error()+")")
		return
	}
	decision.Reasoning = append(decision.Reasoning, llmrouter.FormatRouteReasoning(dec)...)
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
// Uses skip-probe resolution so startup stays fast; endpoints come from prefer order.
func modelChoicesFromAIConfig() []ModelChoice {
	cfg, err := inferenceAILoadFn(inferenceAIPathFn())
	if err != nil || cfg == nil || len(cfg.Roles) == 0 || len(cfg.Backends) == 0 {
		return nil
	}
	var out []ModelChoice
	ctx, cancel := context.WithTimeout(context.Background(), inferenceHintTimeout)
	defer cancel()
	for name := range cfg.Roles {
		dec, err := inferenceResolveFn(ctx, cfg, llmrouter.ResolveRoleOptions{
			Role:      name,
			SkipProbe: true,
		})
		if err != nil || dec.Model == "" || dec.Endpoint == "" {
			continue
		}
		proto := agent.ProtocolOpenAI
		if dec.Kind == config.AIBackendOllama {
			proto = agent.ProtocolOllama
		}
		out = append(out, ModelChoice{
			ID:            "ai-role:" + name,
			Model:         dec.Model,
			Protocol:      proto,
			ProviderName:  "ai-role",
			ProviderKind:  "local",
			Node:          dec.Node,
			Endpoint:      dec.Endpoint,
			SecurityClass: agent.BackendLocal,
		})
	}
	return out
}
