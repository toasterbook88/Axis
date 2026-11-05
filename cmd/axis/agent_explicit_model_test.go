package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func TestExplicitGatewayModelWithoutRoleReachesStartup(t *testing.T) {
	previousLoad, previousResolve := inferenceAILoadFn, inferenceResolveFn
	t.Cleanup(func() { inferenceAILoadFn, inferenceResolveFn = previousLoad, previousResolve })
	cfg := &config.AIConfig{Backends: []config.AIBackendConfig{{Name: "gateway", Kind: config.AIBackendOpenAICompatible, BaseURL: "https://gateway.example/v1"}}}
	inferenceAILoadFn = func(string) (*config.AIConfig, error) { return cfg, nil }
	inferenceResolveFn = func(_ context.Context, _ *config.AIConfig, opts llmrouter.ResolveRoleOptions) (llmrouter.RoleRouteDecision, error) {
		return llmrouter.RoleRouteDecision{Backend: "gateway", Model: opts.Model, Endpoint: "https://gateway.example/v1", Kind: config.AIBackendOpenAICompatible,
			Probed: true, Healthy: true, ModelPresent: true, Probes: []llmrouter.BackendProbe{{Backend: "gateway", OK: true, Models: []string{"review-model"}}}}, nil
	}
	stubStartupReady(t, nil)
	got, err := setupAgentStartupBackend(agentStartupBackendParams{Ctx: context.Background(), Model: "review-model", ExplicitModel: true, StartupRequestedModel: "review-model",
		Provider: "local", RT: readyRuntime(), Out: io.Discard, ErrOut: io.Discard})
	if err != nil {
		t.Fatalf("explicit configured gateway model failed: %v", err)
	}
	if got.ActiveTarget.Protocol != agent.ProtocolOpenAI || got.ActiveTarget.Endpoint != "https://gateway.example/v1" || got.ActiveTarget.ProviderName != "ai-backend:gateway" {
		t.Fatalf("wrong target: %+v", got.ActiveTarget)
	}
}

type explicitModelTransport func(*http.Request) (*http.Response, error)

