package main

import (
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func TestFindModelTargetByRef(t *testing.T) {
	choices := []ModelChoice{
		{ID: "a:ollama:qwen", Model: "qwen", Protocol: agent.ProtocolOllama, Endpoint: "http://a:11434"},
		{ID: "b:ollama:qwen", Model: "qwen", Protocol: agent.ProtocolOllama, Endpoint: "http://b:11434"},
		{ID: "a:ollama:phi", Model: "phi", Protocol: agent.ProtocolOllama, Endpoint: "http://a:11434"},
	}
	if _, err := findModelTargetByRef(choices, "qwen"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous, got %v", err)
	}
	got, err := findModelTargetByRef(choices, "a:ollama:qwen")
	if err != nil || got.Endpoint != "http://a:11434" {
		t.Fatalf("id match: got %+v err=%v", got, err)
	}
	got, err = findModelTargetByRef(choices, "phi")
	if err != nil || got.ID != "a:ollama:phi" {
		t.Fatalf("unique name: got %+v err=%v", got, err)
	}
	if _, err := findModelTargetByRef(choices, "missing"); err == nil {
		t.Fatal("expected missing error")
	}
}

func TestResolveStartupPrefersLocalOverCloudAuto(t *testing.T) {
	choices := []ModelChoice{
		{
			ID: "local:ollama:qwen", Model: "qwen", Protocol: agent.ProtocolOllama,
			ProviderName: "ollama", ProviderKind: "local", Endpoint: chat.DefaultEndpoint,
		},
		{
			ID: "cloud:groq:fast", Model: "fast", Protocol: agent.ProtocolCloud,
			ProviderName: "groq", ProviderKind: "cloud",
		},
	}
	// Without keys, cloud opts fail if cloud chosen — local should win first
	target, _, err := resolveStartupModelTarget("", "auto", "", nil, nil, choices)
	if err != nil {
		t.Fatal(err)
	}
	if target.Protocol != agent.ProtocolOllama || target.Model != "qwen" {
		t.Fatalf("got %+v", target)
	}
}

func TestResolveStartupExplicitLocalModelUsesEndpoint(t *testing.T) {
	choices := []ModelChoice{
		{
			ID: "node1:ollama:qwen", Model: "qwen", Protocol: agent.ProtocolOllama,
			ProviderName: "ollama", ProviderKind: "local", Node: "node1",
			Endpoint: "http://10.0.0.9:11434", SecurityClass: agent.BackendRemote,
		},
	}
	target, _, err := resolveStartupModelTarget("qwen", "auto", "", nil, nil, choices)
	if err != nil {
		t.Fatal(err)
	}
	if target.Endpoint != "http://10.0.0.9:11434" {
		t.Fatalf("endpoint = %q", target.Endpoint)
	}
	backend, err := agent.BuildBackend(target, agent.CloudBackendOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := backend.(*chat.Client)
	if c.Endpoint != "http://10.0.0.9:11434" {
		t.Fatalf("client endpoint = %q", c.Endpoint)
	}
}

func TestResolveStartupExplicitSelectionWins(t *testing.T) {
	explicit := ModelChoice{
		ID: "remote:ollama:x", Model: "x", Protocol: agent.ProtocolOllama,
		ProviderName: "ollama", ProviderKind: "local", Node: "remote",
		Endpoint: "http://1.2.3.4:11434",
	}
	choices := []ModelChoice{explicit, {
		ID: "cloud:p:m", Model: "m", Protocol: agent.ProtocolCloud, ProviderName: "p", ProviderKind: "cloud",
	}}
	target, _, err := resolveStartupModelTarget("", "auto", "", &explicit, nil, choices)
	if err != nil {
		t.Fatal(err)
	}
	if target.Endpoint != "http://1.2.3.4:11434" {
		t.Fatalf("got %+v", target)
	}
}

func TestCollectModelChoicesSetsProtocols(t *testing.T) {
	// Avoid network probes for remotes by using local-only nodes
	rt := &runtimectx.Context{
		Snapshot: &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{
				{
					Name:   "localhost",
					Status: models.StatusComplete,
					Ollama: &models.OllamaInfo{Installed: true, Port: 11434, Models: []string{"local-a"}},
					ResidentModels: []models.ResidentModel{
						{Name: "mlx-m", Runtime: "mlx", Port: 8080},
					},
				},
			},
		},
		Config: &config.Config{
			AIProviders: map[string]config.AIProviderConfig{
				"groq": {Enabled: true, Type: "cloud", Kind: "groq", Models: []config.AIModelConfig{{Name: "llama-cloud"}}},
			},
		},
	}
	// Mark node as local by empty hostname matching - IsLocalNode checks stable id etc.
	// Force local via hostname localhost which IsLocalNode may accept
	choices := collectModelChoices(rt)
	var sawOllama, sawOpenAI, sawCloud bool
	var cloudDisabled bool
	for _, c := range choices {
		switch c.Protocol {
		case agent.ProtocolOllama:
			sawOllama = true
		case agent.ProtocolOpenAI:
			sawOpenAI = true
		case agent.ProtocolCloud:
			sawCloud = true
			cloudDisabled = c.Disabled
		}
	}
	if !sawOllama || !sawOpenAI || !sawCloud {
		t.Fatalf("protocols ollama=%v openai=%v cloud=%v choices=%+v", sawOllama, sawOpenAI, sawCloud, choices)
	}
	// No API key configured → cloud entry must be disabled in the catalog.
	if !cloudDisabled {
		t.Fatal("expected cloud choice without API key to be disabled")
	}
	_ = context.Background()
}

func TestEffectiveStartupRequestedModel(t *testing.T) {
	t.Run("flag wins", func(t *testing.T) {
		rt := &runtimectx.Context{Config: &config.Config{Chat: &config.ChatConfig{DefaultModel: "from-config"}}}
		if got := effectiveStartupRequestedModel("from-flag", rt); got != "from-flag" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("config default when flag empty", func(t *testing.T) {
		rt := &runtimectx.Context{Config: &config.Config{Chat: &config.ChatConfig{DefaultModel: "from-config"}}}
		if got := effectiveStartupRequestedModel("", rt); got != "from-config" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("warm preferred when no config default", func(t *testing.T) {
		rt := &runtimectx.Context{
			Snapshot: &models.ClusterSnapshot{
				Nodes: []models.NodeFacts{
					{Name: "n1", ResidentModels: []models.ResidentModel{{Name: "llama3.2:latest", Runtime: "ollama"}}},
				},
			},
			Config: &config.Config{},
		}
		got := effectiveStartupRequestedModel("", rt)
		if got == "" {
			t.Fatal("expected preferred model from resident list")
		}
	})
	t.Run("empty when nothing configured", func(t *testing.T) {
		if got := effectiveStartupRequestedModel("", &runtimectx.Context{}); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
}

func TestResolveStartupHonorsDefaultModelOverFirstLocal(t *testing.T) {
	choices := []ModelChoice{
		{
			ID: "local:ollama:aaa-first", Model: "aaa-first", Protocol: agent.ProtocolOllama,
			ProviderName: "ollama", ProviderKind: "local", Endpoint: chat.DefaultEndpoint,
		},
		{
			ID: "local:ollama:configured-default", Model: "configured-default", Protocol: agent.ProtocolOllama,
			ProviderName: "ollama", ProviderKind: "local", Endpoint: chat.DefaultEndpoint,
		},
	}
	// Pass effective requested model as resolveStartup receives after effectiveStartupRequestedModel.
	target, _, err := resolveStartupModelTarget("configured-default", "auto", "", nil, nil, choices)
	if err != nil {
		t.Fatal(err)
	}
	if target.Model != "configured-default" {
		t.Fatalf("got model %q, want configured-default (not alpha-first catalog entry)", target.Model)
	}
}

func TestResolveStartupCloudPriorityAndCredentials(t *testing.T) {
	t.Setenv("AXIS_TEST_CLOUD_PRI_HIGH", "high-key")
	// Alpha-earlier provider has NO key and priority 1; later has key and priority 100.
	rt := &runtimectx.Context{
		Config: &config.Config{
			AIProviders: map[string]config.AIProviderConfig{
				"aaa-no-key": {
					Enabled: true, Type: "cloud", Kind: "openrouter", Priority: 1,
					APIKeyEnv: "AXIS_TEST_CLOUD_PRI_MISSING",
					Models:    []config.AIModelConfig{{Name: "cheap-a", CostPer1K: 0.001}},
				},
				"zzz-has-key": {
					Enabled: true, Type: "cloud", Kind: "groq", Priority: 100,
					APIKeyEnv: "AXIS_TEST_CLOUD_PRI_HIGH",
					Models: []config.AIModelConfig{
						{Name: "expensive", CostPer1K: 0.05},
						{Name: "cheap-z", CostPer1K: 0.002},
					},
				},
			},
		},
	}

	t.Run("auto cloud fallback uses priority provider and cheapest model", func(t *testing.T) {
		// No local choices → cloud path
		target, opts, err := resolveStartupModelTarget("", "auto", "", nil, rt, nil)
		if err != nil {
			t.Fatal(err)
		}
		if target.ProviderName != "zzz-has-key" {
			t.Fatalf("provider = %q, want zzz-has-key (priority + credentials)", target.ProviderName)
		}
		if target.Model != "cheap-z" {
			t.Fatalf("model = %q, want cheap-z (lowest cost on selected provider)", target.Model)
		}
		if opts.APIKey != "high-key" {
			t.Fatalf("api key = %q", opts.APIKey)
		}
		if opts.CostPer1K != 0.002 {
			t.Fatalf("cost = %v, want 0.002", opts.CostPer1K)
		}
	})

	t.Run("provider cloud same selection without explicit model", func(t *testing.T) {
		target, _, err := resolveStartupModelTarget("", "cloud", "", nil, rt, nil)
		if err != nil {
			t.Fatal(err)
		}
		if target.ProviderName != "zzz-has-key" || target.Model != "cheap-z" {
			t.Fatalf("got %+v", target)
		}
	})

	t.Run("provider cloud ignores local default_model name", func(t *testing.T) {
		// requestedModel may carry chat.default_model; it must not be treated as a cloud model.
		target, _, err := resolveStartupModelTarget("some-local-default", "cloud", "", nil, rt, nil)
		if err != nil {
			t.Fatal(err)
		}
		if target.Model != "cheap-z" {
			t.Fatalf("got model %q, want cheap-z (not local default name)", target.Model)
		}
	})

	t.Run("skips keyless alpha-first provider", func(t *testing.T) {
		providers := listCredentialedCloudProviders(rt)
		if len(providers) != 1 || providers[0].name != "zzz-has-key" {
			t.Fatalf("credentialed providers = %+v", providers)
		}
	})
}

func TestResolveStartupInvalidCloudModelErrors(t *testing.T) {
	t.Setenv("AXIS_TEST_CLOUD_INV", "k")
	rt := &runtimectx.Context{
		Config: &config.Config{
			AIProviders: map[string]config.AIProviderConfig{
				"groq": {
					Enabled: true, Type: "cloud", Kind: "groq", Priority: 50,
					APIKeyEnv: "AXIS_TEST_CLOUD_INV",
					Models:    []config.AIModelConfig{{Name: "llama-fast", CostPer1K: 0.01}},
				},
			},
		},
	}
	local := []ModelChoice{{
		ID: "local:ollama:qwen", Model: "qwen", Protocol: agent.ProtocolOllama,
		ProviderName: "ollama", ProviderKind: "local", Endpoint: chat.DefaultEndpoint,
	}}

	tests := []struct {
		name     string
		provider string
		cloud    string
		wantSub  string
	}{
		{name: "cloud provider mode", provider: "cloud", cloud: "typo-model", wantSub: "typo-model"},
		{name: "auto does not ignore bad cloud-model", provider: "auto", cloud: "typo-model", wantSub: "typo-model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := resolveStartupModelTarget("", tt.provider, tt.cloud, nil, rt, local)
			if err == nil {
				t.Fatal("expected error for invalid cloud model")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("error %q should mention %q", err.Error(), tt.wantSub)
			}
			if !strings.Contains(err.Error(), "groq:llama-fast") {
				t.Fatalf("error %q should list valid choices", err.Error())
			}
		})
	}

	t.Run("valid cloud-model succeeds", func(t *testing.T) {
		target, opts, err := resolveStartupModelTarget("", "cloud", "llama-fast", nil, rt, local)
		if err != nil {
			t.Fatal(err)
		}
		if target.Model != "llama-fast" || opts.APIKey != "k" {
			t.Fatalf("got target=%+v opts=%+v", target, opts)
		}
	})

	t.Run("provider-qualified form", func(t *testing.T) {
		target, _, err := resolveStartupModelTarget("", "cloud", "groq:llama-fast", nil, rt, nil)
		if err != nil {
			t.Fatal(err)
		}
		if target.ProviderName != "groq" || target.Model != "llama-fast" {
			t.Fatalf("got %+v", target)
		}
	})
}

func TestResolveCheapCloudTargetCostAndMembership(t *testing.T) {
	t.Setenv("AXIS_TEST_CHEAP_KEY", "ck")
	rt := &runtimectx.Context{
		Config: &config.Config{
			AIProviders: map[string]config.AIProviderConfig{
				"groq": {
					Enabled: true, Type: "cloud", Kind: "groq",
					APIKeyEnv: "AXIS_TEST_CHEAP_KEY",
					Endpoint:  "https://api.groq.example",
					Models: []config.AIModelConfig{
						{Name: "primary-big", CostPer1K: 0.10},
						{Name: "mini-fast", CostPer1K: 0.001, Aliases: []string{"mini"}},
					},
				},
			},
		},
	}
	primary := ModelChoice{
		ID: "cloud:groq:primary-big", Model: "primary-big", Protocol: agent.ProtocolCloud,
		ProviderName: "groq", ProviderKind: "cloud", Endpoint: "https://api.groq.example",
	}

	t.Run("resolves cheap cost not primary cost", func(t *testing.T) {
		target, opts, err := resolveCheapCloudTarget(rt, primary, "mini-fast")
		if err != nil {
			t.Fatal(err)
		}
		if target.Model != "mini-fast" {
			t.Fatalf("model = %q", target.Model)
		}
		if opts.CostPer1K != 0.001 {
			t.Fatalf("cost = %v, want 0.001 (not primary 0.10)", opts.CostPer1K)
		}
		if opts.APIKey != "ck" {
			t.Fatalf("key = %q", opts.APIKey)
		}
	})

	t.Run("alias resolves canonical name and cost", func(t *testing.T) {
		target, opts, err := resolveCheapCloudTarget(rt, primary, "mini")
		if err != nil {
			t.Fatal(err)
		}
		if target.Model != "mini-fast" || opts.CostPer1K != 0.001 {
			t.Fatalf("got target=%+v opts=%+v", target, opts)
		}
	})

	t.Run("rejects model not on provider", func(t *testing.T) {
		_, _, err := resolveCheapCloudTarget(rt, primary, "not-on-groq")
		if err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Fatalf("expected membership error, got %v", err)
		}
	})
}

func TestCloudOptsForTargetNormalizesProviderNameAndAliasCost(t *testing.T) {
	t.Setenv("AXIS_TEST_OPTS_KEY", "ok")
	rt := &runtimectx.Context{
		Config: &config.Config{
			AIProviders: map[string]config.AIProviderConfig{
				"Groq": { // canonical map key casing
					Enabled: true, Type: "cloud", Kind: "groq",
					APIKeyEnv: "AXIS_TEST_OPTS_KEY",
					Models: []config.AIModelConfig{
						{Name: "alpha", CostPer1K: 0.01, Aliases: []string{"alias-hit"}},
						{Name: "beta", CostPer1K: 0.99},
					},
				},
			},
		},
	}
	loadRT := func(context.Context) (*runtimectx.Context, error) { return rt, nil }

	t.Run("normalizes provider name via pointer", func(t *testing.T) {
		target := ModelChoice{
			Model: "alpha", Protocol: agent.ProtocolCloud, ProviderName: "groq", // wrong casing
		}
		opts, err := cloudOptsForTarget(loadRT, &target)
		if err != nil {
			t.Fatal(err)
		}
		if target.ProviderName != "Groq" {
			t.Fatalf("ProviderName = %q, want Groq (normalized onto pointer)", target.ProviderName)
		}
		if opts.CostPer1K != 0.01 || opts.APIKey != "ok" {
			t.Fatalf("opts = %+v", opts)
		}
	})

	t.Run("alias match does not pick later model cost", func(t *testing.T) {
		target := ModelChoice{
			Model: "alias-hit", Protocol: agent.ProtocolCloud, ProviderName: "Groq",
		}
		opts, err := cloudOptsForTarget(loadRT, &target)
		if err != nil {
			t.Fatal(err)
		}
		if opts.CostPer1K != 0.01 {
			t.Fatalf("cost = %v, want 0.01 from alias model (not beta 0.99)", opts.CostPer1K)
		}
		if target.Model != "alpha" {
			t.Fatalf("canonical model = %q, want alpha", target.Model)
		}
	})
}

func TestFindModelInProvider(t *testing.T) {
	pCfg := config.AIProviderConfig{
		Models: []config.AIModelConfig{
			{Name: "main", CostPer1K: 1, Aliases: []string{"m"}},
			{Name: "other", CostPer1K: 2},
		},
	}
	m, ok := findModelInProvider(pCfg, "m")
	if !ok || m.Name != "main" || m.CostPer1K != 1 {
		t.Fatalf("alias: got %+v ok=%v", m, ok)
	}
	if _, ok := findModelInProvider(pCfg, "missing"); ok {
		t.Fatal("expected miss")
	}
}
