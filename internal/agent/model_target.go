package agent

import (
	"fmt"
	"strings"

	"github.com/toasterbook88/axis/internal/chat"
)

// Protocol identifies how to talk to a model endpoint.
const (
	ProtocolOllama = "ollama" // POST /api/chat
	ProtocolOpenAI = "openai" // POST /v1/chat/completions
	ProtocolCloud  = "cloud"  // cloud provider (OpenAI-compatible or Anthropic)
)

// ModelTarget is the canonical identity of a selectable model: name plus the
// protocol, provider, node, and endpoint that will receive requests.
// Every picker/slash-command path must resolve to a ModelTarget before
// constructing a ChatBackend.
type ModelTarget struct {
	ID             string
	Model          string
	Protocol       string // ProtocolOllama | ProtocolOpenAI | ProtocolCloud
	ProviderName   string // ollama | mlx | llama.cpp | cloud provider name
	ProviderKind   string // local | cloud
	Node           string // empty = local machine
	Endpoint       string // base URL (no /api or /v1 suffix required)
	SecurityClass  BackendSecurityClass
	Disabled       bool
	DisabledReason string
}

// CloudBackendOptions supplies cloud-only credentials when ProtocolCloud.
type CloudBackendOptions struct {
	ProviderKind string // openrouter, groq, anthropic, ...
	APIKey       string
	CostPer1K    float64
}

// BuildBackend constructs the ChatBackend for target. For ProtocolCloud, opts
// must include a non-empty API key and provider kind.
func BuildBackend(target ModelTarget, opts CloudBackendOptions) (ChatBackend, error) {
	if target.Disabled {
		reason := target.DisabledReason
		if reason == "" {
			reason = "disabled"
		}
		return nil, fmt.Errorf("model %q is not usable: %s", target.Model, reason)
	}
	if strings.TrimSpace(target.Model) == "" {
		return nil, fmt.Errorf("model name is empty")
	}

	switch target.Protocol {
	case ProtocolOllama:
		endpoint := strings.TrimSpace(target.Endpoint)
		if endpoint == "" {
			endpoint = chat.DefaultEndpoint
		}
		return chat.NewClient(endpoint, target.Model), nil

	case ProtocolOpenAI:
		endpoint := strings.TrimSpace(target.Endpoint)
		if endpoint == "" {
			return nil, fmt.Errorf("openai-compatible model %q has empty endpoint", target.Model)
		}
		return NewOpenAICompatibleBackend(endpoint, target.Model, opts.APIKey)

	case ProtocolCloud:
		if strings.TrimSpace(opts.APIKey) == "" {
			return nil, fmt.Errorf("cloud provider %q requires an API key", target.ProviderName)
		}
		kind := opts.ProviderKind
		if kind == "" {
			kind = target.ProviderName
		}
		return NewCloudBackendWithKey(kind, target.ProviderName, target.Endpoint, opts.APIKey, target.Model, opts.CostPer1K)

	default:
		return nil, fmt.Errorf("unsupported model protocol %q for %q", target.Protocol, target.Model)
	}
}

// DisplayProvider returns a short operator-facing provider label.
func (t ModelTarget) DisplayProvider() string {
	if t.Protocol == ProtocolCloud {
		return "Cloud (" + t.ProviderName + ")"
	}
	if t.ProviderName == "" {
		return "local"
	}
	if t.Node != "" {
		return fmt.Sprintf("%s @ %s", t.ProviderName, t.Node)
	}
	return t.ProviderName
}