func (f explicitModelTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func explicitModelConfig(t *testing.T, backends []config.AIBackendConfig, bodies map[string]string) (*[]string, *config.AIConfig) {
	t.Helper()
	previousLoad, previousResolve := inferenceAILoadFn, inferenceResolveFn
	t.Cleanup(func() { inferenceAILoadFn, inferenceResolveFn = previousLoad, previousResolve })
	cfg := &config.AIConfig{Backends: backends}
	inferenceAILoadFn = func(string) (*config.AIConfig, error) { return cfg, nil }
	var seen []string
	var mu sync.Mutex
	client := &http.Client{Transport: explicitModelTransport(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		seen = append(seen, r.URL.String())
		mu.Unlock()
		body, ok := bodies[r.URL.String()]
		status := http.StatusOK
		if !ok {
			status, body = http.StatusNotFound, `{}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	inferenceResolveFn = func(ctx context.Context, cfg *config.AIConfig, opts llmrouter.ResolveRoleOptions) (llmrouter.RoleRouteDecision, error) {
		opts.HTTPClient = client
		return llmrouter.ResolveRole(ctx, cfg, opts)
	}
	return &seen, cfg
}

func TestExplicitGatewaySkipsEmptyAndTaggedOnlyBackends(t *testing.T) {
	for _, first := range []string{`{"data":[]}`, `{"data":[{"id":"review-model:7b"}]}`} {
		t.Run(first, func(t *testing.T) {
			_, _ = explicitModelConfig(t, []config.AIBackendConfig{
				{Name: "first", Kind: config.AIBackendOpenAICompatible, BaseURL: "https://first.example/v1"},
				{Name: "second", Kind: config.AIBackendOpenAICompatible, BaseURL: "https://second.example/v1"},
			}, map[string]string{"https://first.example/v1/models": first, "https://second.example/v1/models": `{"data":[{"id":"review-model"}]}`})
			got := appendExplicitAIModelChoice(context.Background(), "review-model", readyRuntime(), nil)
			if len(got) != 1 || got[0].ProviderName != "ai-backend:second" || got[0].Model != "review-model" {
				t.Fatalf("must select the exact ID on the later backend: %+v", got)
			}
		})
	}
}

func TestExplicitGatewayProvesTheViewerEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name, node, hostname, want string
	}{
		{"optional-node", "", "", "https://advertised.example/v1"},
		{"peer", "peer", "peer.example", "https://advertised.example/v1"},
		{"local", "here", "localhost", "http://127.0.0.1:18080/v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := "https://base.example/v1"
			if tc.name == "local" {
				base = tc.want
			}
			seen, _ := explicitModelConfig(t, []config.AIBackendConfig{{Name: "gateway", Kind: config.AIBackendOpenAICompatible,
				BaseURL: base, AdvertiseURL: "https://advertised.example/v1", Node: tc.node}},
				map[string]string{tc.want + "/models": `{"data":[{"id":"review-model"}]}`})
			rt := &runtimectx.Context{Config: &config.Config{Nodes: []config.NodeConfig{{Name: tc.node, Hostname: tc.hostname}}}}
			got := appendExplicitAIModelChoice(context.Background(), "review-model", rt, nil)
			if len(got) != 1 || got[0].Endpoint != tc.want || !strings.Contains(strings.Join(*seen, " "), tc.want+"/models") {
				t.Fatalf("target and positive proof must agree: got=%+v probes=%v", got, *seen)
			}
		})
	}
}

func TestExplicitModelOnlyOllamaLatestIsAnImplicitTag(t *testing.T) {
	for _, tc := range []struct {
		kind, listed string
		want         bool
	}{
		{config.AIBackendOllama, "review-model:latest", true},
		{config.AIBackendOllama, "review-model:7b", false},
		{config.AIBackendOpenAICompatible, "review-model:latest", false},
		{config.AIBackendOpenAICompatible, "Review-Model", false},
	} {
		t.Run(tc.kind+tc.listed, func(t *testing.T) {
			url, body := "https://gateway.example/v1/models", `{"data":[{"id":"`+tc.listed+`"}]}`
			if tc.kind == config.AIBackendOllama {
				url, body = "https://gateway.example/api/tags", `{"models":[{"name":"`+tc.listed+`"}]}`
			}
			explicitModelConfig(t, []config.AIBackendConfig{{Name: "gateway", Kind: tc.kind, BaseURL: "https://gateway.example/v1"}}, map[string]string{url: body})
			got := appendExplicitAIModelChoice(context.Background(), "review-model", readyRuntime(), nil)
			if (len(got) == 1) != tc.want {
				t.Fatalf("target=%+v want acceptance=%v", got, tc.want)
			}
		})
	}
}

func TestExplicitModelKeepsExistingChoiceAndCancellation(t *testing.T) {
	seen, _ := explicitModelConfig(t, []config.AIBackendConfig{{Name: "gateway", Kind: config.AIBackendOpenAICompatible, BaseURL: "https://gateway.example/v1"}}, nil)
	existing := []ModelChoice{{Model: "review-model", ProviderKind: "local", Endpoint: "https://existing.example/v1"}}
	got := appendExplicitAIModelChoice(context.Background(), "review-model", readyRuntime(), existing)
	if len(got) != 1 || got[0].Endpoint != existing[0].Endpoint || len(*seen) != 0 {
		t.Fatalf("existing choice changed: %+v probes=%v", got, *seen)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := appendExplicitAIModelChoice(ctx, "review-model", readyRuntime(), nil); len(got) != 0 || len(*seen) != 0 {
		t.Fatalf("cancelled request probed: %+v %v", got, *seen)
	}
	if got := appendExplicitAIModelChoice(context.Background(), "", readyRuntime(), nil); len(got) != 0 || len(*seen) != 0 {
		t.Fatalf("implicit request probed: %+v %v", got, *seen)
	}
}

func TestExplicitGatewayRetainsCredentialBinding(t *testing.T) {
	t.Setenv("AXIS_EXPLICIT_TEST_KEY", "synthetic-key")
	_, _ = explicitModelConfig(t, []config.AIBackendConfig{{Name: "gateway", Kind: config.AIBackendOpenAICompatible,
		BaseURL: "https://gateway.example/v1", APIKeyEnv: "AXIS_EXPLICIT_TEST_KEY"}}, map[string]string{"https://gateway.example/v1/models": `{"data":[{"id":"review-model"}]}`})
	got := appendExplicitAIModelChoice(context.Background(), "review-model", readyRuntime(), nil)
	if len(got) != 1 {
		t.Fatalf("missing target: %+v", got)
	}
	opts, err := cloudOptsForTarget(nil, &got[0])
	if err != nil || opts.APIKey != "synthetic-key" {
		t.Fatal("configured credential binding was not retained")
	}
}

func TestImplicitStartupDoesNotResolveBareGatewayModel(t *testing.T) {
	seen, _ := explicitModelConfig(t, []config.AIBackendConfig{{Name: "gateway", Kind: config.AIBackendOpenAICompatible, BaseURL: "https://gateway.example/v1"}}, nil)
	stubStartupReady(t, nil)
	_, err := setupAgentStartupBackend(agentStartupBackendParams{Ctx: context.Background(), StartupRequestedModel: "implicit-model", Provider: "local", RT: readyRuntime(), Out: io.Discard, ErrOut: io.Discard})
	if err != nil || len(*seen) != 0 {
		t.Fatalf("implicit startup changed: err=%v probes=%v", err, *seen)
	}
}

func TestExplicitGatewayRejectsUnprovenOrMismatchedDecision(t *testing.T) {
	for _, field := range []string{"error", "unprobed", "unhealthy", "unlisted", "backend", "model", "endpoint", "kind", "probe"} {
		t.Run(field, func(t *testing.T) {
			explicitModelConfig(t, []config.AIBackendConfig{{Name: "gateway", Kind: config.AIBackendOpenAICompatible, BaseURL: "https://gateway.example/v1"}}, nil)
			inferenceResolveFn = func(context.Context, *config.AIConfig, llmrouter.ResolveRoleOptions) (llmrouter.RoleRouteDecision, error) {
				d := llmrouter.RoleRouteDecision{Backend: "gateway", Model: "review-model", Endpoint: "https://gateway.example/v1", Kind: config.AIBackendOpenAICompatible,
					Probed: true, Healthy: true, ModelPresent: true, Probes: []llmrouter.BackendProbe{{Backend: "gateway", OK: true, Models: []string{"review-model"}}}}
				switch field {
				case "error":
					return d, errors.New("synthetic unavailable")
				case "unprobed":
					d.Probed = false
				case "unhealthy":
					d.Healthy = false
				case "unlisted":
					d.ModelPresent = false
				case "backend":
					d.Backend = "other"
				case "model":
					d.Model = "other"
				case "endpoint":
					d.Endpoint = "https://other.example/v1"
				case "kind":
					d.Kind = config.AIBackendOllama
				case "probe":
					d.Probes[0].OK = false
				}
				return d, nil
			}
			if got := appendExplicitAIModelChoice(context.Background(), "review-model", readyRuntime(), nil); len(got) != 0 {
				t.Fatalf("unproven choice accepted: %+v", got)
			}
		})
	}
}

func TestExplicitGatewaySkipsDisabledAndUnsupportedBackends(t *testing.T) {
	disabled := false
	seen, _ := explicitModelConfig(t, []config.AIBackendConfig{
		{Name: "disabled", Kind: config.AIBackendOpenAICompatible, BaseURL: "https://disabled.example/v1", Enabled: &disabled},
		{Name: "unknown", Kind: "unknown", BaseURL: "https://unknown.example/v1"},
	}, nil)
	if got := appendExplicitAIModelChoice(context.Background(), "review-model", readyRuntime(), nil); len(got) != 0 || len(*seen) != 0 {
		t.Fatalf("ineligible backend probed: %+v %v", got, *seen)
	}
}

func TestExplicitCloudStartupKeepsPrecedence(t *testing.T) {
	for _, provider := range []string{"cloud", "auto"} {
		t.Run(provider, func(t *testing.T) {
			seen, _ := explicitModelConfig(t, []config.AIBackendConfig{{Name: "gateway", Kind: config.AIBackendOpenAICompatible, BaseURL: "https://gateway.example/v1"}}, nil)
			t.Setenv("AXIS_EXPLICIT_CLOUD_KEY", "synthetic-key")
			rt := readyRuntime()
			rt.Config.AIProviders = map[string]config.AIProviderConfig{"cloud-test": {Type: "cloud", Kind: "openrouter", Enabled: true,
				Endpoint: "https://cloud.example/v1", APIKeyEnv: "AXIS_EXPLICIT_CLOUD_KEY", Models: []config.AIModelConfig{{Name: "cloud-model"}}}}
			got, err := setupAgentStartupBackend(agentStartupBackendParams{Ctx: context.Background(), Model: "review-model", ExplicitModel: true, StartupRequestedModel: "review-model",
				Provider: provider, CloudModel: "cloud-model", RT: rt, Out: io.Discard, ErrOut: io.Discard})
			if err != nil || got.ActiveTarget.Protocol != agent.ProtocolCloud || got.ActiveTarget.Model != "cloud-model" || len(*seen) != 0 {
				t.Fatalf("cloud precedence changed: %+v err=%v probes=%v", got, err, *seen)
			}
		})
	}
}
