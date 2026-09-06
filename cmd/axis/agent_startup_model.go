package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/secrets"
	"github.com/toasterbook88/axis/internal/ui"
)

// ModelChoice is an alias for agent.ModelTarget (canonical model identity).
type ModelChoice = agent.ModelTarget

// agentStartupBackendParams carries inputs needed to select the model and configure the backend.
type agentStartupBackendParams struct {
	Model                 string
	StartupRequestedModel string
	Provider              string
	CloudModel            string
	CheapModel            string
	SelectModel           bool
	Verbose               bool
	RT                    *runtimectx.Context
	Ctx                   context.Context
	In                    *os.File
	Out                   io.Writer
	ErrOut                io.Writer
}

// agentStartupBackendResult returns the resolved active target, backend, and choices catalog.
type agentStartupBackendResult struct {
	ActiveTarget ModelChoice
	Backend      agent.ChatBackend
	Choices      []ModelChoice
}

// setupAgentStartupBackend handles interactive model selection (if requested or needed),
// target resolution, backend instantiation, and optional cheap-model multi-routing.
func setupAgentStartupBackend(p agentStartupBackendParams) (agentStartupBackendResult, error) {
	hasDefaultModel := false
	if p.RT != nil && p.RT.Config != nil && p.RT.Config.AgentDefaultModel() != "" {
		hasDefaultModel = true
	}

	choices := collectModelChoices(p.RT)
	var explicitTarget *ModelChoice
	provider := p.Provider
	cloudModel := p.CloudModel

	isTerm := p.In != nil && term.IsTerminal(int(p.In.Fd()))
	if p.SelectModel || (p.Model == "" && !hasDefaultModel && isTerm) {
		if len(choices) == 0 {
			return agentStartupBackendResult{}, ExitCodeError{
				Code:    ExitErrConfigLoad,
				Message: "no models found (neither local Ollama models nor enabled cloud providers)",
			}
		}
		var selectOptions []ui.SelectOption
		for _, choice := range choices {
			detail := fmt.Sprintf("%s - %s", choice.ProviderName, choice.ProviderKind)
			if choice.ProviderKind == "local" {
				if choice.Node != "" {
					detail = fmt.Sprintf("Remote node %s (%s) [%s]", choice.Node, choice.Endpoint, choice.Protocol)
				} else {
					detail = fmt.Sprintf("Local (%s) [%s]", choice.Endpoint, choice.Protocol)
				}
			}
			disabled := choice.Disabled
			if choice.DisabledReason != "" && disabled {
				detail += " (" + choice.DisabledReason + ")"
			}
			selectOptions = append(selectOptions, ui.SelectOption{
				ID:       choice.ID,
				Label:    choice.Model,
				Detail:   detail,
				Disabled: disabled,
			})
		}

		sel := &REPLSelector{
			terminal: ui.NewStdTerminal(p.In, p.Out),
			in:       &UnbufferedLineReader{reader: p.In},
			out:      p.Out,
		}
		res, err := sel.Select(p.Ctx, "Select model to use for the AXIS Agent session:", selectOptions)
		if err != nil {
			return agentStartupBackendResult{}, fmt.Errorf("select model: %w", err)
		}
		if !res.Selected {
			return agentStartupBackendResult{}, fmt.Errorf("model selection aborted")
		}

		var chosen ModelChoice
		for _, c := range choices {
			if c.ID == res.ID {
				chosen = c
				break
			}
		}
		if chosen.Model == "" {
			return agentStartupBackendResult{}, fmt.Errorf("selected model id %q not found", res.ID)
		}
		explicitTarget = &chosen
		// Interactive local/remote selection forces local provider mode for auto policy.
		if chosen.ProviderKind != "cloud" && strings.EqualFold(provider, "auto") {
			provider = "local"
		}
		if chosen.ProviderKind == "cloud" {
			provider = "cloud"
			if cloudModel == "" {
				cloudModel = chosen.Model
			}
		}
	}

	// Pass the effective requested model (flag / default_model / preferred), not the raw flag alone.
	activeTarget, cloudOpts, err := resolveStartupModelTarget(p.StartupRequestedModel, provider, cloudModel, explicitTarget, p.RT, choices)
	if err != nil {
		return agentStartupBackendResult{}, ExitCodeError{Code: ExitErrConfigLoad, Message: err.Error()}
	}

	backend, err := agent.BuildBackend(activeTarget, cloudOpts)
	if err != nil {
		return agentStartupBackendResult{}, ExitCodeError{Code: ExitErrConfigLoad, Message: err.Error()}
	}

	// Multi-model routing: cheap model on the same cloud provider only.
	if p.CheapModel != "" && activeTarget.Protocol == agent.ProtocolCloud {
		cheapTarget, cheapOpts, cerr := resolveCheapCloudTarget(p.RT, activeTarget, p.CheapModel)
		if cerr == nil {
			cheapBackend, berr := agent.BuildBackend(cheapTarget, cheapOpts)
			if berr == nil {
				rb := agent.NewRoutingBackend(backend, cheapBackend, nil)
				if p.Verbose {
					rb.SetVerbose(p.ErrOut)
					fmt.Fprintf(p.ErrOut, "Multi-model routing: primary=%q cheap=%q\n", activeTarget.Model, cheapTarget.Model)
				}
				backend = rb
			} else if p.Verbose {
				fmt.Fprintf(p.ErrOut, "Warning: cheap-model %q not available: %v\n", p.CheapModel, berr)
			}
		} else if p.Verbose {
			fmt.Fprintf(p.ErrOut, "Warning: cheap-model %q not available: %v\n", p.CheapModel, cerr)
		}
	}

	return agentStartupBackendResult{
		ActiveTarget: activeTarget,
		Backend:      backend,
		Choices:      choices,
	}, nil
}

