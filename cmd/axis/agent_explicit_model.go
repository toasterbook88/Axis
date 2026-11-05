package main

import (
	"context"
	"strings"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

// appendExplicitAIModelChoice resolves an explicitly named model omitted from
// the role catalog. It does not change implicit defaults or configure a backend.
func appendExplicitAIModelChoice(ctx context.Context, requested string, rt *runtimectx.Context, choices []ModelChoice) []ModelChoice {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return choices
	}
	for _, choice := range choices {
		if !choice.Disabled && choice.ProviderKind == "local" && strings.EqualFold(choice.Model, requested) {
			return choices
		}
	}
	cfg, err := inferenceAILoadFn(inferenceAIPathFn())
	if err != nil || cfg == nil || len(cfg.Backends) == 0 {
		return choices
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, inferenceHintTimeout)
	defer cancel()
	var nodes *config.Config
	if rt != nil {
		nodes = rt.Config
	}
	for _, backend := range cfg.Backends {
		if ctx.Err() != nil {
			break
		}
		if !backend.IsEnabled() {
			continue
		}
		protocol := agent.ProtocolOpenAI
		switch backend.Kind {
		case config.AIBackendOllama:
			protocol = agent.ProtocolOllama
		case config.AIBackendOpenAICompatible, config.AIBackendLlamaCPP, config.AIBackendMLX:
		default:
			continue
		}
		endpoint := chooseAICatalogEndpoint(backend, nodes)
		if protocol == agent.ProtocolOllama {
			endpoint = strings.TrimSuffix(strings.TrimRight(endpoint, "/"), "/v1")
		}
		if endpoint == "" {
			continue
		}
		probeBackend := backend
		probeBackend.BaseURL, probeBackend.AdvertiseURL = endpoint, ""
		singleton := &config.AIConfig{Backends: []config.AIBackendConfig{probeBackend}}
		decision, err := inferenceResolveFn(ctx, singleton, llmrouter.ResolveRoleOptions{
			Model: requested, RequireModelListed: true, NodeConfig: nodes,
		})
		if err != nil || ctx.Err() != nil || !decision.Probed || !decision.Healthy || !decision.ModelPresent ||
			decision.Backend != backend.Name || decision.Model != requested || decision.Endpoint != endpoint || decision.Kind != backend.Kind {
			continue
		}
		present := false
		for _, probe := range decision.Probes {
			if probe.Backend != backend.Name || !probe.OK {
				continue
			}
			for _, id := range probe.Models {
				if id == requested || (protocol == agent.ProtocolOllama &&
					strings.LastIndex(requested, ":") <= strings.LastIndex(requested, "/") && id == requested+":latest") {
					present = true
				}
			}
		}
		if !present {
			continue
		}
		security := agent.BackendRemote
		if llmrouter.EndpointIsClusterLocal(endpoint) {
			security = agent.BackendLocal
		}
		return append(choices, ModelChoice{ID: "ai-model:" + backend.Name + ":" + requested,
			Model: requested, Protocol: protocol, ProviderName: "ai-backend:" + backend.Name,
			ProviderKind: "local", Node: decision.Node, Endpoint: endpoint, SecurityClass: security, Listed: true})
	}
	return choices
}
