package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
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

// stubAIRole serves one ai.yaml role on a reachable backend whose live model
// list is listed/listOK.
func stubAIRole(t *testing.T, roleModel string, listed []string, listOK bool) {
	t.Helper()
	prevLoad, prevResolve, prevProbe, prevList := inferenceAILoadFn, inferenceResolveFn, inferenceProbeFn, inferenceListModelsFn
	t.Cleanup(func() {
		inferenceAILoadFn, inferenceResolveFn, inferenceProbeFn, inferenceListModelsFn = prevLoad, prevResolve, prevProbe, prevList
	})
	cfg := &config.AIConfig{
		Backends: []config.AIBackendConfig{{Name: "hub", Kind: config.AIBackendOpenAICompatible, BaseURL: "http://hub.example.com/v1"}},
		Roles:    map[string]config.AIRoleConfig{"default": {Prefer: []string{"hub"}, Model: roleModel}},
	}
	inferenceAILoadFn = func(string) (*config.AIConfig, error) { return cfg, nil }
	inferenceResolveFn = func(_ context.Context, _ *config.AIConfig, _ llmrouter.ResolveRoleOptions) (llmrouter.RoleRouteDecision, error) {
		return llmrouter.RoleRouteDecision{Role: "default", Backend: "hub", Model: roleModel, Endpoint: "http://hub.example.com/v1", Kind: config.AIBackendOpenAICompatible}, nil
	}
	inferenceProbeFn = func(string) bool { return true }
	inferenceListModelsFn = func(context.Context, config.AIBackendConfig, *config.Config) ([]string, bool) { return listed, listOK }
}

// A role whose model the backend does not serve must not be selectable; the
// hub answering is not evidence that this model exists behind it.
func TestModelChoicesFromAIConfigDisablesModelNotServedByBackend(t *testing.T) {
	stubAIRole(t, "qwythos", []string{"bonsai-2-27b", "qwen3-coder"}, true)
	choices := modelChoicesFromAIConfig(nil)
	if len(choices) != 1 || !choices[0].Disabled || choices[0].DisabledReason != "not served by hub" {
		t.Fatalf("choices = %+v, want one disabled 'not served by hub'", choices)
	}
}

func TestModelChoicesFromAIConfigKeepsServedModel(t *testing.T) {
	stubAIRole(t, "qwen3-coder", []string{"bonsai-2-27b", "qwen3-coder"}, true)
	choices := modelChoicesFromAIConfig(nil)
	if len(choices) != 1 || choices[0].Disabled {
		t.Fatalf("choices = %+v, want one enabled", choices)
	}
}

// When the list cannot be read (auth, empty answer), absence is unknown, not
// proven, so the role stays selectable.
func TestModelChoicesFromAIConfigKeepsRoleWhenListUnavailable(t *testing.T) {
	stubAIRole(t, "qwythos", nil, false)
	choices := modelChoicesFromAIConfig(nil)
	if len(choices) != 1 || choices[0].Disabled {
		t.Fatalf("choices = %+v, want enabled when the list is unavailable", choices)
	}
}

// A local server that answers 401 is up but keyed; "unreachable" would be false.
func TestCollectModelChoicesSaysKeyedLocalServerNeedsKey(t *testing.T) {
	stubNoAIConfigAndRecordProbes(t)
	prevOK, prevStatus := probeEndpointFn, probeStatusFn
	t.Cleanup(func() { probeEndpointFn, probeStatusFn = prevOK, prevStatus })
	probeEndpointFn = func(string) bool { return false }
	probeStatusFn = func(string) int { return http.StatusUnauthorized }

	rt := catalogChoicesRuntime(t)
	rt.Snapshot.Nodes[0].ResidentModels = []models.ResidentModel{{Name: "keyed-gguf", Runtime: "llama.cpp", Port: 8082}}
	keyed, ok := choiceByModel(t, collectModelChoices(rt), "keyed-gguf")
	if !ok || !keyed.Disabled || keyed.DisabledReason != "requires an API key" {
		t.Fatalf("keyed = %+v, want disabled 'requires an API key'", keyed)
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