// resolveNodeEndpoint determines the reachable HTTP endpoint for a given node
// and port. If port is 0, it defaults to 11434. It prioritizes the explicitly
// configured SSHTarget (since that is the known dial route), followed by
// private LAN addresses, over other overlay/docker interfaces.
func resolveNodeEndpoint(n models.NodeFacts, port int) (string, error) {
	if port <= 0 {
		port = 11434
	}

	if models.IsLocalNode(n) {
		return fmt.Sprintf("http://localhost:%d", port), nil
	}

	var targetHost string

	// 1. Prefer the configured dial target if it's a parseable IP.
	if n.SSHTarget != "" {
		if _, err := netip.ParseAddr(n.SSHTarget); err == nil {
			targetHost = n.SSHTarget
		}
	}

	// 2. Fallback to searching addresses, preferring private LAN.
	if targetHost == "" {
		for _, addr := range n.Addresses {
			if addr.Scope != "link-local" && addr.Address != "" {
				if ip, err := netip.ParseAddr(addr.Address); err == nil {
					if isPrivateLAN(ip) {
						targetHost = addr.Address
						break
					}
					// keep first valid address if no LAN found
					if targetHost == "" {
						targetHost = addr.Address
					}
				}
			}
		}
	}

	// 3. Absolute fallback to hostname (machine name).
	if targetHost == "" {
		targetHost = n.Hostname
	}

	if targetHost == "" {
		return "", fmt.Errorf("remote node %s has no valid network address or hostname", n.Name)
	}

	return fmt.Sprintf("http://%s:%d", targetHost, port), nil
}

func isPrivateLAN(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return true
	}
	if ip.Is4() {
		return ip.IsPrivate()
	}
	// IPv6 ULA (fc00::/7)
	b := ip.As16()
	return b[0]&0xfe == 0xfc
}

