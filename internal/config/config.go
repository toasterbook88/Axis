// Package config loads AXIS node configuration from ~/.axis/nodes.yaml.
package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// NodeConfig describes a single node in the cluster seed file.
// ssh_user and ssh_port are config-only — they do NOT propagate into NodeFacts.
type NodeConfig struct {
	Name       string `json:"name" yaml:"name"`
	Hostname   string `json:"hostname" yaml:"hostname"`
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

// Config is the top-level AXIS configuration.
type Config struct {
	Nodes     []NodeConfig     `json:"nodes" yaml:"nodes"`
	Discovery *DiscoveryConfig `json:"discovery,omitempty" yaml:"discovery,omitempty"`
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
