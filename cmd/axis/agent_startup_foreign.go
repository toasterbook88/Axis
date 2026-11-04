package main

import (
	"strings"

	"github.com/toasterbook88/axis/internal/agent"
)

// foreignOllama reports a usable Ollama choice that belongs to another node.
// An empty Node is this machine, and that choice stays eligible.
func foreignOllama(c ModelChoice) bool {
	return !c.Disabled && c.ProviderKind == "local" && c.Protocol == agent.ProtocolOllama && strings.TrimSpace(c.Node) != ""
}

// sameNodeOllama reports a usable Ollama choice on this machine.
func sameNodeOllama(c ModelChoice) bool {
	return !c.Disabled && c.Model != "" && c.ProviderKind == "local" &&
		c.Protocol == agent.ProtocolOllama && strings.TrimSpace(c.Node) == ""
}

// foreignOllamaBorrow reports that startup kept another node's Ollama even
// though the operator did not ask for that model by name or explicit pick.
func foreignOllamaBorrow(target ModelChoice, explicit *ModelChoice, requestedModel string) bool {
	if explicit != nil {
		return false
	}
	return foreignOllama(target) && !strings.EqualFold(target.Model, strings.TrimSpace(requestedModel))
}

// hubInsteadOfForeignOllama substitutes a usable same-node Ollama or an enabled
// hub role for another node's Ollama. A same-node Ollama pick always wins over
// the hub even when a foreign Ollama sorted earlier by model name. When several
// hub choices probed clean, the configured default role wins if it is one of them.
func hubInsteadOfForeignOllama(choices []ModelChoice, picked ModelChoice) (ModelChoice, bool) {
	if !foreignOllama(picked) {
		return ModelChoice{}, false
	}
	for _, c := range choices {
		if sameNodeOllama(c) {
			return c, true
		}
	}
	var hubs []ModelChoice
	for _, c := range choices {
		if c.Disabled || c.Model == "" || c.ProviderKind != "local" || c.Protocol != agent.ProtocolOpenAI {
			continue
		}
		if !strings.HasPrefix(c.ProviderName, "ai-backend:") {
			continue
		}
		hubs = append(hubs, c)
	}
	if len(hubs) == 0 {
		return ModelChoice{}, false
	}
	if role := strings.TrimSpace(modelFromAIRoleFn("default")); role != "" {
		for _, h := range hubs {
			if strings.EqualFold(h.Model, role) {
				return h, true
			}
		}
	}
	return hubs[0], true
}

// pickLocalStartup keeps a same-node catalog pick and refuses another node's
// Ollama when a same-node Ollama or an enabled hub role is usable.
func pickLocalStartup(choices []ModelChoice, t ModelChoice, optsFor func(ModelChoice) (agent.CloudBackendOptions, error)) (ModelChoice, agent.CloudBackendOptions, error) {
	if alt, ok := hubInsteadOfForeignOllama(choices, t); ok {
		t = alt
	}
	opts, err := optsFor(t)
	return t, opts, err
}
