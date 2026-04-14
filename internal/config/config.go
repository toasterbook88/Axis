// Package config loads AXIS node configuration from ~/.axis/nodes.yaml.
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
	Name       string `json:"name" yaml:"name"`
	Hostname   string `json:"hostname" yaml:"hostname"`
	StableID   string `json:"stable_id,omitempty" yaml:"stable_id,omitempty"`
	SSHUser    string `json:"ssh_user" yaml:"ssh_user"`
	Role       string `json:"role,omitempty" yaml:"role,omitempty"`
	SSHPort    int    `json:"ssh_port,omitempty" yaml:"ssh_port,omitempty"`
	TimeoutSec int    `json:"timeout_sec,omitempty" yaml:"timeout_sec,omitempty"`
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

// Config is the top-level AXIS configuration.
type Config struct {
	Nodes       []NodeConfig                `json:"nodes" yaml:"nodes"`
	Discovery   *DiscoveryConfig            `json:"discovery,omitempty" yaml:"discovery,omitempty"`
	Chat        *ChatConfig                 `json:"chat,omitempty" yaml:"chat,omitempty"`
	AIProviders map[string]AIProviderConfig `json:"ai_providers,omitempty" yaml:"ai_providers,omitempty"`
	Inference   *InferenceConfig            `json:"inference,omitempty" yaml:"inference,omitempty"`
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

// Validate checks that all required fields are present.
func (c *Config) Validate() error {
	if len(c.Nodes) == 0 {
		return fmt.Errorf("config: no nodes defined")
	}
	for i := range c.Nodes {
		n := &c.Nodes[i]
		if n.Name == "" {
			return fmt.Errorf("config: node[%d] missing name", i)
		}
		if n.Hostname == "" {
			return fmt.Errorf("config: node[%d] (%s) missing hostname", i, n.Name)
		}
		if n.SSHUser == "" {
			return fmt.Errorf("config: node[%d] (%s) missing ssh_user", i, n.Name)
		}
		n.StableID = models.NormalizeStableID(n.StableID)
	}
	return nil
}
