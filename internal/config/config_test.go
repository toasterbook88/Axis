package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	os.WriteFile(path, []byte(`
nodes:
  - name: node-a
    hostname: node-a.local
    stable_id: F47AC10B-58CC-4372-A567-0E02B2C3D479
    ssh_user: user
    role: cortex
  - name: node-b
    hostname: node-b.local
    ssh_user: user
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(cfg.Nodes))
	}
	if cfg.Nodes[0].Name != "node-a" {
		t.Errorf("node[0].name: got %q, want node-a", cfg.Nodes[0].Name)
	}
	if cfg.Nodes[0].StableID != "f47ac10b-58cc-4372-a567-0e02b2c3d479" {
		t.Errorf("node[0].stable_id: got %q", cfg.Nodes[0].StableID)
	}
	if cfg.Nodes[1].Hostname != "node-b.local" {
		t.Errorf("node[1].hostname: got %q, want node-b.local", cfg.Nodes[1].Hostname)
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	os.WriteFile(path, []byte(`{{{not yaml`), 0644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoad_ValidConfigWithDiscovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	os.WriteFile(path, []byte(`
nodes:
  - name: node-a
    hostname: node-a.local
    ssh_user: user
discovery:
  enabled: true
  udp_port: 42424
  beacon_interval_sec: 3
  secret: shared-cluster-secret
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load with discovery: %v", err)
	}
	if cfg.Discovery == nil || !cfg.Discovery.Enabled {
		t.Fatalf("expected discovery config to load, got %#v", cfg.Discovery)
	}
	if cfg.Discovery.UDPPort != 42424 {
		t.Fatalf("expected discovery udp_port 42424, got %d", cfg.Discovery.UDPPort)
	}
}

func TestLoadRejectsInvalidNetworkAndDurationValues(t *testing.T) {
	tests := []struct {
		name      string
		nodeField string
		discovery string
		wantErr   string
	}{
		{name: "negative SSH port", nodeField: "ssh_port: -1", wantErr: "ssh_port must be between 1 and 65535"},
		{name: "oversized SSH port", nodeField: "ssh_port: 65536", wantErr: "ssh_port must be between 1 and 65535"},
		{name: "negative legacy timeout", nodeField: "timeout_sec: -1", wantErr: "timeout_sec cannot be negative"},
		{name: "negative dial timeout", nodeField: "dial_timeout_sec: -1", wantErr: "dial_timeout_sec cannot be negative"},
		{name: "negative collect timeout", nodeField: "collect_timeout_sec: -1", wantErr: "collect_timeout_sec cannot be negative"},
		{name: "overflowing timeout", nodeField: "timeout_sec: 9223372037", wantErr: "timeout_sec exceeds the maximum supported duration"},
		{name: "negative discovery port", discovery: "udp_port: -1", wantErr: "discovery.udp_port must be between 1 and 65535"},
		{name: "oversized discovery port", discovery: "udp_port: 65536", wantErr: "discovery.udp_port must be between 1 and 65535"},
		{name: "negative beacon interval", discovery: "beacon_interval_sec: -1", wantErr: "discovery.beacon_interval_sec cannot be negative"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodeField := ""
			if tt.nodeField != "" {
				nodeField = "    " + tt.nodeField + "\n"
			}
			discovery := ""
			if tt.discovery != "" {
				discovery = "discovery:\n  enabled: true\n  " + tt.discovery + "\n"
			}
			path := filepath.Join(t.TempDir(), "nodes.yaml")
			body := fmt.Sprintf("nodes:\n  - name: node-a\n    hostname: node-a.local\n    ssh_user: user\n%s%s", nodeField, discovery)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}

			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_ValidConfigWithSystemReserveMB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	os.WriteFile(path, []byte(`
nodes:
  - name: node-a
    hostname: node-a.local
    ssh_user: user
    system_reserve_mb: 2048
  - name: node-b
    hostname: node-b.local
    ssh_user: user
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(cfg.Nodes))
	}
	if cfg.Nodes[0].SystemReserveMB != 2048 {
		t.Errorf("node[0].system_reserve_mb: got %d, want 2048", cfg.Nodes[0].SystemReserveMB)
	}
	if cfg.Nodes[1].SystemReserveMB != 0 {
		t.Errorf("node[1].system_reserve_mb: got %d, want 0", cfg.Nodes[1].SystemReserveMB)
	}
}

func TestValidate_EmptyNodes(t *testing.T) {
	cfg := &Config{Nodes: []NodeConfig{}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for empty nodes")
	}
}

func TestValidate_MissingName(t *testing.T) {
	cfg := &Config{Nodes: []NodeConfig{
		{Hostname: "x.local", SSHUser: "u"},
	}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestValidate_MissingHostname(t *testing.T) {
	cfg := &Config{Nodes: []NodeConfig{
		{Name: "n", SSHUser: "u"},
	}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing hostname")
	}
}

func TestValidate_MissingSSHUser(t *testing.T) {
	cfg := &Config{Nodes: []NodeConfig{
		{Name: "n", Hostname: "x.local"},
	}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing ssh_user")
	}
}

func TestEffectiveTimeout_Default(t *testing.T) {
	n := &NodeConfig{}
	if t2 := n.EffectiveTimeout(); t2 != 10 {
		t.Errorf("expected 10, got %d", t2)
	}
}

func TestEffectiveTimeout_Custom(t *testing.T) {
	n := &NodeConfig{TimeoutSec: 30}
	if t2 := n.EffectiveTimeout(); t2 != 30 {
		t.Errorf("expected 30, got %d", t2)
	}
}

func TestEffectiveCollectTimeout_DefaultFloor(t *testing.T) {
	n := &NodeConfig{} // legacy timeout 10
	if got := n.EffectiveCollectTimeout(); got != 45 {
		t.Fatalf("collect timeout = %d, want 45 floor", got)
	}
	n.TimeoutSec = 60
	if got := n.EffectiveCollectTimeout(); got != 60 {
		t.Fatalf("collect timeout = %d, want 60 when legacy higher", got)
	}
	n.CollectTimeoutSec = 12
	if got := n.EffectiveCollectTimeout(); got != 12 {
		t.Fatalf("explicit collect timeout = %d, want 12", got)
	}
}

func TestEffectiveDialTimeout(t *testing.T) {
	n := &NodeConfig{TimeoutSec: 8}
	if got := n.EffectiveDialTimeout(); got != 8 {
		t.Fatalf("dial = %d, want 8 from legacy", got)
	}
	n.DialTimeoutSec = 3
	if got := n.EffectiveDialTimeout(); got != 3 {
		t.Fatalf("dial = %d, want 3 explicit", got)
	}
}

func TestLoadRejectsInvalidAIProviderNumericValues(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{name: "negative priority", body: "ai_providers:\n  local:\n    type: local\n    priority: -1\n", wantErr: "priority must be between 0 and 100"},
		{name: "oversized priority", body: "ai_providers:\n  local:\n    type: local\n    priority: 101\n", wantErr: "priority must be between 0 and 100"},
		{name: "negative model cost", body: "ai_providers:\n  local:\n    type: local\n    models:\n      - name: model-a\n        cost_per_1k: -0.01\n", wantErr: "cost_per_1k must be a finite non-negative value"},
		{name: "NaN model cost", body: "ai_providers:\n  local:\n    type: local\n    models:\n      - name: model-a\n        cost_per_1k: .nan\n", wantErr: "cost_per_1k must be a finite non-negative value"},
		{name: "negative request cap", body: "inference:\n  max_cost_per_request: -0.01\n", wantErr: "max_cost_per_request must be a finite non-negative value"},
		{name: "infinite alert threshold", body: "inference:\n  budget_alert_threshold: .inf\n", wantErr: "budget_alert_threshold must be a finite non-negative value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "nodes.yaml")
			body := "nodes:\n  - name: node-a\n    hostname: node-a.local\n    ssh_user: user\n" + tt.body
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}

			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_AIProvider_UnknownField_Rejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	if err := os.WriteFile(path, []byte(`nodes:
  - name: node-a
    hostname: node-a.local
    ssh_user: user
ai_providers:
  ollama:
    type: local
    unknown_field: oops
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown field in ai_providers entry")
	}
	if !strings.Contains(err.Error(), "unknown_field") {
		t.Logf("error was: %v", err)
		// Not all YAML decoders surface the field name; accept any error.
	}
}

func TestLoad_AIProviderModel_UnknownField_Rejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	if err := os.WriteFile(path, []byte(`nodes:
  - name: node-a
    hostname: node-a.local
    ssh_user: user
ai_providers:
  ollama:
    type: local
    models:
      - name: granite3.1-moe:1b
        unexpected: true
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown field in ai_providers model entry")
	}
}

func TestValidate_NegativeSystemReserveMB(t *testing.T) {
	cfg := &Config{Nodes: []NodeConfig{
		{Name: "n", Hostname: "x.local", SSHUser: "u", SystemReserveMB: -500},
	}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for negative system_reserve_mb")
	} else if !strings.Contains(err.Error(), "system_reserve_mb cannot be negative") {
		t.Fatalf("unexpected error message: %v", err)
	}
}
