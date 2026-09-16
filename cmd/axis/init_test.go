package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

func TestInitCmdExistingConfigNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.yaml")
	original := &config.Config{
		Nodes:     []config.NodeConfig{{Name: "local", Hostname: "localhost", SSHUser: "operator", Role: "primary", TimeoutSec: 10}},
		Discovery: &config.DiscoveryConfig{Enabled: false},
	}
	if _, err := config.SaveAtomic(path, original); err != nil {
		t.Fatal(err)
	}

	out := executeInit(t, path, "\n\n\n", testInitDeps())
	if !strings.Contains(out, "Configuration already matches") {
		t.Fatalf("expected idempotent result:\n%s", out)
	}
	backups, err := filepath.Glob(path + ".bak-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("no-op created backups: %v", backups)
	}
}

func TestInitCmdInvalidExistingConfigRequiresExplicitReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.yaml")
	original := []byte("nodes: [not valid\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}

	out := executeInit(t, path, "n\n", testInitDeps())
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("invalid config was mutated without consent: %q", got)
	}
	if !strings.Contains(out, "No changes written") {
		t.Fatalf("missing cancellation output:\n%s", out)
	}
}

func TestInitValidationAndUtilityHelpers(t *testing.T) {
	for _, value := range []string{"node-a", "Node_2", "m1.local"} {
		if err := validateNodeName(value); err != nil {
			t.Errorf("validateNodeName(%q): %v", value, err)
		}
	}
	for _, value := range []string{"", "two words", "node/a"} {
		if err := validateNodeName(value); err == nil {
			t.Errorf("validateNodeName(%q) unexpectedly succeeded", value)
		}
	}
	if err := validateHostname("https://node.local"); err == nil {
		t.Fatal("URL should not be accepted as hostname")
	}
	if err := validateHostname("two words"); err == nil {
		t.Fatal("hostname with whitespace should be rejected")
	}
	if err := validateSSHUser("two users"); err == nil {
		t.Fatal("SSH user with whitespace should be rejected")
	}
	if got := normalizeSuggestedName("weird host.local"); got != "weird-host" {
		t.Fatalf("normalizeSuggestedName = %q", got)
	}
	if got := normalizeSuggestedName("...bad..."); got != "bad" {
		t.Fatalf("normalizeSuggestedName trim = %q", got)
	}
	if got := normalizedRole("PRIMARY"); got != "primary" {
		t.Fatalf("normalizedRole primary = %q", got)
	}
	if got := normalizedRole("other"); got != "worker" {
		t.Fatalf("normalizedRole fallback = %q", got)
	}
	if got := enabledLabel(true); got != "enabled" {
		t.Fatalf("enabledLabel(true) = %q", got)
	}
	if got := enabledLabel(false); got != "disabled" {
		t.Fatalf("enabledLabel(false) = %q", got)
	}

	cfg := twoNodeConfig()
	if idx, found := findNodeIndex(cfg, "REMOTE"); !found || idx != 1 {
		t.Fatalf("findNodeIndex = %d, %v", idx, found)
	}
	if duplicateHostPort(cfg, "10.0.0.2", 22, -1) != true {
		t.Fatal("expected duplicate host:port")
	}
	if duplicateHostPort(cfg, "10.0.0.2", 22, 1) != false {
		t.Fatal("expected duplicate host:port exception")
	}
	if duplicateHostPort(cfg, "10.0.0.2", 2222, -1) != false {
		t.Fatal("same host different port should be allowed")
	}
	if got := defaultNodeUser(&config.Config{}, "fallback"); got != "fallback" {
		t.Fatalf("defaultNodeUser fallback = %q", got)
	}

	cfg.AIProviders = map[string]config.AIProviderConfig{
		"p": {Type: "local", Models: []config.AIModelConfig{{Name: "m1"}}},
	}
	cfg.MCPServers = map[string]config.MCPServerConfig{
		"mcp": {Transport: "stdio", Command: []string{"cmd"}, Headers: map[string]string{"A": "1"}},
	}
	clone := cloneConfig(cfg)
	clone.Nodes[0].Name = "changed"
	clone.Webhooks = append(clone.Webhooks, "new")
	clone.AIProviders["p"] = config.AIProviderConfig{Type: "cloud"}
	clone.AIProviders["extra"] = config.AIProviderConfig{Type: "local"}
	mcp := clone.MCPServers["mcp"]
	mcp.Command[0] = "changed"
	mcp.Headers["A"] = "2"
	clone.MCPServers["mcp"] = mcp
	if cfg.Nodes[0].Name == "changed" || len(cfg.Webhooks) != 0 {
		t.Fatal("cloneConfig did not isolate slices")
	}
	if cfg.AIProviders["p"].Type != "local" || len(cfg.AIProviders) != 1 {
		t.Fatal("cloneConfig did not isolate AIProviders map")
	}
	if cfg.MCPServers["mcp"].Command[0] != "cmd" || cfg.MCPServers["mcp"].Headers["A"] != "1" {
		t.Fatal("cloneConfig did not isolate MCPServers nested data")
	}

	optional := &config.Config{
		Agent:                &config.AgentConfig{DefaultModel: "m"},
		Chat:                 &config.ChatConfig{DefaultModel: "legacy"},
		AIProviders:          map[string]config.AIProviderConfig{"p": {Type: "local"}},
		Inference:            &config.InferenceConfig{DefaultMode: "local"},
		MCPServers:           map[string]config.MCPServerConfig{"mcp": {Transport: "stdio"}},
		Webhooks:             []string{"https://example.com"},
		AllowedInternalHosts: []string{"127.0.0.1"},
	}
	target := &config.Config{}
	preserveOptionalConfig(target, optional)
	if target.Agent == nil || target.Chat == nil || target.AIProviders == nil || target.Inference == nil || target.MCPServers == nil || len(target.Webhooks) != 1 || len(target.AllowedInternalHosts) != 1 {
		t.Fatalf("optional config not preserved: %+v", target)
	}
}

