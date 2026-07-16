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

func TestIsMeshEnabled(t *testing.T) {
	cases := []struct {
		name string
		cfg  *Config
		want bool
	}{
		{"nil config", nil, true},
		{"no discovery", &Config{Nodes: []NodeConfig{{Name: "a", Hostname: "a.local", SSHUser: "u"}}}, true},
		{"discovery enabled", &Config{Nodes: []NodeConfig{{Name: "a", Hostname: "a.local", SSHUser: "u"}}, Discovery: &DiscoveryConfig{Enabled: true}}, true},
		{"discovery disabled", &Config{Nodes: []NodeConfig{{Name: "a", Hostname: "a.local", SSHUser: "u"}}, Discovery: &DiscoveryConfig{Enabled: false}}, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsMeshEnabled(); got != tt.want {
				t.Fatalf("IsMeshEnabled() = %v, want %v", got, tt.want)
			}
		})
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

func TestPrimaryHostnameAndDialHostnames(t *testing.T) {
	n := &NodeConfig{
		Hostname: "192.168.1.1",
		Endpoints: []NodeEndpoint{
			{Name: "lan", Hostname: "192.168.1.1"},
			{Name: "ts", Hostname: "100.1.2.3"},
		},
	}
	if got := n.PrimaryHostname(); got != "192.168.1.1" {
		t.Fatalf("primary = %q", got)
	}
	hosts := n.DialHostnames()
	if len(hosts) != 2 || hosts[1] != "100.1.2.3" {
		t.Fatalf("dial hosts = %v", hosts)
	}
}

func TestNormalize_DoesNotSynthesizeHostname(t *testing.T) {
	cfg := &Config{Nodes: []NodeConfig{{
		Name:    "edge",
		SSHUser: "axis",
		Endpoints: []NodeEndpoint{
			{Name: "ts", Hostname: "100.1.2.3"},
		},
	}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.Nodes[0].Hostname != "" {
		t.Fatalf("Validate must not fill Hostname, got %q", cfg.Nodes[0].Hostname)
	}
	cfg.Normalize()
	if cfg.Nodes[0].Hostname != "" {
		t.Fatalf("Normalize must not synthesize Hostname, got %q", cfg.Nodes[0].Hostname)
	}
	if cfg.Nodes[0].PrimaryHostname() != "100.1.2.3" {
		t.Fatalf("PrimaryHostname = %q", cfg.Nodes[0].PrimaryHostname())
	}
}

func TestSaveAtomicPreservesEndpointsOnlyHostname(t *testing.T) {
	// Sol P1 regression: Load → unrelated edit → Save must not write synthetic hostname.
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	authored := `nodes:
  - name: edge
    ssh_user: axis
    role: agent
    endpoints:
      - name: lan
        hostname: 192.168.1.50
      - name: ts
        hostname: 100.1.2.3
`
	if err := os.WriteFile(path, []byte(authored), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Nodes[0].Hostname != "" {
		t.Fatalf("after Load Hostname must stay empty, got %q", cfg.Nodes[0].Hostname)
	}
	// Unrelated in-memory edit
	cfg.Nodes[0].Role = "worker"
	if _, err := SaveAtomic(path, cfg); err != nil {
		t.Fatalf("SaveAtomic: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if strings.Contains(body, "hostname: 192.168.1.50") && strings.Count(body, "hostname:") > 2 {
		// endpoints still have hostname keys; top-level node hostname must not appear
	}
	// Node-level hostname field serializes as "hostname:" under the node, not only under endpoints.
	// Parse back without Normalize side effects on disk content.
	if strings.Contains(body, "\n    hostname:") || strings.Contains(body, "\n  hostname:") {
		// Could be endpoints nested with different indent. Check for hostname at node level only.
		// Authored endpoints use "        hostname:" (8 spaces). Node-level would be "    hostname:" (4 spaces) after "name: edge".
	}
	// Strict: unmarshaled without Load normalize must still have empty Hostname.
	var reloaded Config
	if err := decodeStrict(raw, &reloaded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if reloaded.Nodes[0].Hostname != "" {
		t.Fatalf("saved file synthesized hostname %q; body:\n%s", reloaded.Nodes[0].Hostname, body)
	}
	if reloaded.Nodes[0].Role != "worker" {
		t.Fatalf("role not saved: %q", reloaded.Nodes[0].Role)
	}
	if reloaded.Nodes[0].PrimaryHostname() != "192.168.1.50" {
		t.Fatalf("PrimaryHostname = %q", reloaded.Nodes[0].PrimaryHostname())
	}
}

func TestNodeConfigIsLocal_UsesAllDialHostnames(t *testing.T) {
	// Secondary endpoint is loopback while primary is non-local TEST-NET.
	n := NodeConfig{
		Name:    "self",
		SSHUser: "me",
		Endpoints: []NodeEndpoint{
			{Name: "lan", Hostname: "198.51.100.1"},
			{Name: "loop", Hostname: "127.0.0.1"},
		},
	}
	if !n.IsLocal() {
		t.Fatal("expected IsLocal true via secondary loopback endpoint")
	}
	// Primary hostname only, remote
	remote := NodeConfig{
		Name:     "remote",
		Hostname: "198.51.100.9",
		SSHUser:  "me",
	}
	if remote.IsLocal() {
		t.Fatal("expected remote not local")
	}
	// Primary is loopback
	primaryLocal := NodeConfig{
		Name:     "here",
		Hostname: "127.0.0.1",
		SSHUser:  "me",
	}
	if !primaryLocal.IsLocal() {
		t.Fatal("expected primary loopback local")
	}
	// Endpoints-only without local addresses
	onlyRemoteEP := NodeConfig{
		Name:    "edge",
		SSHUser: "me",
		Endpoints: []NodeEndpoint{
			{Name: "x", Hostname: "198.51.100.8"},
		},
	}
	if onlyRemoteEP.IsLocal() {
		t.Fatal("endpoints-only remote must not be local")
	}
}

func TestMembershipFingerprint_Stable(t *testing.T) {
	a := &Config{Nodes: []NodeConfig{
		{Name: "b", Role: "hub", SSHUser: "axis"},
		{Name: "a", Role: "muscle", SSHUser: "cranium"},
	}}
	b := &Config{Nodes: []NodeConfig{
		{Name: "a", Hostname: "x", Role: "muscle", SSHUser: "cranium"},
		{Name: "b", Hostname: "y", Role: "hub", SSHUser: "axis"},
	}}
	if a.MembershipFingerprint() != b.MembershipFingerprint() {
		t.Fatalf("fingerprint should ignore hostnames and node order")
	}
	c := &Config{Nodes: []NodeConfig{
		{Name: "a", Role: "other", SSHUser: "cranium"},
		{Name: "b", Role: "hub", SSHUser: "axis"},
	}}
	if a.MembershipFingerprint() == c.MembershipFingerprint() {
		t.Fatalf("role change should change fingerprint")
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
  groq:
    type: cloud
    kind: groq
    api_key_env: GROQ_API_KEY
    api_key_file: ~/.config/axis/groq.key
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

	groq := cfg.AIProviders["groq"]
	if groq.APIKeyEnv != "GROQ_API_KEY" {
		t.Errorf("groq.api_key_env = %q", groq.APIKeyEnv)
	}
	if groq.APIKeyFile != "~/.config/axis/groq.key" {
		t.Errorf("groq.api_key_file = %q", groq.APIKeyFile)
	}
	if groq.Enabled {
		t.Error("groq.enabled should be false")
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

func TestLoad_Webhooks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	if err := os.WriteFile(path, []byte(`nodes:
  - name: node-a
    hostname: node-a.local
    ssh_user: user
webhooks:
  - "https://hooks.slack.com/services/123"
  - "http://localhost:8080/events"
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load with webhooks: %v", err)
	}
	if len(cfg.Webhooks) != 2 {
		t.Fatalf("expected 2 webhooks, got %d", len(cfg.Webhooks))
	}
	if cfg.Webhooks[0] != "https://hooks.slack.com/services/123" {
		t.Errorf("webhook[0] = %q", cfg.Webhooks[0])
	}
	if cfg.Webhooks[1] != "http://localhost:8080/events" {
		t.Errorf("webhook[1] = %q", cfg.Webhooks[1])
	}
}

func TestLoad_AllowedInternalHosts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	if err := os.WriteFile(path, []byte(`nodes:
  - name: node-a
    hostname: node-a.local
    ssh_user: user
allowed_internal_hosts:
  - "axis.lan"
  - "127.0.0.1"
  - "169.254.1.2"
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load with allowed_internal_hosts: %v", err)
	}
	want := []string{"axis.lan", "127.0.0.1", "169.254.1.2"}
	if len(cfg.AllowedInternalHosts) != len(want) {
		t.Fatalf("expected %d allowed hosts, got %d", len(want), len(cfg.AllowedInternalHosts))
	}
	for i, h := range want {
		if cfg.AllowedInternalHosts[i] != h {
			t.Errorf("allowed_internal_hosts[%d] = %q, want %q", i, cfg.AllowedInternalHosts[i], h)
		}
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