func switchAgentToModelChoice(session *agentREPLSession, choice ModelChoice) error {
	opts, err := cloudOptsForTarget(session.Runtime, &choice)
	if err != nil {
		return err
	}
	backend, err := agent.BuildBackend(choice, opts)
	if err != nil {
		return err
	}
	session.Agent.SetBackend(backend, choice.SecurityClass)
	session.Agent.SetModel(choice.Model)
	// Refresh guarded runners so OwnerLabel provenance tracks the live model
	// after /model (Layer 4 contract — not the startup-captured name).
	session.Agent.SetRunShell(guardedAgentShellRunner(choice.Model))
	session.Agent.SetRunOnNode(func(ctx context.Context, node, command string) (string, error) {
		return guardedAgentCommandRunner(choice.Model, node)(ctx, command)
	})
	session.ActiveTarget = choice
	errW := session.ErrOut
	switch choice.Protocol {
	case agent.ProtocolCloud:
		fmt.Fprintf(errW, "Switched to cloud provider %q, model %q\n", choice.ProviderName, choice.Model)
	case agent.ProtocolOpenAI:
		if choice.Node != "" {
			fmt.Fprintf(errW, "Switched to %s model %q on node %q (%s)\n", choice.ProviderName, choice.Model, choice.Node, choice.Endpoint)
		} else {
			fmt.Fprintf(errW, "Switched to %s model %q (%s)\n", choice.ProviderName, choice.Model, choice.Endpoint)
		}
	default:
		if choice.Node != "" {
			fmt.Fprintf(errW, "Switched to model %q on remote node %q (%s)\n", choice.Model, choice.Node, choice.Endpoint)
		} else {
			fmt.Fprintf(errW, "Switched to local Ollama model %q (%s)\n", choice.Model, choice.Endpoint)
		}
	}
	return nil
}

// cloudOptsForTarget resolves cloud credentials when target.Protocol is cloud.
// For any ProtocolOpenAI target it also tries ai.yaml backend keys (by
// ai-backend: name, then by matching base_url). That intentionally covers both
// ai-role catalog entries and nodes.yaml resident OpenAI endpoints that share
// a hub URL with a keyed ai.yaml backend.
// target is a pointer so provider-name casing normalization is retained by callers.
func cloudOptsForTarget(loadRT func(context.Context) (*runtimectx.Context, error), target *ModelChoice) (agent.CloudBackendOptions, error) {
	var opts agent.CloudBackendOptions
	if target == nil {
		return opts, fmt.Errorf("cloud target is nil")
	}
	if target.Protocol == agent.ProtocolOpenAI {
		if key := apiKeyForAIBackend(target.ProviderName, target.Endpoint); key != "" {
			opts.APIKey = key
		}
		return opts, nil
	}
	if target.Protocol != agent.ProtocolCloud {
		return opts, nil
	}
	if loadRT == nil {
		return opts, fmt.Errorf("runtime loader required for cloud model %q", target.Model)
	}
	rt, err := loadRT(context.Background())
	if err != nil {
		return opts, fmt.Errorf("load runtime for cloud provider: %w", err)
	}
	if rt == nil || rt.Config == nil {
		return opts, fmt.Errorf("cloud provider %q not configured", target.ProviderName)
	}
	pCfg, ok := rt.Config.AIProviders[target.ProviderName]
	if !ok {
		for name, p := range rt.Config.AIProviders {
			if strings.EqualFold(name, target.ProviderName) {
				pCfg = p
				ok = true
				target.ProviderName = name
				break
			}
		}
	}
	if !ok {
		return opts, fmt.Errorf("cloud provider %q not configured", target.ProviderName)
	}
	key, err := secrets.ResolveOrEmpty(pCfg.APIKeyEnv, pCfg.APIKeyFile)
	if err != nil || key == "" {
		return opts, fmt.Errorf("API key for cloud provider %q not found or empty", target.ProviderName)
	}
	opts.APIKey = key
	opts.ProviderKind = pCfg.Kind
	if m, found := findModelInProvider(pCfg, target.Model); found {
		opts.CostPer1K = m.CostPer1K
		// Prefer the canonical configured name for telemetry consistency.
		target.Model = m.Name
	}
	return opts, nil
}

// findModelInProvider matches a model name or alias within a provider config.
func findModelInProvider(pCfg config.AIProviderConfig, name string) (config.AIModelConfig, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return config.AIModelConfig{}, false
	}
	for _, m := range pCfg.Models {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
		for _, alias := range m.Aliases {
			if strings.EqualFold(alias, name) {
				return m, true
			}
		}
	}
	return config.AIModelConfig{}, false
}

