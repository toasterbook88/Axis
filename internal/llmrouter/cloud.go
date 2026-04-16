package llmrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/secrets"
)

const (
	defaultCloudTimeout      = 5 * time.Second
	estimatedResponseTokens  = 256
	openRouterDefaultBaseURL = "https://openrouter.ai/api/v1"
	groqDefaultBaseURL       = "https://api.groq.com/openai/v1"
	anthropicDefaultBaseURL  = "https://api.anthropic.com/v1"
)

// CloudModel describes a configured cloud model and its routing metadata.
type CloudModel struct {
	Name      string
	Aliases   []string
	CostPer1K float64
}

// CloudProviderConfig is the constructor input for cloud-backed providers.
type CloudProviderConfig struct {
	Name       string
	Endpoint   string
	APIKey     string
	Priority   int
	Models     []CloudModel
	HTTPClient *http.Client
}

type cloudProviderKind string

const (
	cloudProviderOpenRouter cloudProviderKind = "openrouter"
	cloudProviderGroq       cloudProviderKind = "groq"
	cloudProviderAnthropic  cloudProviderKind = "anthropic"
)

type cloudProvider struct {
	kind       cloudProviderKind
	name       string
	endpoint   string
	apiKey     string
	priority   int
	models     []CloudModel
	httpClient *http.Client
}

func NewOpenRouterProvider(cfg CloudProviderConfig) Provider {
	return newCloudProvider(cloudProviderOpenRouter, cfg, openRouterDefaultBaseURL)
}

func NewGroqProvider(cfg CloudProviderConfig) Provider {
	return newCloudProvider(cloudProviderGroq, cfg, groqDefaultBaseURL)
}

func NewAnthropicProvider(cfg CloudProviderConfig) Provider {
	return newCloudProvider(cloudProviderAnthropic, cfg, anthropicDefaultBaseURL)
}

func newCloudProvider(kind cloudProviderKind, cfg CloudProviderConfig, defaultEndpoint string) *cloudProvider {
	modelsCopy := append([]CloudModel(nil), cfg.Models...)
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultCloudTimeout}
	}

	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		endpoint = defaultEndpoint
	}

	return &cloudProvider{
		kind:       kind,
		name:       strings.TrimSpace(cfg.Name),
		endpoint:   endpoint,
		apiKey:     strings.TrimSpace(cfg.APIKey),
		priority:   cfg.Priority,
		models:     modelsCopy,
		httpClient: client,
	}
}

func (p *cloudProvider) Name() string {
	return p.name
}

func (p *cloudProvider) Type() ProviderType {
	return ProviderCloud
}

func (p *cloudProvider) Health(ctx context.Context) (HealthStatus, error) {
	start := time.Now()

	// All providers are probed via GET /models to get real health + latency data.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+"/models", nil)
	if err != nil {
		return HealthStatus{}, fmt.Errorf("cloud health request: %w", err)
	}
	p.applyAuth(req)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return HealthStatus{}, fmt.Errorf("%s: health probe: %w", p.name, err)
	}
	defer resp.Body.Close()

	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	latency := time.Since(start)

	// Anthropic returns 200 for valid keys; other providers do the same.
	// Accept any 2xx as healthy — some endpoints return 200, some 204.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return HealthStatus{
			OK:      true,
			Latency: latency,
			Message: "ok",
		}, nil
	}

	return HealthStatus{
		OK:      false,
		Latency: latency,
		Message: fmt.Sprintf("status %d", resp.StatusCode),
	}, nil
}

func (p *cloudProvider) SupportsModel(model string) bool {
	if strings.TrimSpace(model) == "" {
		return p.defaultModel() != ""
	}
	for _, candidate := range p.models {
		if strings.EqualFold(candidate.Name, model) {
			return true
		}
		for _, alias := range candidate.Aliases {
			if strings.EqualFold(alias, model) {
				return true
			}
		}
	}
	return false
}

func (p *cloudProvider) EstimateCost(prompt, model string) float64 {
	selected, ok := p.lookupModel(model)
	if !ok || selected.CostPer1K <= 0 {
		return 0
	}
	estimatedTokens := estimateTokens(prompt) + estimatedResponseTokens
	return (float64(estimatedTokens) / 1000.0) * selected.CostPer1K
}

