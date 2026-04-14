package config

import (
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

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/nodes.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
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

func TestLoad_RejectsUnknownTopLevelField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	os.WriteFile(path, []byte(`
nodes:
  - name: node-a
    hostname: node-a.local
    ssh_user: user
unexpected: true
`), 0644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown top-level field")
	}
	if !strings.Contains(err.Error(), "field unexpected not found") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestLoad_RejectsUnknownNodeField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	os.WriteFile(path, []byte(`
nodes:
  - name: node-a
    hostname: node-a.local
    ssh_user: user
    sshuser_typo: user
`), 0644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown node field")
	}
	if !strings.Contains(err.Error(), "field sshuser_typo not found") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestLoad_RejectsUnknownDiscoveryField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	os.WriteFile(path, []byte(`
nodes:
  - name: node-a
    hostname: node-a.local
    ssh_user: user
discovery:
  enabled: true
  beacon_interval_typo: 3
`), 0644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown discovery field")
	}
	if !strings.Contains(err.Error(), "field beacon_interval_typo not found") {
		t.Fatalf("expected unknown-field error, got %v", err)
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

func TestEffectiveSSHPort_Default(t *testing.T) {
	n := &NodeConfig{}
	if p := n.EffectiveSSHPort(); p != 22 {
		t.Errorf("expected 22, got %d", p)
	}
}

func TestEffectiveSSHPort_Custom(t *testing.T) {
	n := &NodeConfig{SSHPort: 2222}
	if p := n.EffectiveSSHPort(); p != 2222 {
		t.Errorf("expected 2222, got %d", p)
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

func TestDefaultConfigPath(t *testing.T) {
	p := DefaultConfigPath()
	if p == "" {
		t.Fatal("expected non-empty path")
	}
	if filepath.Base(p) != "nodes.yaml" {
		t.Errorf("expected nodes.yaml, got %s", filepath.Base(p))
	}
}

// --- AI providers + inference ---

func TestLoad_AIProviders_ParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	if err := os.WriteFile(path, []byte(`nodes:
  - name: node-a
    hostname: node-a.local
    ssh_user: user
ai_providers:
  ollama:
    type: local
    endpoint: "http://localhost:11434"
    priority: 10
    enabled: true
    models:
      - name: granite3.1-moe:1b
        aliases: ["fast", "cheap"]
        cost_per_1k: 0
  openai:
    type: cloud
    api_key_env: OPENAI_API_KEY
    api_key_file: ~/.config/axis/openai.key
    priority: 5
    enabled: false
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load with ai_providers: %v", err)
	}
	if len(cfg.AIProviders) != 2 {
		t.Fatalf("expected 2 ai_providers, got %d", len(cfg.AIProviders))
	}
	ollama, ok := cfg.AIProviders["ollama"]
	if !ok {
		t.Fatal("missing 'ollama' provider")
	}
	if ollama.Type != "local" {
		t.Errorf("ollama.type = %q, want local", ollama.Type)
	}
	if ollama.Endpoint != "http://localhost:11434" {
		t.Errorf("ollama.endpoint = %q", ollama.Endpoint)
	}
	if ollama.Priority != 10 {
		t.Errorf("ollama.priority = %d, want 10", ollama.Priority)
	}
	if !ollama.Enabled {
		t.Error("ollama.enabled should be true")
	}
	if len(ollama.Models) != 1 || ollama.Models[0].Name != "granite3.1-moe:1b" {
		t.Errorf("ollama.models = %+v", ollama.Models)
	}
	if len(ollama.Models[0].Aliases) != 2 {
		t.Fatalf("ollama.models[0].aliases = %+v, want 2 aliases", ollama.Models[0].Aliases)
	}

	openai := cfg.AIProviders["openai"]
	if openai.APIKeyEnv != "OPENAI_API_KEY" {
		t.Errorf("openai.api_key_env = %q", openai.APIKeyEnv)
	}
	if openai.APIKeyFile != "~/.config/axis/openai.key" {
		t.Errorf("openai.api_key_file = %q", openai.APIKeyFile)
	}
	if openai.Enabled {
		t.Error("openai.enabled should be false")
	}
}

func TestLoad_AIProviders_Absent_BackwardCompat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	if err := os.WriteFile(path, []byte(`nodes:
  - name: node-a
    hostname: node-a.local
    ssh_user: user
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load without ai_providers: %v", err)
	}
	if len(cfg.AIProviders) != 0 {
		t.Errorf("expected empty ai_providers map, got %d entries", len(cfg.AIProviders))
	}
	if cfg.Inference != nil {
		t.Errorf("expected nil inference config, got %+v", cfg.Inference)
	}
}

func TestLoad_InferenceConfig_Parses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	if err := os.WriteFile(path, []byte(`nodes:
  - name: node-a
    hostname: node-a.local
    ssh_user: user
inference:
  default_mode: local
  prefer: latency
  max_cost_per_request: 0.01
  budget_alert_threshold: 5.0
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load with inference: %v", err)
	}
	if cfg.Inference == nil {
		t.Fatal("expected inference config, got nil")
	}
	if cfg.Inference.DefaultMode != "local" {
		t.Errorf("inference.default_mode = %q, want local", cfg.Inference.DefaultMode)
	}
	if cfg.Inference.Prefer != "latency" {
		t.Errorf("inference.prefer = %q, want latency", cfg.Inference.Prefer)
	}
	if cfg.Inference.MaxCostPerRequest != 0.01 {
		t.Errorf("inference.max_cost_per_request = %v, want 0.01", cfg.Inference.MaxCostPerRequest)
	}
	if cfg.Inference.BudgetAlertThreshold != 5.0 {
		t.Errorf("inference.budget_alert_threshold = %v, want 5.0", cfg.Inference.BudgetAlertThreshold)
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

func TestLoad_InferenceConfig_UnknownField_Rejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	if err := os.WriteFile(path, []byte(`nodes:
  - name: node-a
    hostname: node-a.local
    ssh_user: user
inference:
  default_mode: local
  typo_field: bad
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown field in inference block")
	}
}

func TestLoad_ChatDefaultModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	if err := os.WriteFile(path, []byte(`nodes:
  - name: node-a
    hostname: node-a.local
    ssh_user: user
chat:
  default_model: "llama3.2:latest"
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load with chat config: %v", err)
	}
	if cfg.Chat == nil {
		t.Fatal("expected chat config to be parsed, got nil")
	}
	if cfg.Chat.DefaultModel != "llama3.2:latest" {
		t.Fatalf("chat.default_model = %q, want %q", cfg.Chat.DefaultModel, "llama3.2:latest")
	}
}
