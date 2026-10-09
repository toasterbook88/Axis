package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/modelinventory"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/secrets"
)

// collectModelChoices lists the models the agent can switch to: the
// snapshot's model catalog, enabled cloud providers, and ai.yaml roles.
//
// Catalog entries come from each node's own report, so the list is the same
// whichever node builds it. Remote entries are not probed here; probing from
// the caller's vantage is what made the list disagree across nodes. Only
// local llama.cpp/MLX residents get a localhost check, so a stale port on
// this machine is not offered as selectable.
func collectModelChoices(rt *runtimectx.Context) []ModelChoice {
	if rt == nil {
		return nil
	}
	choices := catalogModelChoices(rt.Snapshot)
	choices = append(choices, cloudProviderChoices(rt.Config)...)
	choices = append(choices, modelChoicesFromAIConfig(rt.Config)...)
	localizeChoiceNodes(choices, rt.Snapshot)

	sort.Slice(choices, func(i, j int) bool {
		if choices[i].ProviderKind != choices[j].ProviderKind {
			return choices[i].ProviderKind < choices[j].ProviderKind
		}
		if choices[i].ProviderName != choices[j].ProviderName {
			return choices[i].ProviderName < choices[j].ProviderName
		}
		return choices[i].Model < choices[j].Model
	})
	return choices
}

func catalogModelChoices(snap *models.ClusterSnapshot) []ModelChoice {
	if snap == nil {
		return nil
	}
	nodes := make(map[string]models.NodeFacts, len(snap.Nodes))
	for _, n := range snap.Nodes {
		nodes[n.Name] = n
	}

	var choices []ModelChoice
	for _, e := range modelinventory.Catalog(snap, "").Entries {
		if embeddingOnly(e.Capabilities) {
			continue
		}
		n := nodes[e.Node]
		local := models.IsLocalNode(n)
		choice := ModelChoice{
			ID:            e.Node + ":" + e.Engine + ":" + e.Model,
			Model:         e.Model,
			Protocol:      agent.ProtocolOpenAI,
			ProviderName:  e.Engine,
			ProviderKind:  "local",
			Node:          e.Node,
			SecurityClass: agent.BackendRemote,
			Loaded:        e.State == models.ModelCatalogLoaded,
			CloudProxy:    e.Locality == models.ModelLocalityCloudProxy,
			Capabilities:  append([]string(nil), e.Capabilities...),
			Port:          e.Port,
		}
		if e.Engine == "ollama" {
			choice.Protocol = agent.ProtocolOllama
			if n.Ollama != nil {
				choice.Port = n.Ollama.Port
			}
		}
		if local && !choice.CloudProxy {
			choice.SecurityClass = agent.BackendLocal
		}
		if endpoint, err := resolveNodeEndpoint(n, choice.Port); err == nil {
			choice.Endpoint = endpoint
		} else {
			choice.Disabled, choice.DisabledReason = true, "no valid endpoint"
		}
		if e.Engine != "ollama" && choice.Port <= 0 {
			choice.Disabled, choice.DisabledReason = true, "no valid endpoint"
		}
		choices = append(choices, choice)
	}
	probeLocalResidentChoices(choices, nodes)
	return choices
}

// modelChoiceDetail is the one description every model picker shows: where
// the model runs, its engine, whether it is loaded, and whether requests
// leave the cluster. The reason is appended when the choice is disabled.
func modelChoiceDetail(c ModelChoice) string {
	var parts []string
	switch {
	case c.ProviderKind == "cloud":
		parts = append(parts, "cloud provider "+c.ProviderName)
	case c.Node == "":
		parts = append(parts, "this node", c.ProviderName)
	default:
		parts = append(parts, "node "+c.Node, c.ProviderName)
	}
	if c.Loaded {
		parts = append(parts, "loaded")
	}
	if c.CloudProxy {
		parts = append(parts, "cloud proxy, leaves the cluster")
	}
	detail := strings.Join(parts, " · ")
	if c.Disabled && c.DisabledReason != "" {
		detail += " (" + c.DisabledReason + ")"
	}
	return detail
}

// embeddingOnly is true only when the engine reported capabilities and none
// of them is text completion. Unknown capabilities are not excluded.
func embeddingOnly(capabilities []string) bool {
	return len(capabilities) > 0 && slices.Contains(capabilities, "embedding") && !slices.Contains(capabilities, "completion")
}

// probeLocalResidentChoices disables local llama.cpp/MLX choices whose port
// does not answer on localhost. Probes run concurrently, one per endpoint.
func probeLocalResidentChoices(choices []ModelChoice, nodes map[string]models.NodeFacts) {
	targets := map[string][]int{}
	for i, c := range choices {
		if c.Disabled || c.Protocol != agent.ProtocolOpenAI || !models.IsLocalNode(nodes[c.Node]) {
			continue
		}
		url := c.Endpoint + "/v1/models"
		targets[url] = append(targets[url], i)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for url, idxs := range targets {
		wg.Add(1)
		go func(url string, idxs []int) {
			defer wg.Done()
			if probeEndpointFn(url) {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, i := range idxs {
				choices[i].Disabled, choices[i].DisabledReason = true, "unreachable"
			}
		}(url, idxs)
	}
	wg.Wait()
}

// localizeChoiceNodes clears Node for choices on this machine, matching the
// ModelTarget contract (empty Node = local), so the picker never labels the
// local node "remote".
func localizeChoiceNodes(choices []ModelChoice, snap *models.ClusterSnapshot) {
	if snap == nil {
		return
	}
	local := map[string]bool{}
	for _, n := range snap.Nodes {
		if models.IsLocalNode(n) {
			local[n.Name] = true
		}
	}
	for i := range choices {
		if local[choices[i].Node] {
			choices[i].Node = ""
		}
	}
}

func cloudProviderChoices(cfg *config.Config) []ModelChoice {
	if cfg == nil {
		return nil
	}
	var choices []ModelChoice
	for pName, pCfg := range cfg.AIProviders {
		if !pCfg.Enabled || !strings.EqualFold(pCfg.Type, "cloud") {
			continue
		}
		key, keyErr := secrets.ResolveOrEmpty(pCfg.APIKeyEnv, pCfg.APIKeyFile)
		disabled := keyErr != nil || key == ""
		reason := ""
		if disabled {
			reason = "API key not found"
		}
		for _, m := range pCfg.Models {
			if m.Name == "" {
				continue
			}
			choices = append(choices, ModelChoice{
				ID:             fmt.Sprintf("cloud:%s:%s", pName, m.Name),
				Model:          m.Name,
				Protocol:       agent.ProtocolCloud,
				ProviderName:   pName,
				ProviderKind:   "cloud",
				Endpoint:       pCfg.Endpoint,
				SecurityClass:  agent.BackendRemote,
				Disabled:       disabled,
				DisabledReason: reason,
			})
		}
	}
	return choices
}
