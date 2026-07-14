package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
)

func TestInitCmdFirstTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".axis", "nodes.yaml")
	deps := testInitDeps()
	input := "my-local-node\nmy-ssh-user\n\n\n\n"
	out := executeInit(t, path, input, deps)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load generated config: %v\noutput:\n%s", err, out)
	}
	if len(cfg.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(cfg.Nodes))
	}
	local := cfg.Nodes[0]
	if local.Name != "my-local-node" || local.Hostname != "localhost" || local.SSHUser != "my-ssh-user" || local.Role != "primary" {
		t.Fatalf("unexpected local node: %+v", local)
	}
	if cfg.Discovery == nil || cfg.Discovery.Enabled {
		t.Fatalf("discovery should be explicitly disabled: %+v", cfg.Discovery)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("mode = %o, want 600", got)
	}
	if !strings.Contains(out, "First-time setup") || !strings.Contains(out, "Configuration saved") {
		t.Fatalf("missing onboarding output:\n%s", out)
	}
}

func TestInitCmdFirstTimeAddsDiscoveredTailscalePeer(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".axis", "nodes.yaml")
	deps := testInitDeps()
	deps.discoverTailscale = func(context.Context) ([]config.NodeConfig, error) {
		return []config.NodeConfig{{Name: "worker.local", Hostname: "100.64.0.2", SSHPort: 22, TimeoutSec: 10}}, nil
	}
	input := strings.Join([]string{
		"local-node",
		"operator",
		"2",
		"",
		"n",
		"4",
		"",
		"",
		"",
		"",
		"",
		"",
	}, "\n")
	out := executeInit(t, path, input, deps)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load generated config: %v\noutput:\n%s", err, out)
	}
	if len(cfg.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2\noutput:\n%s", len(cfg.Nodes), out)
	}
	if cfg.Nodes[1].Name != "worker.local" || cfg.Nodes[1].Hostname != "100.64.0.2" || cfg.Nodes[1].SSHUser != "operator" {
		t.Fatalf("unexpected discovered node: %+v", cfg.Nodes[1])
	}
	if cfg.Discovery == nil || !cfg.Discovery.Enabled || cfg.Discovery.Secret == "" {
		t.Fatalf("discovery not enabled for multi-node setup: %+v", cfg.Discovery)
	}
}

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

func TestInitCmdUpdatesExistingAndPreservesOptionalSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.yaml")
	original := &config.Config{
		Nodes:     []config.NodeConfig{{Name: "local", Hostname: "localhost", SSHUser: "operator", Role: "primary", TimeoutSec: 10}},
		Discovery: &config.DiscoveryConfig{Enabled: false},
		Chat:      &config.ChatConfig{DefaultModel: "test-model"},
		Webhooks:  []string{"https://example.com/hook"},
	}
	if _, err := config.SaveAtomic(path, original); err != nil {
		t.Fatal(err)
	}

	deps := testInitDeps()
	deps.verifySSH = func(context.Context, string, int, string, int, io.Writer) bool { return true }
	input := strings.Join([]string{
		"",
		"1",
		"1",
		"remote-worker",
		"192.168.1.50",
		"",
		"70000",
		"2222",
		"15",
		"",
		"4",
		"5",
		"",
	}, "\n") + "\n"
	out := executeInit(t, path, input, deps)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2\noutput:\n%s", len(cfg.Nodes), out)
	}
	remote := cfg.Nodes[1]
	if remote.Name != "remote-worker" || remote.Hostname != "192.168.1.50" || remote.SSHPort != 2222 || remote.TimeoutSec != 15 {
		t.Fatalf("unexpected remote node: %+v", remote)
	}
	if cfg.Chat == nil || cfg.Chat.DefaultModel != "test-model" || len(cfg.Webhooks) != 1 {
		t.Fatalf("optional sections were not preserved: %+v", cfg)
	}
	if !strings.Contains(out, "enter a value from 1 to 65535") {
		t.Fatalf("invalid port was not rejected:\n%s", out)
	}
	backups, err := filepath.Glob(path + ".bak-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want one", backups)
	}
}

func TestInitCmdEditsExistingNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.yaml")
	original := twoNodeConfig()
	if _, err := config.SaveAtomic(path, original); err != nil {
		t.Fatal(err)
	}

	input := strings.Join([]string{
		"",
		"2",
		"2",
		"remote-renamed",
		"10.0.0.3",
		"",
		"",
		"",
		"20",
		"n",
		"5",
		"",
		"",
		"",
	}, "\n") + "\n"
	out := executeInit(t, path, input, testInitDeps())

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load edited config: %v\noutput:\n%s", err, out)
	}
	if len(cfg.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(cfg.Nodes))
	}
	remote := cfg.Nodes[1]
	if remote.Name != "remote-renamed" || remote.Hostname != "10.0.0.3" || remote.TimeoutSec != 20 || remote.Role != "worker" {
		t.Fatalf("unexpected edited node: %+v", remote)
	}
}

func TestInitCmdRemovesExistingNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.yaml")
	if _, err := config.SaveAtomic(path, twoNodeConfig()); err != nil {
		t.Fatal(err)
	}

	input := strings.Join([]string{
		"",
		"3",
		"2",
		"y",
		"5",
		"",
		"",
		"",
	}, "\n") + "\n"
	out := executeInit(t, path, input, testInitDeps())

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load edited config: %v\noutput:\n%s", err, out)
	}
	if len(cfg.Nodes) != 1 || cfg.Nodes[0].Name != "local" {
		t.Fatalf("unexpected nodes after remove: %+v", cfg.Nodes)
	}
}

func TestInitCmdEnablesDiscoveryOnExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.yaml")
	original := &config.Config{
		Nodes:     []config.NodeConfig{{Name: "local", Hostname: "localhost", SSHUser: "operator", Role: "primary", TimeoutSec: 10}},
		Discovery: &config.DiscoveryConfig{Enabled: false},
	}
	if _, err := config.SaveAtomic(path, original); err != nil {
		t.Fatal(err)
	}

	input := strings.Join([]string{
		"",
		"4",
		"y",
		"42425",
		"5",
		"5",
		"",
		"",
		"",
	}, "\n") + "\n"
	out := executeInit(t, path, input, testInitDeps())

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load edited config: %v\noutput:\n%s", err, out)
	}
	if cfg.Discovery == nil || !cfg.Discovery.Enabled || cfg.Discovery.UDPPort != 42425 || cfg.Discovery.BeaconInterval != 5 || cfg.Discovery.Secret == "" {
		t.Fatalf("unexpected discovery config: %+v", cfg.Discovery)
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

func TestPrepareInitConfigReplacePreservesPromptErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.yaml")
	if _, err := config.SaveAtomic(path, twoNodeConfig()); err != nil {
		t.Fatal(err)
	}
	prompt := testPrompter(t, "2\n")
	defer prompt.Close()

	_, err := prepareInitConfig(context.Background(), prompt, path, testInitDeps())
	if !errors.Is(err, errInitCanceled) {
		t.Fatalf("err = %v, want cancellation", err)
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
	if duplicateHostname(cfg, "10.0.0.2", -1) != true {
		t.Fatal("expected duplicate hostname")
	}
	if duplicateHostname(cfg, "10.0.0.2", 1) != false {
		t.Fatal("expected duplicate hostname exception")
	}
	if got := defaultNodeUser(&config.Config{}, "fallback"); got != "fallback" {
		t.Fatalf("defaultNodeUser fallback = %q", got)
	}

	clone := cloneConfig(cfg)
	clone.Nodes[0].Name = "changed"
	clone.Webhooks = append(clone.Webhooks, "new")
	if cfg.Nodes[0].Name == "changed" || len(cfg.Webhooks) != 0 {
		t.Fatal("cloneConfig did not isolate slices")
	}

	optional := &config.Config{
		Chat:                 &config.ChatConfig{DefaultModel: "m"},
		AIProviders:          map[string]config.AIProviderConfig{"p": {Type: "local"}},
		Inference:            &config.InferenceConfig{DefaultMode: "local"},
		MCPServers:           map[string]config.MCPServerConfig{"mcp": {Transport: "stdio"}},
		Webhooks:             []string{"https://example.com"},
		AllowedInternalHosts: []string{"127.0.0.1"},
	}
	target := &config.Config{}
	preserveOptionalConfig(target, optional)
	if target.Chat == nil || target.AIProviders == nil || target.Inference == nil || target.MCPServers == nil || len(target.Webhooks) != 1 || len(target.AllowedInternalHosts) != 1 {
		t.Fatalf("optional config not preserved: %+v", target)
	}
}

func TestPromptHelpersRejectInvalidInputThenAccept(t *testing.T) {
	prompt := testPrompter(t, "bad\ny\nabc\n7\n3\n")
	defer prompt.Close()

	ok, err := prompt.Confirm("Confirm", false)
	if err != nil || !ok {
		t.Fatalf("Confirm = %v, %v", ok, err)
	}
	n, err := prompt.Int("Number", 5, 1, 10)
	if err != nil || n != 7 {
		t.Fatalf("Int = %d, %v", n, err)
	}
	choice, err := prompt.Choose(context.Background(), "Choice", []uiOption(), "b")
	if err != nil || choice != "c" {
		t.Fatalf("Choose = %q, %v", choice, err)
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

func testPrompter(t *testing.T, input string) *initPrompter {
	t.Helper()
	cmd := initCmd()
	cmd.SetIn(bytes.NewBufferString(input))
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	prompt, err := newInitPrompter(cmd)
	if err != nil {
		t.Fatal(err)
	}
	return prompt
}

func uiOption() []ui.SelectOption {
	return []ui.SelectOption{
		{ID: "a", Label: "A"},
		{ID: "b", Label: "B", Disabled: true},
		{ID: "c", Label: "C"},
	}
}

func testInitDeps() initDependencies {
	return initDependencies{
		hostname:    func() (string, error) { return "test-host.local", nil },
		defaultUser: func() string { return "operator" },
		loadConfig:  config.Load,
		saveConfig:  config.SaveAtomic,
		verifySSH:   func(context.Context, string, int, string, int, io.Writer) bool { return true },
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
