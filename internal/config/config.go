// Package config is STABLE — AXIS node configuration loader with strict YAML parsing.
// It is part of the stable operator path.
package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
	"gopkg.in/yaml.v3"
)

// NodeConfig describes a single node in the cluster seed file.
// ssh_user and ssh_port are config-only — they do NOT propagate into NodeFacts.
// stable_id is an optional operator seed used for locality/dedupe only; it
// does not override observed node identity.
type NodeConfig struct {
	Name            string `json:"name" yaml:"name"`
	Hostname        string `json:"hostname" yaml:"hostname"`
	StableID        string `json:"stable_id,omitempty" yaml:"stable_id,omitempty"`
	SSHUser         string `json:"ssh_user" yaml:"ssh_user"`
	Role            string `json:"role,omitempty" yaml:"role,omitempty"`
	SSHPort         int    `json:"ssh_port,omitempty" yaml:"ssh_port,omitempty"`
	TimeoutSec      int    `json:"timeout_sec,omitempty" yaml:"timeout_sec,omitempty"`
	SystemReserveMB int64  `json:"system_reserve_mb,omitempty" yaml:"system_reserve_mb,omitempty"`
}

// EffectiveSSHPort returns the SSH port, defaulting to 22.
func (n *NodeConfig) EffectiveSSHPort() int {
	if n.SSHPort <= 0 {
		return 22
	}
	return n.SSHPort
}

// EffectiveTimeout returns the timeout in seconds, defaulting to 10.
func (n *NodeConfig) EffectiveTimeout() int {
	if n.TimeoutSec <= 0 {
		return 10
	}
	return n.TimeoutSec
}

