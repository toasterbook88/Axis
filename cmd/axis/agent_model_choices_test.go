package main

import (
	"os"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

// catalogChoicesRuntime has a local node (matched by hostname) and a remote
// node with an Ollama catalog: a chat model, an embedding model, and a cloud
// proxy, one of them loaded.
func catalogChoicesRuntime(t *testing.T) *runtimectx.Context {
	t.Helper()
	hn, err := os.Hostname()
	if err != nil || hn == "" {
		t.Skip("hostname unavailable")
	}
	return &runtimectx.Context{
		Snapshot: &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{
				{
					Name: "here", Hostname: hn, Status: models.StatusComplete,
					Ollama: &models.OllamaInfo{Installed: true, Port: 11434, Catalog: []models.OllamaModelEntry{
						{Name: "local-chat:1b", Capabilities: []string{"completion"}},
					}},
				},
				{
					Name: "worker", Hostname: "worker", SSHTarget: "198.51.100.7", Status: models.StatusComplete,
					Ollama: &models.OllamaInfo{Installed: true, Port: 11434, Catalog: []models.OllamaModelEntry{
						{Name: "coder:7b", Capabilities: []string{"completion", "tools"}},
						{Name: "embed:latest", Capabilities: []string{"embedding"}},
						{Name: "big:cloud", RemoteHost: "https://cloud.example.com:443", Capabilities: []string{"completion"}},
						{Name: "mystery:1b"}, // capabilities unknown
					}},
					ResidentModels: []models.ResidentModel{{Name: "coder:7b", Runtime: "ollama", Port: 11434}},
				},
				{Name: "gone", Status: models.StatusUnreachable, Error: "ssh: unable to authenticate"},
			},
		},
		Config: &config.Config{},
	}
}

func stubNoAIConfigAndRecordProbes(t *testing.T) map[string]bool {
	t.Helper()
	prevProbe, prevLoad := probeEndpointFn, inferenceAILoadFn
	t.Cleanup(func() { probeEndpointFn, inferenceAILoadFn = prevProbe, prevLoad })
	inferenceAILoadFn = func(string) (*config.AIConfig, error) { return &config.AIConfig{}, nil }
	probed := map[string]bool{}
	probeEndpointFn = func(url string) bool { probed[url] = true; return true }
	return probed
}

func choiceByModel(t *testing.T, choices []ModelChoice, model string) (ModelChoice, bool) {
	t.Helper()
	for _, c := range choices {
		if c.Model == model {
			return c, true
		}
	}
	return ModelChoice{}, false
}

// Listing must not probe remote nodes: probing from the caller's vantage is
// what made the list differ from node to node.
func TestCollectModelChoicesDoesNotProbeRemoteNodes(t *testing.T) {
	probed := stubNoAIConfigAndRecordProbes(t)
	choices := collectModelChoices(catalogChoicesRuntime(t))
	for url := range probed {
		if !strings.Contains(url, "localhost") {
			t.Fatalf("listing probed remote endpoint %s", url)
		}
	}
	coder, ok := choiceByModel(t, choices, "coder:7b")
	if !ok || coder.Disabled || coder.Node != "worker" || coder.Port != 11434 || coder.Protocol != agent.ProtocolOllama {
		t.Fatalf("coder = %+v (found %v), want enabled ollama choice on worker:11434", coder, ok)
	}
	if !coder.Loaded {
		t.Fatalf("coder.Loaded = false, want true (resident)")
	}
}

func TestCollectModelChoicesExcludesEmbeddingOnlyModels(t *testing.T) {
	stubNoAIConfigAndRecordProbes(t)
	choices := collectModelChoices(catalogChoicesRuntime(t))
	if _, ok := choiceByModel(t, choices, "embed:latest"); ok {
		t.Fatal("embedding-only model offered as a chat model")
	}
	// Unknown capabilities are not evidence of "embedding only".
	if _, ok := choiceByModel(t, choices, "mystery:1b"); !ok {
		t.Fatal("model with unknown capabilities was dropped")
	}
}

func TestCollectModelChoicesFlagsCloudProxyAsRemote(t *testing.T) {
	stubNoAIConfigAndRecordProbes(t)
	choices := collectModelChoices(catalogChoicesRuntime(t))
	big, ok := choiceByModel(t, choices, "big:cloud")
	if !ok || !big.CloudProxy || big.SecurityClass != agent.BackendRemote {
		t.Fatalf("big = %+v, want cloud-proxy with remote security class", big)
	}
}

func TestCollectModelChoicesLabelsLocalNodeAsLocal(t *testing.T) {
	stubNoAIConfigAndRecordProbes(t)
	choices := collectModelChoices(catalogChoicesRuntime(t))
	local, ok := choiceByModel(t, choices, "local-chat:1b")
	if !ok || local.Node != "" || local.SecurityClass != agent.BackendLocal {
		t.Fatalf("local = %+v, want Node empty (local) and local security class", local)
	}
}

func TestModelChoiceDetailSaysWhereAndWhat(t *testing.T) {
	cases := []struct {
		choice ModelChoice
		want   string
	}{
		{ModelChoice{ProviderKind: "local", ProviderName: "ollama", Node: "", Loaded: true}, "this node · ollama · loaded"},
		{ModelChoice{ProviderKind: "local", ProviderName: "ollama", Node: "worker", CloudProxy: true}, "node worker · ollama · cloud proxy, leaves the cluster"},
		{ModelChoice{ProviderKind: "local", ProviderName: "llama.cpp", Node: "worker", Disabled: true, DisabledReason: "unreachable"}, "node worker · llama.cpp (unreachable)"},
		{ModelChoice{ProviderKind: "cloud", ProviderName: "groq"}, "cloud provider groq"},
	}
	for _, tc := range cases {
		if got := modelChoiceDetail(tc.choice); got != tc.want {
			t.Errorf("modelChoiceDetail(%+v) = %q, want %q", tc.choice, got, tc.want)
		}
	}
}

// IDs keep the pre-catalog format so saved defaults and /model <id> still match.
func TestCollectModelChoicesKeepsChoiceIDFormat(t *testing.T) {
	stubNoAIConfigAndRecordProbes(t)
	choices := collectModelChoices(catalogChoicesRuntime(t))
	coder, _ := choiceByModel(t, choices, "coder:7b")
	if coder.ID != "worker:ollama:coder:7b" {
		t.Fatalf("ID = %q, want worker:ollama:coder:7b", coder.ID)
	}
}
