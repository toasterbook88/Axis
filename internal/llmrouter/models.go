package llmrouter

import (
	"context"
	"time"
)

// ProviderType categorises an inference provider as local or cloud.
type ProviderType string

const (
	// ProviderLocal covers Ollama, llama.cpp, MLX, and Apple Foundation Models.
	// Local providers are used by default; no API key required.
	ProviderLocal ProviderType = "local"

	// ProviderCloud covers OpenAI, Anthropic, Groq, and similar services.
	// Cloud providers require explicit opt-in (--cloud flag or config).
	ProviderCloud ProviderType = "cloud"
)

// HealthStatus is the result of a Provider health check.
type HealthStatus struct {
	// OK is true when the provider responded successfully within the deadline.
	OK bool

	// Latency is the round-trip time of the health probe. Zero when OK is false.
	Latency time.Duration

	// Message carries an optional human-readable description (error text or
	// the provider's own status string).
	Message string
}

// ModelCapability describes what a model can do.
// Used by the routing engine to filter providers for a given task.
type ModelCapability struct {
	// ContextWindow is the maximum supported context size in tokens.
	ContextWindow int

	// SupportsJSON indicates the model reliably produces valid JSON output
	// (e.g. via grammar-constrained decoding or explicit instruction-following).
	SupportsJSON bool

	// SupportsVision indicates the model can accept image inputs.
	// AXIS v0.8.0 routes text-only; this field is reserved for future use.
	SupportsVision bool

	// MaxTokens is the maximum number of tokens the model will generate.
	MaxTokens int
}

// GenerateResult holds the outcome of a single Provider.Generate call.
type GenerateResult struct {
	// Response is the model's text output.
	Response string

	// TokensIn is the number of prompt tokens consumed.
	TokensIn int

	// TokensOut is the number of tokens generated.
	TokensOut int

	// LatencyMs is the wall-clock time of the generate call in milliseconds.
	LatencyMs int64

	// Cost is the estimated USD cost of this call. Always 0 for local providers.
	Cost float64
}

// RoutingDecision is the advisory output of the routing engine.
// It is returned to the caller before any generation occurs so the operator
// can review or override the selection.
type RoutingDecision struct {
	// Provider is the selected provider name (matches the registry key).
	Provider string `json:"provider" yaml:"provider"`

	// Model is the selected model tag (e.g. "llama3.1:8b", "gpt-4o").
	Model string `json:"model" yaml:"model"`

	// Endpoint is the base URL the provider will be contacted at.
	Endpoint string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`

	// Reasoning lists the rules that produced this decision, in evaluation order.
	Reasoning []string `json:"reasoning" yaml:"reasoning"`

	// EstCost is the estimated USD cost. 0 for local providers.
	EstCost float64 `json:"estimated_cost,omitempty" yaml:"estimated_cost,omitempty"`

	// EstLatency is a human-readable latency estimate (e.g. "50ms", "2s").
	EstLatency string `json:"estimated_latency" yaml:"estimated_latency"`

	// IsLocal is true when Provider.Type() == ProviderLocal.
	IsLocal bool `json:"is_local" yaml:"is_local"`

	// Fallbacks lists provider names that would be tried if the primary fails.
	Fallbacks []string `json:"fallback_providers,omitempty" yaml:"fallback_providers,omitempty"`

	// Confidence is the router's confidence in this decision [0.0, 1.0].
	Confidence float64 `json:"confidence" yaml:"confidence"`
}

// Provider is the interface implemented by every inference backend.
// All implementations must be safe for concurrent use.
type Provider interface {
	// Name returns the provider's registry key (matches the config map key).
	Name() string

	// Type returns ProviderLocal or ProviderCloud.
	Type() ProviderType

	// Health probes the provider and reports its current status.
	// Implementations must respect ctx deadlines.
	Health(ctx context.Context) (HealthStatus, error)

	// SupportsModel reports whether the provider can serve the named model.
	// model may be a full tag ("llama3.1:8b") or an alias ("fast").
	SupportsModel(model string) bool

	// Generate runs a single inference call and returns the result.
	// Implementations must respect ctx deadlines and never contact cloud
	// endpoints unless Type() == ProviderCloud.
	Generate(ctx context.Context, prompt, model string) (GenerateResult, error)
}