// DiscoveryConfig describes the UDP discovery properties.
type DiscoveryConfig struct {
	Enabled        bool   `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	UDPPort        int    `json:"udp_port,omitempty" yaml:"udp_port,omitempty"`
	BeaconInterval int    `json:"beacon_interval_sec,omitempty" yaml:"beacon_interval_sec,omitempty"`
	Secret         string `json:"secret,omitempty" yaml:"secret,omitempty"`
}

// ChatConfig holds optional operator preferences for the chat and agent surfaces.
// All fields are optional; omitting the section entirely is valid.
type ChatConfig struct {
	// DefaultModel is the Ollama model tag to use when no --model flag is given.
	// When unset, AXIS auto-selects the best available installed model.
	// Example: default_model: "llama3.2:latest"
	DefaultModel string `json:"default_model,omitempty" yaml:"default_model,omitempty"`
}

// AIModelConfig describes a single model within a provider config.
type AIModelConfig struct {
	Name      string   `json:"name" yaml:"name"`
	Aliases   []string `json:"aliases,omitempty" yaml:"aliases,omitempty"`
	CostPer1K float64  `json:"cost_per_1k,omitempty" yaml:"cost_per_1k,omitempty"`
}

// AIProviderConfig describes a single AI inference provider in nodes.yaml.
// The section is optional; omitting it entirely is valid.
//
// Example (nodes.yaml):
//
//	ai_providers:
//	  ollama-local:
//	    type: local
//	    endpoint: http://localhost:11434
//	    enabled: true
//	  openai:
//	    type: cloud
//	    api_key_env: OPENAI_API_KEY
//	    enabled: true
//	    priority: 80
type AIProviderConfig struct {
	// Type is "local" or "cloud".
	Type string `json:"type" yaml:"type"`

	// Kind is "openrouter", "groq", "anthropic", etc.
	Kind string `json:"kind,omitempty" yaml:"kind,omitempty"`

	// Endpoint is the base URL for the provider.
	// Cloud providers use a fixed default when this is unset.
	Endpoint string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`

	// APIKeyEnv is the environment variable that holds the API key.
	// Evaluated at runtime by internal/secrets.
	APIKeyEnv string `json:"api_key_env,omitempty" yaml:"api_key_env,omitempty"`

	// APIKeyFile is a path to a file whose contents are the API key.
	// Used as fallback if APIKeyEnv is unset or empty.
	APIKeyFile string `json:"api_key_file,omitempty" yaml:"api_key_file,omitempty"`

	// Priority is 0–100 (higher = preferred when multiple providers are eligible).
	Priority int `json:"priority,omitempty" yaml:"priority,omitempty"`

	// Enabled controls whether this provider is considered for routing.
	Enabled bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`

	// Models enumerates known models for this provider.
	// Auto-detected local providers (Ollama) do not require this.
	Models []AIModelConfig `json:"models,omitempty" yaml:"models,omitempty"`
}

// InferenceConfig holds optional cluster-wide inference preferences.
//
// Example (nodes.yaml):
//
//	inference:
//	  default_mode: local
//	  prefer: latency
//	  max_cost_per_request: 0.10
type InferenceConfig struct {
	// DefaultMode controls which providers are considered by default.
	// Valid values: "local" (default), "cloud", "auto".
	DefaultMode string `json:"default_mode,omitempty" yaml:"default_mode,omitempty"`

	// Prefer controls the tie-breaker when multiple providers are eligible.
	// Valid values: "latency" (default), "cost", "quality".
	Prefer string `json:"prefer,omitempty" yaml:"prefer,omitempty"`

	// MaxCostPerRequest is a hard cap in USD. Requests estimated to exceed
	// this are rejected before execution. 0 means no cap.
	MaxCostPerRequest float64 `json:"max_cost_per_request,omitempty" yaml:"max_cost_per_request,omitempty"`

	// BudgetAlertThreshold triggers a warning when daily spend exceeds this
	// amount in USD. 0 means no alert.
	BudgetAlertThreshold float64 `json:"budget_alert_threshold,omitempty" yaml:"budget_alert_threshold,omitempty"`
}

// MCPServerConfig describes a single external MCP server connection.
type MCPServerConfig struct {
	// Transport is "stdio" or "http".
	Transport string `json:"transport" yaml:"transport"`

	// Command is the executable and arguments for stdio transport.
	Command []string `json:"command,omitempty" yaml:"command,omitempty"`

	// URL is the endpoint for HTTP/SSE transport.
	URL string `json:"url,omitempty" yaml:"url,omitempty"`

	// Headers are optional HTTP headers (for http transport).
	Headers map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
}

// Config is the top-level AXIS configuration.
type Config struct {
	Nodes       []NodeConfig                `json:"nodes" yaml:"nodes"`
	Discovery   *DiscoveryConfig            `json:"discovery,omitempty" yaml:"discovery,omitempty"`
	Chat        *ChatConfig                 `json:"chat,omitempty" yaml:"chat,omitempty"`
	AIProviders map[string]AIProviderConfig `json:"ai_providers,omitempty" yaml:"ai_providers,omitempty"`
	Inference   *InferenceConfig            `json:"inference,omitempty" yaml:"inference,omitempty"`
	MCPServers  map[string]MCPServerConfig  `json:"mcp_servers,omitempty" yaml:"mcp_servers,omitempty"`
	Webhooks    []string                    `json:"webhooks,omitempty" yaml:"webhooks,omitempty"`
}

// DefaultConfigPath returns ~/.axis/nodes.yaml.
func DefaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".axis", "nodes.yaml")
}

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	var cfg Config
	if err := decodeStrict(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if err := cfg.MigrateProviders(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func decodeStrict(data []byte, cfg *Config) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return err
	}

	var extra any
	if err := dec.Decode(&extra); err != nil {
		if err == io.EOF {
			return nil
		}
		return err
	}
	return fmt.Errorf("multiple YAML documents are not supported")
}

// FindNode returns the configuration for the specified node name.
func (c *Config) FindNode(name string) (NodeConfig, bool) {
	if c == nil {
		return NodeConfig{}, false
	}
	for _, n := range c.Nodes {
		if strings.EqualFold(n.Name, name) {
			return n, true
		}
	}
	return NodeConfig{}, false
}

// IsMeshEnabled returns whether the mesh gossip layer should be started.
// For backward compatibility, mesh is enabled when the discovery config
// is absent. When discovery is explicitly configured, mesh follows Enabled.
func (c *Config) IsMeshEnabled() bool {
	if c == nil || c.Discovery == nil {
		return true
	}
	return c.Discovery.Enabled
}

// MigrateProviders infers missing Kind for cloud providers and canonicalizes it.
func (c *Config) MigrateProviders() error {
	for name, prov := range c.AIProviders {
		if strings.EqualFold(prov.Type, "cloud") {
			kind := strings.ToLower(strings.TrimSpace(prov.Kind))
			if kind == "" {
				inferred := ""
				nameLower := strings.ToLower(name)
				epLower := strings.ToLower(prov.Endpoint)

				matchesOpenRouter := strings.Contains(nameLower, "openrouter") || strings.Contains(epLower, "openrouter.ai")
				matchesGroq := strings.Contains(nameLower, "groq") || strings.Contains(epLower, "groq.com")
				matchesAnthropic := strings.Contains(nameLower, "anthropic") || strings.Contains(nameLower, "claude") || strings.Contains(epLower, "anthropic.com")

				count := 0
				if matchesOpenRouter {
					inferred = "openrouter"
					count++
				}
				if matchesGroq {
					inferred = "groq"
					count++
				}
				if matchesAnthropic {
					inferred = "anthropic"
					count++
				}

				if count == 1 {
					prov.Kind = inferred
				} else {
					return fmt.Errorf("provider %q: missing kind and could not make unambiguous inference from name/endpoint", name)
				}
			} else {
				prov.Kind = kind
			}
			c.AIProviders[name] = prov
		}
	}
	return nil
}

// Validate checks that all required fields are present.
func (c *Config) Validate() error {
	if len(c.Nodes) == 0 {
		return fmt.Errorf("config: no nodes defined")
	}
	nodeNames := make(map[string]bool)
	for i := range c.Nodes {
		n := &c.Nodes[i]
		if n.Name == "" {
			return fmt.Errorf("config: node[%d] missing name", i)
		}
		lowerName := strings.ToLower(n.Name)
		if nodeNames[lowerName] {
			return fmt.Errorf("config: duplicate node name %q (case-insensitive)", n.Name)
		}
		nodeNames[lowerName] = true

		if n.Hostname == "" {
			return fmt.Errorf("config: node[%d] (%s) missing hostname", i, n.Name)
		}
		if n.SSHUser == "" {
			return fmt.Errorf("config: node[%d] (%s) missing ssh_user", i, n.Name)
		}
		if n.SystemReserveMB < 0 {
			return fmt.Errorf("config: node[%d] (%s) system_reserve_mb cannot be negative: %d", i, n.Name, n.SystemReserveMB)
		}
		n.StableID = models.NormalizeStableID(n.StableID)
	}

	for name, prov := range c.AIProviders {
		if strings.EqualFold(prov.Type, "cloud") {
			kind := strings.ToLower(strings.TrimSpace(prov.Kind))
			if kind != "openrouter" && kind != "groq" && kind != "anthropic" {
				return fmt.Errorf("provider %q: unsupported cloud provider kind %q", name, prov.Kind)
			}
		}
	}

	return nil
}