// costFromTokens calculates cost using actual API-reported token counts.
// Falls back to the heuristic estimate if actual tokens are unavailable.
func (p *cloudProvider) costFromTokens(prompt, model string, tokensIn, tokensOut int) float64 {
	actualTokens := tokensIn + tokensOut
	if actualTokens <= 0 {
		return p.EstimateCost(prompt, model)
	}
	selected, ok := p.lookupModel(model)
	if !ok || selected.CostPer1K <= 0 {
		return 0
	}
	return (float64(actualTokens) / 1000.0) * selected.CostPer1K
}

func (p *cloudProvider) Send(ctx context.Context, prompt, model string) (GenerateResult, error) {
	selected, ok := p.lookupModel(model)
	if !ok {
		return GenerateResult{}, fmt.Errorf("%s: unsupported model %q", p.name, model)
	}

	start := time.Now()
	switch p.kind {
	case cloudProviderAnthropic:
		return p.sendAnthropic(ctx, prompt, selected, start)
	default:
		return p.sendOpenAICompatible(ctx, prompt, selected, start)
	}
}

func (p *cloudProvider) Endpoint() string {
	return p.endpoint
}

func (p *cloudProvider) DefaultModel() string {
	return p.defaultModel()
}

func (p *cloudProvider) Priority() int {
	return p.priority
}

func (p *cloudProvider) defaultModel() string {
	bestName := ""
	bestCost := 0.0
	for _, model := range p.models {
		if model.Name == "" {
			continue
		}
		if bestName == "" {
			bestName = model.Name
		}
		if model.CostPer1K > 0 && (bestCost == 0 || model.CostPer1K < bestCost) {
			bestCost = model.CostPer1K
			bestName = model.Name
		}
	}
	return bestName
}

func (p *cloudProvider) lookupModel(model string) (CloudModel, bool) {
	needle := strings.TrimSpace(model)
	if needle == "" {
		needle = p.defaultModel()
	}
	for _, candidate := range p.models {
		if strings.EqualFold(candidate.Name, needle) {
			return candidate, true
		}
		for _, alias := range candidate.Aliases {
			if strings.EqualFold(alias, needle) {
				return candidate, true
			}
		}
	}
	return CloudModel{}, false
}

