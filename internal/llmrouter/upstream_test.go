package llmrouter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
)

// fakeHub mimics a LiteLLM proxy: GET /v1/models lists model names, and
// GET /model/info returns {"data":[{model_name, litellm_params.api_base, model_info}]},
// the shape a real proxy returns. A nil upstreams map makes /model/info 404,
// like an OpenAI-compatible hub that is not LiteLLM.
func fakeHub(t *testing.T, upstreams map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			var data []map[string]string
			for name := range upstreams {
				data = append(data, map[string]string{"id": name})
			}
			if upstreams == nil {
				data = append(data, map[string]string{"id": "m"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		case "/model/info":
			if upstreams == nil {
				http.NotFound(w, r)
				return
			}
			var data []map[string]any
			for name, base := range upstreams {
				data = append(data, map[string]any{
					"model_name":     name,
					"litellm_params": map[string]any{"model": "openai/" + name, "api_base": base},
					"model_info":     map[string]any{"id": "id-" + name, "db_model": false},
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// liveUpstream answers /v1/models like an Ollama or llama-server upstream.
func liveUpstream(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

// deadUpstream returns the address of a server that has stopped, so a
// connection to it is refused.
func deadUpstream(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/v1"
	srv.Close()
	return url
}

func hubConfig(prefer []string, backends ...config.AIBackendConfig) *config.AIConfig {
	return &config.AIConfig{
		Backends: backends,
		Roles:    map[string]config.AIRoleConfig{"fast": {Prefer: prefer, Model: "m"}},
	}
}

func resolveFast(t *testing.T, cfg *config.AIConfig, strict bool) (llmrouter.RoleRouteDecision, error) {
	t.Helper()
	return llmrouter.ResolveRole(context.Background(), cfg, llmrouter.ResolveRoleOptions{
		Role: "fast", HTTPClient: http.DefaultClient, RequireModelListed: strict,
	})
}

// The hub lists the model, but the server behind it is down. The route must
// not call that healthy; it should fall through to the next preferred backend.
func TestResolveRole_ListedModelWithDeadUpstreamFallsThrough(t *testing.T) {
	hub := fakeHub(t, map[string]string{"m": deadUpstream(t)})
	other := fakeHub(t, map[string]string{"m": liveUpstream(t)})
	cfg := hubConfig([]string{"hub", "other"},
		config.AIBackendConfig{Name: "hub", Kind: config.AIBackendOpenAICompatible, BaseURL: hub.URL + "/v1"},
		config.AIBackendConfig{Name: "other", Kind: config.AIBackendOpenAICompatible, BaseURL: other.URL + "/v1"},
	)
	dec, err := resolveFast(t, cfg, false)
	if err != nil {
		t.Fatalf("ResolveRole: %v", err)
	}
	if dec.Backend != "other" {
		t.Fatalf("backend=%q want other; reasoning=%v", dec.Backend, dec.Reasoning)
	}
	if !strings.Contains(strings.Join(dec.Reasoning, " "), "upstream") {
		t.Fatalf("reasoning should name the dead upstream: %v", dec.Reasoning)
	}
}

// With no other backend, a dead upstream must not report healthy at 0.95.
func TestResolveRole_ListedModelWithDeadUpstreamIsNotHealthy(t *testing.T) {
	hub := fakeHub(t, map[string]string{"m": deadUpstream(t)})
	cfg := hubConfig([]string{"hub"},
		config.AIBackendConfig{Name: "hub", Kind: config.AIBackendOpenAICompatible, BaseURL: hub.URL + "/v1"},
	)
	dec, err := resolveFast(t, cfg, false)
	if err != nil {
		t.Fatalf("ResolveRole: %v", err)
	}
	if dec.Healthy || dec.Confidence >= 0.5 {
		t.Fatalf("dead upstream reported healthy=%v confidence=%.2f; reasoning=%v", dec.Healthy, dec.Confidence, dec.Reasoning)
	}
	if len(dec.Upstreams) != 1 || dec.Upstreams[0].OK {
		t.Fatalf("upstreams=%+v want one failed check", dec.Upstreams)
	}
}

// Strict mode returns ErrModelUnlisted and says the upstream is the reason.
func TestResolveRole_StrictDeadUpstreamErrors(t *testing.T) {
	hub := fakeHub(t, map[string]string{"m": deadUpstream(t)})
	cfg := hubConfig([]string{"hub"},
		config.AIBackendConfig{Name: "hub", Kind: config.AIBackendOpenAICompatible, BaseURL: hub.URL + "/v1"},
	)
	_, err := resolveFast(t, cfg, true)
	if err == nil || !strings.Contains(err.Error(), "upstream") {
		t.Fatalf("err=%v want an error naming the upstream", err)
	}
}

// An upstream that accepts the connection but fails (an SSH tunnel whose far
// end is gone answers like this) is dead too.
func TestResolveRole_UpstreamServerErrorCountsAsDead(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	t.Cleanup(broken.Close)
	hub := fakeHub(t, map[string]string{"m": broken.URL + "/v1"})
	cfg := hubConfig([]string{"hub"},
		config.AIBackendConfig{Name: "hub", Kind: config.AIBackendOpenAICompatible, BaseURL: hub.URL + "/v1"},
	)
	dec, _ := resolveFast(t, cfg, false)
	if dec.Healthy {
		t.Fatalf("502 upstream reported healthy; reasoning=%v", dec.Reasoning)
	}
}

// A live upstream keeps today's answer: healthy, listed, 0.95.
func TestResolveRole_ListedModelWithLiveUpstreamUnchanged(t *testing.T) {
	hub := fakeHub(t, map[string]string{"m": liveUpstream(t)})
	cfg := hubConfig([]string{"hub"},
		config.AIBackendConfig{Name: "hub", Kind: config.AIBackendOpenAICompatible, BaseURL: hub.URL + "/v1"},
	)
	dec, err := resolveFast(t, cfg, false)
	if err != nil {
		t.Fatalf("ResolveRole: %v", err)
	}
	if !dec.Healthy || !dec.ModelPresent || dec.Confidence != 0.95 {
		t.Fatalf("decision=%+v", dec)
	}
	if len(dec.Upstreams) != 1 || !dec.Upstreams[0].OK {
		t.Fatalf("upstreams=%+v want one passed check", dec.Upstreams)
	}
}

// A hub without /model/info (not LiteLLM) keeps today's answer, with a note.
func TestResolveRole_HubWithoutModelInfoIsNotChecked(t *testing.T) {
	hub := fakeHub(t, nil)
	cfg := hubConfig([]string{"hub"},
		config.AIBackendConfig{Name: "hub", Kind: config.AIBackendOpenAICompatible, BaseURL: hub.URL + "/v1"},
	)
	dec, err := resolveFast(t, cfg, false)
	if err != nil {
		t.Fatalf("ResolveRole: %v", err)
	}
	if !dec.Healthy || dec.Confidence != 0.95 {
		t.Fatalf("decision=%+v", dec)
	}
	if !strings.Contains(strings.Join(dec.Reasoning, " "), "upstream not checked") {
		t.Fatalf("want a not-checked note: %v", dec.Reasoning)
	}
}

// Upstream addresses in /model/info are relative to the hub's machine, so a
// hub on another node is not checked from here.
func TestResolveRole_PeerHubIsNotChecked(t *testing.T) {
	hub := fakeHub(t, map[string]string{"m": deadUpstream(t)})
	cfg := hubConfig([]string{"hub"},
		config.AIBackendConfig{Name: "hub", Kind: config.AIBackendOpenAICompatible, BaseURL: hub.URL + "/v1", Node: "some-peer-node"},
	)
	dec, err := resolveFast(t, cfg, false)
	if err != nil {
		t.Fatalf("ResolveRole: %v", err)
	}
	if !dec.Healthy || len(dec.Upstreams) != 0 {
		t.Fatalf("peer hub was checked: %+v", dec)
	}
	if !strings.Contains(strings.Join(dec.Reasoning, " "), "upstream not checked") {
		t.Fatalf("want a not-checked note: %v", dec.Reasoning)
	}
}

// fakeHubInfo is a LiteLLM hub that lists model "m" and returns entries as
// its /model/info data, for deployment shapes fakeHub cannot express.
func fakeHubInfo(t *testing.T, entries ...map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
		case "/model/info":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": entries})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func deployment(apiBase string) map[string]any {
	params := map[string]any{"model": "openai/m"}
	if apiBase != "" {
		params["api_base"] = apiBase
	}
	return map[string]any{"model_name": "m", "litellm_params": params}
}

// redirectTransport sends requests for host to target, so a test hub can
// stand in for one on another machine.
type redirectTransport struct{ host, target string }

func (rt redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host == rt.host {
		r = r.Clone(r.Context())
		r.URL.Host = rt.target
		r.Host = rt.target
	}
	return http.DefaultTransport.RoundTrip(r)
}

// An unbound hub at a private address can be another machine on the LAN. Its
// api_base values are relative to that machine, so they are not dialed from here.
func TestResolveRole_UnboundLANHubIsNotChecked(t *testing.T) {
	hub := fakeHubInfo(t, deployment(deadUpstream(t)))
	const lanHost = "10.254.0.7:4000"
	cfg := hubConfig([]string{"hub"},
		config.AIBackendConfig{Name: "hub", Kind: config.AIBackendOpenAICompatible, BaseURL: "http://" + lanHost + "/v1"},
	)
	client := &http.Client{Transport: redirectTransport{host: lanHost, target: strings.TrimPrefix(hub.URL, "http://")}}
	dec, err := llmrouter.ResolveRole(context.Background(), cfg, llmrouter.ResolveRoleOptions{
		Role: "fast", HTTPClient: client, RequireModelListed: true,
	})
	if err != nil {
		t.Fatalf("ResolveRole: %v; reasoning=%v", err, dec.Reasoning)
	}
	if !dec.Healthy || len(dec.Upstreams) != 0 {
		t.Fatalf("LAN hub's upstreams were dialed from here: %+v", dec)
	}
	if !strings.Contains(strings.Join(dec.Reasoning, " "), "upstream not checked") {
		t.Fatalf("want a not-checked note: %v", dec.Reasoning)
	}
}

// An api_base with a non-/v1 API prefix lists models at <api_base>/models.
func TestResolveRole_UpstreamWithNonV1PrefixIsChecked(t *testing.T) {
	prefixed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	t.Cleanup(prefixed.Close)
	hub := fakeHubInfo(t, deployment(prefixed.URL+"/api"))
	cfg := hubConfig([]string{"hub"},
		config.AIBackendConfig{Name: "hub", Kind: config.AIBackendOpenAICompatible, BaseURL: hub.URL + "/v1"},
	)
	dec, err := resolveFast(t, cfg, true)
	if err != nil {
		t.Fatalf("ResolveRole: %v; reasoning=%v", err, dec.Reasoning)
	}
	if len(dec.Upstreams) != 1 || !dec.Upstreams[0].OK {
		t.Fatalf("upstreams=%+v want one passed check", dec.Upstreams)
	}
}

// An Ollama-style root api_base (LiteLLM's documented form) serves /v1/models
// only, so the /v1 form stays the first request.
func TestResolveRole_RootUpstreamUsesV1Models(t *testing.T) {
	var paths []string
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	t.Cleanup(ollama.Close)
	hub := fakeHubInfo(t, deployment(ollama.URL))
	cfg := hubConfig([]string{"hub"},
		config.AIBackendConfig{Name: "hub", Kind: config.AIBackendOpenAICompatible, BaseURL: hub.URL + "/v1"},
	)
	dec, err := resolveFast(t, cfg, true)
	if err != nil {
		t.Fatalf("ResolveRole: %v; reasoning=%v", err, dec.Reasoning)
	}
	if len(dec.Upstreams) != 1 || !dec.Upstreams[0].OK || len(paths) != 1 {
		t.Fatalf("upstreams=%+v paths=%v want one passed check at /v1/models", dec.Upstreams, paths)
	}
}

// A deployment without api_base uses its provider's default endpoint, which
// this check cannot see, so a dead custom deployment beside it is not a verdict.
func TestResolveRole_DeploymentWithoutAPIBaseMakesCheckInconclusive(t *testing.T) {
	hub := fakeHubInfo(t, deployment(deadUpstream(t)), deployment(""))
	cfg := hubConfig([]string{"hub"},
		config.AIBackendConfig{Name: "hub", Kind: config.AIBackendOpenAICompatible, BaseURL: hub.URL + "/v1"},
	)
	dec, err := resolveFast(t, cfg, true)
	if err != nil {
		t.Fatalf("ResolveRole: %v; reasoning=%v", err, dec.Reasoning)
	}
	if !dec.Healthy {
		t.Fatalf("hub rejected though one deployment was not checked: %+v", dec)
	}
	reasoning := strings.Join(dec.Reasoning, " ")
	if !strings.Contains(reasoning, "upstream not checked") || !strings.Contains(reasoning, "1 deployment") {
		t.Fatalf("want a not-checked note naming the unchecked deployment: %v", dec.Reasoning)
	}
}