func executeInit(t *testing.T, path, input string, deps initDependencies) string {
	t.Helper()
	out := new(bytes.Buffer)
	cmd := initCmd()
	if err := cmd.Flags().Set("config", path); err != nil {
		t.Fatal(err)
	}
	cmd.SetIn(bytes.NewBufferString(input))
	cmd.SetOut(out)
	cmd.SetErr(out)
	if err := runInitWizardWithDeps(cmd, deps); err != nil {
		t.Fatalf("runInitWizardWithDeps: %v\noutput:\n%s", err, out.String())
	}
	return out.String()
}

func testInitDeps() initDependencies {
	return initDependencies{
		hostname:      func() (string, error) { return "test-host.local", nil },
		defaultUser:   func() string { return "operator" },
		localIdentity: func(context.Context) *models.NodeIdentity { return models.NewNodeIdentity("test-stable-id", "test") },
		loadConfig:    config.Load,
		saveConfig:    config.SaveAtomic,
		verifySSH:     func(context.Context, string, int, string, int, io.Writer) bool { return true },
		discoverTailscale: func(context.Context) ([]config.NodeConfig, error) {
			return nil, nil
		},
		discoverMesh: func(context.Context) ([]config.NodeConfig, error) {
			return nil, nil
		},
	}
}

func twoNodeConfig() *config.Config {
	return &config.Config{
		Nodes: []config.NodeConfig{
			{Name: "local", Hostname: "localhost", SSHUser: "operator", Role: "primary", TimeoutSec: 10},
			{Name: "remote", Hostname: "10.0.0.2", SSHUser: "operator", Role: "worker", SSHPort: 22, TimeoutSec: 10},
		},
		Discovery: &config.DiscoveryConfig{Enabled: false},
	}
}