func (p *cloudProvider) applyAuth(req *http.Request) {
	switch p.kind {
	case cloudProviderAnthropic:
		req.Header.Set("x-api-key", p.apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
}

func (p *cloudProvider) sendOpenAICompatible(ctx context.Context, prompt string, selected CloudModel, start time.Time) (GenerateResult, error) {
	body, err := json.Marshal(map[string]any{
		"model": selected.Name,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens":  256,
		"temperature": 0.1,
	})
	if err != nil {
		return GenerateResult{}, fmt.Errorf("%s: marshal request: %w", p.name, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return GenerateResult{}, fmt.Errorf("%s: build request: %w", p.name, err)
	}
	req.Header.Set("Content-Type", "application/json")
	p.applyAuth(req)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return GenerateResult{}, fmt.Errorf("%s: send request: %w", p.name, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxGenerateResponseBytes))
	if err != nil {
		return GenerateResult{}, fmt.Errorf("%s: read response: %w", p.name, err)
	}
	if resp.StatusCode != http.StatusOK {
		return GenerateResult{}, fmt.Errorf("%s: status %d: %s", p.name, resp.StatusCode, string(raw))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return GenerateResult{}, fmt.Errorf("%s: parse response: %w", p.name, err)
	}
	if len(parsed.Choices) == 0 {
		return GenerateResult{}, fmt.Errorf("%s: empty response", p.name)
	}

	return GenerateResult{
		Response:  parsed.Choices[0].Message.Content,
		TokensIn:  parsed.Usage.PromptTokens,
		TokensOut: parsed.Usage.CompletionTokens,
		LatencyMs: time.Since(start).Milliseconds(),
		Cost:      (float64(parsed.Usage.PromptTokens+parsed.Usage.CompletionTokens) / 1000.0) * selected.CostPer1K,
	}, nil
}

func (p *cloudProvider) sendAnthropic(ctx context.Context, prompt string, selected CloudModel, start time.Time) (GenerateResult, error) {
	body, err := json.Marshal(map[string]any{
		"model":      selected.Name,
		"max_tokens": 256,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	})
	if err != nil {
		return GenerateResult{}, fmt.Errorf("%s: marshal request: %w", p.name, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/messages", bytes.NewReader(body))
	if err != nil {
		return GenerateResult{}, fmt.Errorf("%s: build request: %w", p.name, err)
	}
	req.Header.Set("Content-Type", "application/json")
	p.applyAuth(req)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return GenerateResult{}, fmt.Errorf("%s: send request: %w", p.name, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxGenerateResponseBytes))
	if err != nil {
		return GenerateResult{}, fmt.Errorf("%s: read response: %w", p.name, err)
	}
	if resp.StatusCode != http.StatusOK {
		return GenerateResult{}, fmt.Errorf("%s: status %d: %s", p.name, resp.StatusCode, string(raw))
	}

	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return GenerateResult{}, fmt.Errorf("%s: parse response: %w", p.name, err)
	}
	var response strings.Builder
	for _, block := range parsed.Content {
		if block.Type == "text" {
			if response.Len() > 0 {
				response.WriteByte('\n')
			}
			response.WriteString(block.Text)
		}
	}
	if response.Len() == 0 {
		return GenerateResult{}, fmt.Errorf("%s: empty response", p.name)
	}

	return GenerateResult{
		Response:  response.String(),
		TokensIn:  parsed.Usage.InputTokens,
		TokensOut: parsed.Usage.OutputTokens,
		LatencyMs: time.Since(start).Milliseconds(),
		Cost:      (float64(parsed.Usage.InputTokens+parsed.Usage.OutputTokens) / 1000.0) * selected.CostPer1K,
	}, nil
}

// NewRegistryFromConfig builds a provider registry from configured, enabled AI
// providers that AXIS knows how to route today.
func NewRegistryFromConfig(cfg *config.Config) (*Registry, error) {
	registry := NewRegistry()
	if cfg == nil || len(cfg.AIProviders) == 0 {
		return registry, nil
	}

	names := make([]string, 0, len(cfg.AIProviders))
	for name := range cfg.AIProviders {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		providerCfg := cfg.AIProviders[name]
		if !providerCfg.Enabled || !strings.EqualFold(providerCfg.Type, string(ProviderCloud)) {
			continue
		}

		apiKey, err := secrets.ResolveOrEmpty(providerCfg.APIKeyEnv, providerCfg.APIKeyFile)
		if err != nil {
			return nil, fmt.Errorf("provider %s: %w", name, err)
		}
		if apiKey == "" {
			continue
		}

		provider, err := newConfiguredCloudProvider(name, providerCfg, apiKey)
		if err != nil {
			return nil, err
		}
		if err := registry.Register(provider); err != nil {
			return nil, err
		}
	}

	return registry, nil
}

func newConfiguredCloudProvider(name string, cfg config.AIProviderConfig, apiKey string) (Provider, error) {
	models := make([]CloudModel, 0, len(cfg.Models))
	for _, model := range cfg.Models {
		if strings.TrimSpace(model.Name) == "" {
			continue
		}
		models = append(models, CloudModel{
			Name:      model.Name,
			Aliases:   append([]string(nil), model.Aliases...),
			CostPer1K: model.CostPer1K,
		})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("provider %s: no models configured", name)
	}

	if err := validateEndpointSecurity(cfg.Endpoint, name); err != nil {
		return nil, err
	}

	providerCfg := CloudProviderConfig{
		Name:     name,
		Endpoint: cfg.Endpoint,
		APIKey:   apiKey,
		Priority: cfg.Priority,
		Models:   models,
	}

	switch detectCloudProviderKind(name, cfg.Endpoint) {
	case cloudProviderOpenRouter:
		return NewOpenRouterProvider(providerCfg), nil
	case cloudProviderGroq:
		return NewGroqProvider(providerCfg), nil
	case cloudProviderAnthropic:
		return NewAnthropicProvider(providerCfg), nil
	default:
		return nil, fmt.Errorf("provider %s: unsupported cloud provider", name)
	}
}

// validateEndpointSecurity rejects non-HTTPS endpoints unless the hostname
// is a loopback address (localhost, 127.0.0.1, [::1]). Empty endpoints are
// allowed (the provider constructor fills in the default).
func validateEndpointSecurity(endpoint, providerName string) error {
	trimmed := strings.TrimSpace(endpoint)
	if trimmed == "" {
		return nil
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("provider %s: invalid endpoint URL: %w", providerName, err)
	}

	if strings.EqualFold(parsed.Scheme, "https") || parsed.Scheme == "" {
		return nil
	}

	if !strings.EqualFold(parsed.Scheme, "http") {
		return fmt.Errorf("provider %s: unsupported endpoint scheme %q", providerName, parsed.Scheme)
	}

	// HTTP is only allowed for loopback addresses.
	host := parsed.Hostname()
	if isLoopback(host) {
		return nil
	}

	return fmt.Errorf("provider %s: insecure http endpoint not allowed (only loopback addresses permitted for http)", providerName)
}

// isLoopback returns true when host is a loopback address: "localhost",
// "127.0.0.1", "::1", or any IP that net.IP.IsLoopback reports as loopback.
// IPv6 zone identifiers (e.g. "::1%eth0") are stripped before parsing.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	// Strip IPv6 zone identifier if present (e.g. "::1%eth0" → "::1").
	if idx := strings.IndexByte(host, '%'); idx >= 0 {
		host = host[:idx]
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func detectCloudProviderKind(name, endpoint string) cloudProviderKind {
	needle := strings.ToLower(strings.TrimSpace(name) + " " + strings.TrimSpace(endpoint))
	switch {
	case strings.Contains(needle, "openrouter"):
		return cloudProviderOpenRouter
	case strings.Contains(needle, "groq"):
		return cloudProviderGroq
	case strings.Contains(needle, "anthropic"), strings.Contains(needle, "claude"):
		return cloudProviderAnthropic
	default:
		return ""
	}
}

type cloudSelectionCandidate struct {
	provider Provider
	model    string
	status   HealthStatus
	cost     float64
	priority int
	endpoint string
}

// SelectCloudFallback returns the best healthy cloud provider for a fallback
// classification call. It probes all cloud providers concurrently, filters out
// unhealthy ones, and sorts by the configured preference ("latency" by default,
// matching the documented inference.prefer default).
func SelectCloudFallback(ctx context.Context, registry *Registry, prompt, prefer string) (Provider, RoutingDecision, error) {
	if registry == nil {
		return nil, RoutingDecision{}, errors.New("cloud registry is nil")
	}

	providers := registry.ListByType(ProviderCloud)
	if len(providers) == 0 {
		return nil, RoutingDecision{}, errors.New("no cloud providers configured")
	}

	// Probe all cloud providers concurrently for real health + latency data.
	healthStatuses := registry.CheckHealth(ctx)

	candidates := make([]cloudSelectionCandidate, 0, len(providers))
	for _, provider := range providers {
		model := defaultProviderModel(provider)
		if model == "" || !provider.SupportsModel(model) {
			continue
		}

		status, probed := healthStatuses[provider.Name()]
		if !probed || !status.OK {
			continue
		}

		candidates = append(candidates, cloudSelectionCandidate{
			provider: provider,
			model:    model,
			status:   status,
			cost:     provider.EstimateCost(prompt, model),
			priority: providerPriority(provider),
			endpoint: providerEndpoint(provider),
		})
	}
	if len(candidates) == 0 {
		return nil, RoutingDecision{}, errors.New("no healthy cloud providers available")
	}

	// Default to "latency" when prefer is empty or unrecognized,
	// matching the documented inference.prefer default.
	normalizedPrefer := strings.ToLower(strings.TrimSpace(prefer))
	if normalizedPrefer != "cost" {
		normalizedPrefer = "latency"
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]
		if normalizedPrefer == "cost" {
			return compareCloudCandidatesByCost(left, right)
		}
		return compareCloudCandidatesByLatency(left, right)
	})

	best := candidates[0]
	reasoning := []string{
		fmt.Sprintf("local semantic path fell back to reflex; selected healthy cloud provider %q", best.provider.Name()),
	}
	if normalizedPrefer == "cost" {
		reasoning = append(reasoning, "selection preference: lowest estimated cost among healthy cloud providers")
	} else {
		reasoning = append(reasoning, "selection preference: lowest latency among healthy cloud providers")
	}

	fallbacks := make([]string, 0, len(candidates)-1)
	for _, candidate := range candidates[1:] {
		fallbacks = append(fallbacks, candidate.provider.Name())
	}

	decision := RoutingDecision{
		Provider:   best.provider.Name(),
		Model:      best.model,
		Endpoint:   best.endpoint,
		Reasoning:  reasoning,
		EstCost:    best.cost,
		EstLatency: best.status.Latency.String(),
		IsLocal:    false,
		Fallbacks:  fallbacks,
		Confidence: 0.85,
	}
	return best.provider, decision, nil
}

func compareCloudCandidatesByLatency(left, right cloudSelectionCandidate) bool {
	if left.status.Latency != right.status.Latency {
		return left.status.Latency < right.status.Latency
	}
	if costKnown(left.cost) != costKnown(right.cost) {
		return costKnown(left.cost)
	}
	if left.cost != right.cost {
		return left.cost < right.cost
	}
	if left.priority != right.priority {
		return left.priority > right.priority
	}
	return left.provider.Name() < right.provider.Name()
}

func compareCloudCandidatesByCost(left, right cloudSelectionCandidate) bool {
	if costKnown(left.cost) != costKnown(right.cost) {
		return costKnown(left.cost)
	}
	if left.cost != right.cost {
		return left.cost < right.cost
	}
	if left.status.Latency != right.status.Latency {
		return left.status.Latency < right.status.Latency
	}
	if left.priority != right.priority {
		return left.priority > right.priority
	}
	return left.provider.Name() < right.provider.Name()
}

func costKnown(cost float64) bool {
	return cost > 0
}

func defaultProviderModel(provider Provider) string {
	type providerModelDefaults interface {
		DefaultModel() string
	}
	if defaults, ok := provider.(providerModelDefaults); ok {
		return defaults.DefaultModel()
	}
	return ""
}

func providerPriority(provider Provider) int {
	type providerPriorities interface {
		Priority() int
	}
	if priorities, ok := provider.(providerPriorities); ok {
		return priorities.Priority()
	}
	return 0
}

func providerEndpoint(provider Provider) string {
	type providerEndpoints interface {
		Endpoint() string
	}
	if endpoints, ok := provider.(providerEndpoints); ok {
		return endpoints.Endpoint()
	}
	return ""
}

// ClassifyWithProvider runs a semantic classification prompt through an
// already-selected provider and converts the response into AXIS signal types.
func ClassifyWithProvider(ctx context.Context, provider Provider, prompt, model string) (models.WorkloadClass, IntentSignal, error) {
	result, err := provider.Send(ctx, buildClassifyPrompt(prompt, ""), model)
	if err != nil {
		return models.ClassUnknown, IntentSignal{}, err
	}

	raw := strings.TrimSpace(result.Response)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	if start := strings.Index(raw, "{"); start >= 0 {
		if end := strings.LastIndex(raw, "}"); end >= start {
			raw = raw[start : end+1]
		}
	}

	var parsed classifyResponse
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return models.ClassUnknown, IntentSignal{}, fmt.Errorf("parse classify json: %w", err)
	}

	class, ok := parseWorkloadClass(parsed.Class)
	if !ok {
		return models.ClassUnknown, IntentSignal{}, fmt.Errorf("unrecognised class %q", parsed.Class)
	}

	return class, IntentSignal{
		Class:      class,
		Confidence: clamp(parsed.Confidence, 0, 1),
		Signals:    append([]string(nil), parsed.Signals...),
		Source:     SourceSemantic,
	}, nil
}

func estimateTokens(prompt string) int {
	if strings.TrimSpace(prompt) == "" {
		return 1
	}
	// Simple 4-char/token heuristic keeps cost checks fast and dependency-free.
	return max(1, (len(prompt)+3)/4)
}