// credentialedCloudProvider is an enabled cloud provider with a resolvable API key.
type credentialedCloudProvider struct {
	name string
	cfg  config.AIProviderConfig
	key  string
}

// listCredentialedCloudProviders returns enabled cloud providers that have a
// non-empty API key, ordered by Priority descending then provider name ascending.
func listCredentialedCloudProviders(rt *runtimectx.Context) []credentialedCloudProvider {
	if rt == nil || rt.Config == nil {
		return nil
	}
	var out []credentialedCloudProvider
	for pName, pCfg := range rt.Config.AIProviders {
		if !pCfg.Enabled || !strings.EqualFold(pCfg.Type, "cloud") {
			continue
		}
		key, err := secrets.ResolveOrEmpty(pCfg.APIKeyEnv, pCfg.APIKeyFile)
		if err != nil || key == "" {
			continue
		}
		out = append(out, credentialedCloudProvider{name: pName, cfg: pCfg, key: key})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].cfg.Priority != out[j].cfg.Priority {
			return out[i].cfg.Priority > out[j].cfg.Priority
		}
		return out[i].name < out[j].name
	})
	return out
}

// cheapestModelInProvider picks the lowest positive CostPer1K model; if none
// have a positive cost, the first named model is used.
func cheapestModelInProvider(pCfg config.AIProviderConfig) (config.AIModelConfig, bool) {
	var best config.AIModelConfig
	var found bool
	bestCost := 0.0
	for _, m := range pCfg.Models {
		if strings.TrimSpace(m.Name) == "" {
			continue
		}
		if !found {
			best = m
			found = true
			if m.CostPer1K > 0 {
				bestCost = m.CostPer1K
			}
			continue
		}
		if m.CostPer1K > 0 && (bestCost == 0 || m.CostPer1K < bestCost) {
			best = m
			bestCost = m.CostPer1K
		}
	}
	return best, found
}

func cloudChoiceFromProvider(p credentialedCloudProvider, m config.AIModelConfig) ModelChoice {
	return ModelChoice{
		ID:            fmt.Sprintf("cloud:%s:%s", p.name, m.Name),
		Model:         m.Name,
		Protocol:      agent.ProtocolCloud,
		ProviderName:  p.name,
		ProviderKind:  "cloud",
		Endpoint:      p.cfg.Endpoint,
		SecurityClass: agent.BackendRemote,
	}
}

func cloudOptsFromProvider(p credentialedCloudProvider, m config.AIModelConfig) agent.CloudBackendOptions {
	return agent.CloudBackendOptions{
		ProviderKind: p.cfg.Kind,
		APIKey:       p.key,
		CostPer1K:    m.CostPer1K,
	}
}

// listValidCloudModelChoices returns provider-qualified model ids for error messages.
func listValidCloudModelChoices(providers []credentialedCloudProvider) []string {
	var ids []string
	for _, p := range providers {
		for _, m := range p.cfg.Models {
			if strings.TrimSpace(m.Name) == "" {
				continue
			}
			ids = append(ids, fmt.Sprintf("%s:%s", p.name, m.Name))
		}
	}
	sort.Strings(ids)
	return ids
}

