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
	for _, c := range choices {
		switch c.Protocol {
		case agent.ProtocolOllama:
			sawOllama = true
		case agent.ProtocolOpenAI:
			sawOpenAI = true
		case agent.ProtocolCloud:
			sawCloud = true
		}
	}
	if !sawOllama || !sawOpenAI || !sawCloud {
		t.Fatalf("protocols ollama=%v openai=%v cloud=%v choices=%+v", sawOllama, sawOpenAI, sawCloud, choices)
	}
	_ = context.Background()
}
