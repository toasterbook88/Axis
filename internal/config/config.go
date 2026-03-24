// Package config loads AXIS node configuration from ~/.axis/nodes.yaml.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// NodeConfig describes a single node in the cluster seed file.
// ssh_user and ssh_port are config-only — they do NOT propagate into NodeFacts.
type NodeConfig struct {
	Name       string `yaml:"name"`
	Hostname   string `yaml:"hostname"`
	SSHUser    string `yaml:"ssh_user"`
	Role       string `yaml:"role,omitempty"`
	SSHPort    int    `yaml:"ssh_port,omitempty"`
	TimeoutSec int    `yaml:"timeout_sec,omitempty"`
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
	Enabled        bool   `yaml:"enabled,omitempty"`
	UDPPort        int    `yaml:"udp_port,omitempty"`
	BeaconInterval int    `yaml:"beacon_interval_sec,omitempty"`
	Secret         string `yaml:"secret,omitempty"`
}

// Config is the top-level AXIS configuration.
type Config struct {
	Nodes     []NodeConfig     `yaml:"nodes"`
	Discovery *DiscoveryConfig `yaml:"discovery,omitempty"`
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
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks that all required fields are present.
func (c *Config) Validate() error {
	if len(c.Nodes) == 0 {
		return fmt.Errorf("config: no nodes defined")
	}
	for i, n := range c.Nodes {
		if n.Name == "" {
			return fmt.Errorf("config: node[%d] missing name", i)
		}
		if n.Hostname == "" {
			return fmt.Errorf("config: node[%d] (%s) missing hostname", i, n.Name)
		}
		if n.SSHUser == "" {
			return fmt.Errorf("config: node[%d] (%s) missing ssh_user", i, n.Name)
		}
	}
	return nil
}