// resolveCloudStartupTarget selects a cloud ModelTarget using credential-valid
// providers ordered by Priority. When requestedModel is non-empty it must match
// a configured model (name or alias) or an error is returned.
func resolveCloudStartupTarget(rt *runtimectx.Context, requestedModel string) (ModelChoice, agent.CloudBackendOptions, error) {
	providers := listCredentialedCloudProviders(rt)
	if len(providers) == 0 {
		return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf("no enabled cloud providers with valid API keys found in config")
	}
	req := strings.TrimSpace(requestedModel)
	if req != "" {
		// Support "provider:model" qualified form as well as bare model name.
		if prov, model, ok := splitProviderModel(req); ok {
			for _, p := range providers {
				if !strings.EqualFold(p.name, prov) {
					continue
				}
				if m, found := findModelInProvider(p.cfg, model); found {
					return cloudChoiceFromProvider(p, m), cloudOptsFromProvider(p, m), nil
				}
			}
		}
		for _, p := range providers {
			if m, found := findModelInProvider(p.cfg, req); found {
				return cloudChoiceFromProvider(p, m), cloudOptsFromProvider(p, m), nil
			}
		}
		valid := listValidCloudModelChoices(providers)
		if len(valid) == 0 {
			return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf("cloud model %q not found; no models configured on credentialed providers", req)
		}
		return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf(
			"cloud model %q not found among credentialed providers; valid choices: %s",
			req, strings.Join(valid, ", "))
	}

	// No explicit model: highest-priority provider, then cheapest model on that provider.
	p := providers[0]
	m, ok := cheapestModelInProvider(p.cfg)
	if !ok {
		return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf("no models configured for cloud provider %q", p.name)
	}
	return cloudChoiceFromProvider(p, m), cloudOptsFromProvider(p, m), nil
}

// splitProviderModel parses "provider:model" (exactly one colon separating non-empty parts).
func splitProviderModel(ref string) (provider, model string, ok bool) {
	ref = strings.TrimSpace(ref)
	i := strings.Index(ref, ":")
	if i <= 0 || i >= len(ref)-1 {
		return "", "", false
	}
	// Avoid treating model tags like "llama3.2:latest" as provider-qualified when
	// the left side is not a known provider — callers try provider match first,
	// then bare-name match. Here we only split; matchers verify provider exists.
	return ref[:i], ref[i+1:], true
}

// resolveCheapCloudTarget builds a cheap-routing target on the same provider as
// primary, re-resolving cost from config. The cheap model must be configured on
// that provider (name or alias).
func resolveCheapCloudTarget(rt *runtimectx.Context, primary ModelChoice, cheapModel string) (ModelChoice, agent.CloudBackendOptions, error) {
	cheapModel = strings.TrimSpace(cheapModel)
	if cheapModel == "" {
		return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf("cheap model name is empty")
	}
	if primary.Protocol != agent.ProtocolCloud || primary.ProviderName == "" {
		return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf("cheap-model requires an active cloud provider")
	}
	if rt == nil || rt.Config == nil {
		return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf("runtime config required for cheap-model")
	}
	pCfg, ok := rt.Config.AIProviders[primary.ProviderName]
	if !ok {
		for name, p := range rt.Config.AIProviders {
			if strings.EqualFold(name, primary.ProviderName) {
				pCfg = p
				ok = true
				primary.ProviderName = name
				break
			}
		}
	}
	if !ok {
		return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf("cloud provider %q not configured", primary.ProviderName)
	}
	m, found := findModelInProvider(pCfg, cheapModel)
	if !found {
		return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf(
			"cheap-model %q is not configured on provider %q", cheapModel, primary.ProviderName)
	}
	key, err := secrets.ResolveOrEmpty(pCfg.APIKeyEnv, pCfg.APIKeyFile)
	if err != nil || key == "" {
		return ModelChoice{}, agent.CloudBackendOptions{}, fmt.Errorf("API key for cloud provider %q not found or empty", primary.ProviderName)
	}
	target := ModelChoice{
		ID:            fmt.Sprintf("cloud:%s:%s", primary.ProviderName, m.Name),
		Model:         m.Name,
		Protocol:      agent.ProtocolCloud,
		ProviderName:  primary.ProviderName,
		ProviderKind:  "cloud",
		Endpoint:      pCfg.Endpoint,
		SecurityClass: agent.BackendRemote,
	}
	opts := agent.CloudBackendOptions{
		ProviderKind: pCfg.Kind,
		APIKey:       key,
		CostPer1K:    m.CostPer1K,
	}
	return target, opts, nil
}

// effectiveStartupRequestedModel returns the model name that should drive startup
// selection when --model is empty: agent.default_model (or deprecated chat.default_model),
// warm-resident preferred, then ai.yaml role "default" when configured.
// Returns "" when none are available so the catalog can pick first usable local.
func effectiveStartupRequestedModel(flag string, rt *runtimectx.Context) string {
	if s := strings.TrimSpace(flag); s != "" {
		return s
	}
	if rt != nil && rt.Config != nil {
		if s := rt.Config.AgentDefaultModel(); s != "" {
			return s
		}
	} else {
		if cfg, err := config.Load(config.DefaultConfigPath()); err == nil {
			if s := cfg.AgentDefaultModel(); s != "" {
				return s
			}
		}
	}
	if rt != nil && rt.Snapshot != nil {
		var allInstalled []string
		for _, node := range rt.Snapshot.Nodes {
			for _, m := range node.ResidentModels {
				allInstalled = append(allInstalled, m.Name)
			}
		}
		if best, ok := chat.ChoosePreferredModel(allInstalled); ok {
			return best
		}
	}
	// Operator inference roles (~/.axis/ai.yaml) after chat/warm defaults.
	if s := modelFromAIRoleFn("default"); s != "" {
		return s
	}
	return ""
}

func resolveAgentModel(flag string, rt *runtimectx.Context) string {
	if m := effectiveStartupRequestedModel(flag, rt); m != "" {
		return m
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	return chat.ResolveDefaultModel(ctx)
}

func syntheticLocalOllamaTarget(name string) ModelChoice {
	return ModelChoice{
		ID:            "local:ollama:" + name,
		Model:         name,
		Protocol:      agent.ProtocolOllama,
		ProviderName:  "ollama",
		ProviderKind:  "local",
		Endpoint:      chat.DefaultEndpoint,
		SecurityClass: agent.BackendLocal,
	}
}

// findModelTargetByRef resolves /model <ref> against the catalog.
// Prefer exact ID, then unique model name among non-disabled choices.
func findModelTargetByRef(choices []ModelChoice, ref string) (ModelChoice, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ModelChoice{}, fmt.Errorf("model name is empty")
	}
	for _, c := range choices {
		if c.ID == ref && !c.Disabled {
			return c, nil
		}
	}
	var matches []ModelChoice
	for _, c := range choices {
		if !c.Disabled && strings.EqualFold(c.Model, ref) {
			matches = append(matches, c)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		var ids []string
		for _, m := range matches {
			ids = append(ids, m.ID)
		}
		return ModelChoice{}, fmt.Errorf("model %q is ambiguous; specify an id: %s", ref, strings.Join(ids, ", "))
	}
	return ModelChoice{}, fmt.Errorf("model %q not found in catalog; use /models to list choices or ollama pull %s", ref, ref)
}

// resolveStartupModelTarget picks the active ModelTarget for a new agent session.
// explicitTarget, when non-nil, is the interactive selection (always wins).
// requestedModel should be the effective operator request (explicit --model,
// chat.default_model, or warm preferred) — not only the raw flag.
//
// Policy for provider=auto: prefer reachable local/remote ollama (or openai-local)
// over cloud; cloud only when no usable local target exists. Explicit --provider
// local/cloud and interactive selection are never overridden by auto-cloud.
// Explicit --cloud-model always requires a credentialed configured match (no silent fallback).
func resolveStartupModelTarget(
	requestedModel, providerFlag, cloudModelFlag string,
	explicit *ModelChoice,
	rt *runtimectx.Context,
	choices []ModelChoice,
) (ModelChoice, agent.CloudBackendOptions, error) {
	providerMode := strings.ToLower(strings.TrimSpace(providerFlag))
	if providerMode == "" {
		providerMode = "auto"
	}

	if explicit != nil && explicit.Model != "" {
		opts, err := cloudOptsForTarget(func(context.Context) (*runtimectx.Context, error) { return rt, nil }, explicit)
		return *explicit, opts, err
	}

	// Catalog helpers
	usable := func(c ModelChoice) bool { return !c.Disabled && c.Model != "" }
	firstLocal := func() (ModelChoice, bool) {
		for _, c := range choices {
			if usable(c) && c.ProviderKind == "local" && c.Protocol == agent.ProtocolOllama {
				return c, true
			}
		}
		for _, c := range choices {
			if usable(c) && c.ProviderKind == "local" {
				return c, true
			}
		}
		return ModelChoice{}, false
	}
	matchLocalModel := func(name string) (ModelChoice, bool) {
		var matches []ModelChoice
		for _, c := range choices {
			if usable(c) && c.ProviderKind == "local" && strings.EqualFold(c.Model, name) {
				matches = append(matches, c)
			}
		}
		if len(matches) == 1 {
			return matches[0], true
		}
		if len(matches) > 1 {
			// Prefer local node ollama
			for _, c := range matches {
				if c.Node == "" && c.Protocol == agent.ProtocolOllama {
					return c, true
				}
			}
			return matches[0], true
		}
		return ModelChoice{}, false
	}

	reqModel := strings.TrimSpace(requestedModel)
	cloudReq := strings.TrimSpace(cloudModelFlag)

	switch providerMode {
	case "cloud":
		// Only --cloud-model is an explicit cloud model request. When empty, select the
		// highest-priority credentialed provider and its cheapest configured model.
		// Do not treat chat.default_model / local preferred names as cloud model names.
		return resolveCloudStartupTarget(rt, cloudReq)

	case "local":
		if reqModel != "" {
			if t, ok := matchLocalModel(reqModel); ok {
				return t, agent.CloudBackendOptions{}, nil
			}
			// Explicit local model not in catalog: bind to default local ollama endpoint
			return syntheticLocalOllamaTarget(reqModel), agent.CloudBackendOptions{}, nil
		}
		if t, ok := firstLocal(); ok {
			return t, agent.CloudBackendOptions{}, nil
		}
		// Fallback local default name
		name := resolveAgentModel("", rt)
		return syntheticLocalOllamaTarget(name), agent.CloudBackendOptions{}, nil

	default: // auto
		// Explicit --cloud-model is operator intent: resolve cloud or fail (never ignore).
		if cloudReq != "" {
			return resolveCloudStartupTarget(rt, cloudReq)
		}
		// Effective requested model (flag / default_model / preferred) matching local catalog
		if reqModel != "" {
			if t, ok := matchLocalModel(reqModel); ok {
				return t, agent.CloudBackendOptions{}, nil
			}
			// Honor operator/default name even when not yet in the snapshot catalog.
			return syntheticLocalOllamaTarget(reqModel), agent.CloudBackendOptions{}, nil
		}
		// Prefer any usable local target from the catalog
		if t, ok := firstLocal(); ok {
			return t, agent.CloudBackendOptions{}, nil
		}
		// Else credentialed cloud by priority + cheapest model on that provider
		if t, opts, err := resolveCloudStartupTarget(rt, ""); err == nil {
			return t, opts, nil
		}
		// Last resort: local ollama with resolved default name
		name := resolveAgentModel("", rt)
		return syntheticLocalOllamaTarget(name), agent.CloudBackendOptions{}, nil
	}
}

var probeEndpointFn = func(url string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func collectModelChoices(rt *runtimectx.Context) []ModelChoice {
	var choices []ModelChoice
	if rt == nil {
		return choices
	}

	if rt.Snapshot != nil {
		// Identify unique remote endpoints to probe concurrently
		type probeResult struct {
			endpoint string
			ok       bool
		}
		endpointToNodes := make(map[string][]models.NodeFacts)
		for _, n := range rt.Snapshot.Nodes {
			// Probe Ollama instances
			if n.Ollama != nil && n.Ollama.Installed && !models.IsLocalNode(n) {
				endpoint, err := resolveNodeEndpoint(n, n.Ollama.Port)
				if err == nil && endpoint != "" {
					endpointToNodes[endpoint+"/api/tags"] = append(endpointToNodes[endpoint+"/api/tags"], n)
				}
			}
			// Probe MLX/llama.cpp resident models on every node, including
			// local. A hardcoded or stale port must not stay selectable.
			for _, rm := range n.ResidentModels {
				if (rm.Runtime == "mlx" || rm.Runtime == "llama.cpp") && rm.Port > 0 {
					endpoint, err := resolveNodeEndpoint(n, rm.Port)
					if err == nil && endpoint != "" {
						endpointToNodes[endpoint+"/v1/models"] = append(endpointToNodes[endpoint+"/v1/models"], n)
					}
				}
			}
		}

		ch := make(chan probeResult, len(endpointToNodes))
		var wg sync.WaitGroup
		for ep := range endpointToNodes {
			wg.Add(1)
			go func(endpoint string) {
				defer wg.Done()
				ok := probeEndpointFn(endpoint)
				ch <- probeResult{endpoint: endpoint, ok: ok}
			}(ep)
		}

		// Wait in background and close channel when done
		go func() {
			wg.Wait()
			close(ch)
		}()

		probeMap := make(map[string]bool)
		for res := range ch {
			probeMap[res.endpoint] = res.ok
		}

		seen := make(map[string]bool)
		for _, n := range rt.Snapshot.Nodes {
			var nodeLabel string
			if models.IsLocalNode(n) {
				nodeLabel = ""
			} else {
				nodeLabel = n.Name
			}

			// Add Ollama models
			if n.Ollama != nil && n.Ollama.Installed {
				endpoint, err := resolveNodeEndpoint(n, n.Ollama.Port)
				disabled := false
				reason := ""
				if err != nil {
					disabled = true
					reason = "no valid endpoint"
					endpoint = ""
				} else if !models.IsLocalNode(n) && !probeMap[endpoint+"/api/tags"] {
					disabled = true
					reason = "unreachable"
				}
				// Local security: process-local. Remote node HTTP is still cluster LAN.
				sec := agent.BackendLocal
				if !models.IsLocalNode(n) {
					sec = agent.BackendRemote
				}
				for _, mName := range n.Ollama.Models {
					key := n.Name + ":ollama:" + mName
					if !seen[key] {
						seen[key] = true
						choices = append(choices, ModelChoice{
							ID:             key,
							Model:          mName,
							Protocol:       agent.ProtocolOllama,
							ProviderName:   "ollama",
							ProviderKind:   "local",
							Node:           nodeLabel,
							Endpoint:       endpoint,
							SecurityClass:  sec,
							Disabled:       disabled,
							DisabledReason: reason,
						})
					}
				}
			}

			// Add Resident Models (llama.cpp / MLX / etc) — OpenAI-compatible protocol
			for _, rm := range n.ResidentModels {
				if rm.Runtime == "ollama" {
					continue // already covered above
				}
				endpoint, err := resolveNodeEndpoint(n, rm.Port)
				disabled := false
				reason := ""
				if err != nil || rm.Port <= 0 {
					disabled = true
					reason = "no valid endpoint"
					endpoint = ""
				} else if !probeMap[endpoint+"/v1/models"] {
					disabled = true
					reason = "unreachable"
				}
				sec := agent.BackendLocal
				if !models.IsLocalNode(n) {
					sec = agent.BackendRemote
				}
				key := n.Name + ":" + rm.Runtime + ":" + rm.Name
				if !seen[key] {
					seen[key] = true
					choices = append(choices, ModelChoice{
						ID:             key,
						Model:          rm.Name,
						Protocol:       agent.ProtocolOpenAI,
						ProviderName:   rm.Runtime,
						ProviderKind:   "local",
						Node:           nodeLabel,
						Endpoint:       endpoint,
						SecurityClass:  sec,
						Disabled:       disabled,
						DisabledReason: reason,
					})
				}
			}
		}
	}

	if rt.Config != nil {
		for pName, pCfg := range rt.Config.AIProviders {
			if pCfg.Enabled && strings.EqualFold(pCfg.Type, "cloud") {
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
						Node:           "",
						Endpoint:       pCfg.Endpoint,
						SecurityClass:  agent.BackendRemote,
						Disabled:       disabled,
						DisabledReason: reason,
					})
				}
			}
		}
	}

	// Inference roles from ~/.axis/ai.yaml (OpenAI-compatible / ollama backends).
	choices = append(choices, modelChoicesFromAIConfig(rt.Config)...)

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
