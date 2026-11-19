package llmrouter_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
)

// listingUpstream answers every model-list request with body and counts them.
func listingUpstream(t *testing.T, body string) (string, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1", &hits
}

// listing is an OpenAI-style model list of ids owned by an Ollama-like owner.
func listing(ids ...string) string {
	data := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		data = append(data, map[string]string{"id": id, "object": "model", "owned_by": "library"})
	}
	b, _ := json.Marshal(map[string]any{"object": "list", "data": data})
	return string(b)
}

// namedHub lists names at /v1/models and serves entries at /model/info.
func namedHub(t *testing.T, names []string, entries ...map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			var data []map[string]string
			for _, n := range names {
				data = append(data, map[string]string{"id": n})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		case "/model/info":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": entries})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// entry is one /model/info deployment; extra adds litellm_params fields.
func entry(name, model, apiBase string, extra map[string]any) map[string]any {
	params := map[string]any{"model": model, "api_base": apiBase}
	for k, v := range extra {
		params[k] = v
	}
	return map[string]any{"model_name": name, "litellm_params": params}
}

func hubBackend(name string, srv *httptest.Server) config.AIBackendConfig {
	return config.AIBackendConfig{Name: name, Kind: config.AIBackendOpenAICompatible, BaseURL: srv.URL + "/v1"}
}

func resolveModel(t *testing.T, cfg *config.AIConfig, model string, strict bool) (llmrouter.RoleRouteDecision, error) {
	t.Helper()
	return llmrouter.ResolveRole(context.Background(), cfg, llmrouter.ResolveRoleOptions{
		Model: model, HTTPClient: http.DefaultClient, RequireModelListed: strict,
	})
}

// The incident: the hub lists the model and the server behind it is up, but no
// longer has it. Strict routing falls through to a hub that can serve it.
func TestResolveRole_UpstreamWithoutTheModelIsNotServing(t *testing.T) {
	up, _ := listingUpstream(t, listing("other-model:latest"))
	hub := namedHub(t, []string{"m"}, entry("m", "openai/m", up, nil))
	other := fakeHub(t, map[string]string{"m": liveUpstream(t)})
	cfg := hubConfig([]string{"hub", "other"}, hubBackend("hub", hub), hubBackend("other", other))
	dec, err := resolveFast(t, cfg, true)
	if err != nil || dec.Backend != "other" {
		t.Fatalf("backend=%q err=%v; reasoning=%v", dec.Backend, err, dec.Reasoning)
	}
	if !strings.Contains(strings.Join(dec.Reasoning, " "), `listed, but no upstream serves it (`+up+`: answered, but does not list "m")`) {
		t.Fatalf("reasoning should name the upstream that lacks the model: %v", dec.Reasoning)
	}
	if len(dec.Upstreams) == 0 || !dec.Upstreams[0].Unlisted || dec.Upstreams[0].Model != "m" || dec.Upstreams[0].OK {
		t.Fatalf("upstreams=%+v want the first check unlisted for model m", dec.Upstreams)
	}
	// axis ai route --format json|yaml and inference_route_explain show both fields.
	// The decision has its own top-level model, so look inside upstreams only.
	js, _ := json.Marshal(dec.Upstreams)
	ys, _ := yaml.Marshal(dec.Upstreams)
	for _, want := range []string{`"model":"m"`, `"unlisted":true`} {
		if !strings.Contains(string(js), want) {
			t.Fatalf("JSON lacks %s: %s", want, js)
		}
	}
	for _, want := range []string{"model: m\n", "unlisted: true\n"} {
		if !strings.Contains(string(ys), want) {
			t.Fatalf("YAML lacks %q: %s", want, ys)
		}
	}

	alone := hubConfig([]string{"hub"}, hubBackend("hub", hub))
	_, err = resolveFast(t, alone, true)
	if !errors.Is(err, llmrouter.ErrModelUnlisted) || !strings.HasSuffix(err.Error(), "(listed, but no upstream serves it)") {
		t.Fatalf("strict error = %v", err)
	}
}

// allow_unlisted keeps a hub whose upstream only fails the name check selectable.
func TestResolveRole_AllowUnlistedKeepsAnUnlistedUpstreamSelectable(t *testing.T) {
	up, _ := listingUpstream(t, listing("other-model:latest"))
	hub := namedHub(t, []string{"m"}, entry("m", "openai/m", up, nil))
	dec, err := resolveFast(t, hubConfig([]string{"hub"}, hubBackend("hub", hub)), false)
	if err != nil || dec.Backend != "hub" || !dec.Healthy || dec.Confidence != 0.55 {
		t.Fatalf("decision=%+v err=%v", dec, err)
	}
	if !strings.Contains(strings.Join(dec.Reasoning, " "), `its upstream does not list "m" — still selecting (allow_unlisted)`) {
		t.Fatalf("reasoning=%v", dec.Reasoning)
	}
}

// Every provider that names a server by api_base forwards a checkable name, as
// a prefix or as custom_llm_provider; any other provider gives no verdict.
func TestUpstreamForwardedNameByProvider(t *testing.T) {
	type tc struct {
		name, model   string
		extra         map[string]any
		wantForwarded string
	}
	var cases []tc
	for _, p := range []string{"openai", "ollama", "ollama_chat", "hosted_vllm", "lm_studio", "llamafile"} {
		cases = append(cases,
			tc{p + "-prefix", p + "/m", nil, "m"},
			tc{p + "-custom", "m", map[string]any{"custom_llm_provider": p}, "m"},
			tc{p + "-custom-prefixed", p + "/m", map[string]any{"custom_llm_provider": p}, "m"},
		)
	}
	cases = append(cases,
		tc{"custom-wins-over-prefix", "ollama/m", map[string]any{"custom_llm_provider": "openai"}, "ollama/m"},
		tc{"unknown-prefix", "openrouter/m", nil, ""},
		tc{"unknown-custom", "m", map[string]any{"custom_llm_provider": "openrouter"}, ""},
		tc{"no-provider", "m", nil, ""},
	)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			up, _ := listingUpstream(t, listing("other-model:latest"))
			hub := namedHub(t, []string{"m"}, entry("m", c.model, up, c.extra))
			dec, _ := resolveFast(t, hubConfig([]string{"hub"}, hubBackend("hub", hub)), true)
			if len(dec.Upstreams) != 1 {
				t.Fatalf("upstreams=%+v", dec.Upstreams)
			}
			got := dec.Upstreams[0]
			if got.Model != c.wantForwarded || got.Unlisted != (c.wantForwarded != "") || got.OK != (c.wantForwarded == "") {
				t.Fatalf("check=%+v want forwarded %q", got, c.wantForwarded)
			}
		})
	}
}

// Servers that answer names they do not list give no verdict.
func TestUpstreamServersThatAnswerUnlistedNames(t *testing.T) {
	for name, tc := range map[string]struct{ forwarded, body string }{
		"llama-server": {"openai/m", `{"object":"list","data":[{"id":"/models/weights-Q4_K_M.gguf","object":"model","owned_by":"llamacpp"}]}`},
		"tgi":          {"openai/m", `{"object":"list","data":[{"id":"org/model-7b","object":"model","created":0,"owned_by":"org/model-7b"}]}`},
		"tgi-name":     {"openai/tgi", listing("org/model-7b")},
	} {
		t.Run(name, func(t *testing.T) {
			up, _ := listingUpstream(t, tc.body)
			hub := namedHub(t, []string{"m"}, entry("m", tc.forwarded, up, nil))
			dec, err := resolveFast(t, hubConfig([]string{"hub"}, hubBackend("hub", hub)), true)
			if err != nil || !dec.Healthy || len(dec.Upstreams) != 1 || !dec.Upstreams[0].OK {
				t.Fatalf("decision=%+v err=%v", dec, err)
			}
		})
	}
}

// The reasoning and the check name the model the hub forwards, not the alias,
// and the strict skip names the upstream that answered without it.
func TestResolveRole_ReasoningNamesTheForwardedModel(t *testing.T) {
	dead := deadUpstream(t)
	up, _ := listingUpstream(t, listing("other-model:latest"))
	hub := namedHub(t, []string{"m"}, entry("m", "openai/served-m", dead, nil), entry("m", "openai/served-m", up, nil))
	cfg := hubConfig([]string{"hub"}, hubBackend("hub", hub))
	dec, err := resolveFast(t, cfg, true)
	if !errors.Is(err, llmrouter.ErrModelUnlisted) || !strings.HasSuffix(err.Error(), "(listed, but no upstream serves it)") {
		t.Fatalf("strict error = %v", err)
	}
	if !strings.Contains(strings.Join(dec.Reasoning, " "), `no upstream serves it (`+up+`: answered, but does not list "served-m")`) {
		t.Fatalf("strict reasoning should name the unlisted upstream and the forwarded model: %v", dec.Reasoning)
	}
	if len(dec.Upstreams) != 2 || dec.Upstreams[1].Model != "served-m" || !dec.Upstreams[1].Unlisted || dec.Upstreams[0].Unlisted {
		t.Fatalf("upstreams=%+v", dec.Upstreams)
	}
	dec, err = resolveFast(t, cfg, false)
	if err != nil || dec.Confidence != 0.55 || !strings.Contains(strings.Join(dec.Reasoning, " "), `its upstream does not list "served-m"`) {
		t.Fatalf("allow_unlisted: decision=%+v err=%v", dec, err)
	}
}

// A repository-style forwarded name is checked, not dropped.
func TestUpstreamRepositoryNameIsChecked(t *testing.T) {
	up, _ := listingUpstream(t, listing("org/other-4bit"))
	hub := namedHub(t, []string{"m"}, entry("m", "openai/org/m-4bit", up, nil))
	dec, _ := resolveFast(t, hubConfig([]string{"hub"}, hubBackend("hub", hub)), true)
	if len(dec.Upstreams) != 1 || dec.Upstreams[0].Model != "org/m-4bit" || !dec.Upstreams[0].Unlisted {
		t.Fatalf("upstreams=%+v want org/m-4bit unlisted", dec.Upstreams)
	}
}

// With no exact alias, the tag-insensitive fallback still finds the deployment.
func TestResolveRole_TaggedRequestFallsBackToTheUntaggedAlias(t *testing.T) {
	up, _ := listingUpstream(t, listing("other-model:latest"))
	hub := namedHub(t, []string{"m"}, entry("m", "openai/m", up, nil))
	dec, err := resolveModel(t, &config.AIConfig{Backends: []config.AIBackendConfig{hubBackend("hub", hub)}}, "m:latest", true)
	if !errors.Is(err, llmrouter.ErrModelUnlisted) || len(dec.Upstreams) != 1 || !dec.Upstreams[0].Unlisted {
		t.Fatalf("err=%v upstreams=%+v", err, dec.Upstreams)
	}
}

// Case, the implicit :latest tag and repository-style names match.
func TestUpstreamNameVariantsMatch(t *testing.T) {
	for name, tc := range map[string]struct{ forwarded, listed string }{
		"case":             {"openai/MODEL-9B", "model-9b"},
		"case-and-latest":  {"openai/M", "m:latest"},
		"latest-forwarded": {"openai/m:latest", "m"},
		"repository":       {"openai/org/m-4bit", "org/m-4bit"},
		"tagged":           {"openai/m:9b", "m:9b"},
	} {
		t.Run(name, func(t *testing.T) {
			up, _ := listingUpstream(t, listing("unrelated", tc.listed))
			hub := namedHub(t, []string{"m"}, entry("m", tc.forwarded, up, nil))
			dec, err := resolveFast(t, hubConfig([]string{"hub"}, hubBackend("hub", hub)), true)
			if err != nil || len(dec.Upstreams) != 1 || !dec.Upstreams[0].OK {
				t.Fatalf("decision=%+v err=%v", dec, err)
			}
			// The match is a real check of the forwarded name, not a missing verdict.
			if want := strings.TrimPrefix(tc.forwarded, "openai/"); dec.Upstreams[0].Model != want {
				t.Fatalf("forwarded model %q, want %q", dec.Upstreams[0].Model, want)
			}
		})
	}
}

// A list that cannot say gives "answered", as before.
func TestUpstreamEmptyOrUnparseableListGivesAnswered(t *testing.T) {
	for name, body := range map[string]string{
		"empty":       `{"object":"list","data":[]}`,
		"no-data":     `{"object":"list"}`,
		"not-json":    `<html>ok</html>`,
		"ids-missing": `{"data":[{"object":"model"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			up, _ := listingUpstream(t, body)
			hub := namedHub(t, []string{"m"}, entry("m", "openai/m", up, nil))
			dec, err := resolveFast(t, hubConfig([]string{"hub"}, hubBackend("hub", hub)), true)
			if err != nil || len(dec.Upstreams) != 1 || !dec.Upstreams[0].OK || dec.Upstreams[0].Message != "answered" {
				t.Fatalf("decision=%+v err=%v", dec, err)
			}
		})
	}
}

// Two deployments of one alias on one server: one fetch, both checked, and the
// one that is listed serves the alias.
func TestUpstreamOneFetchPerAPIBase(t *testing.T) {
	up, hits := listingUpstream(t, listing("m"))
	hub := namedHub(t, []string{"m"}, entry("m", "openai/gone", up, nil), entry("m", "openai/m", up, nil))
	dec, err := resolveFast(t, hubConfig([]string{"hub"}, hubBackend("hub", hub)), true)
	if err != nil || !dec.Healthy {
		t.Fatalf("decision=%+v err=%v", dec, err)
	}
	if hits.Load() != 1 {
		t.Fatalf("upstream fetched %d times, want 1", hits.Load())
	}
	if len(dec.Upstreams) != 2 || !dec.Upstreams[0].Unlisted || !dec.Upstreams[1].OK {
		t.Fatalf("upstreams=%+v want gone unlisted, then m ok", dec.Upstreams)
	}
}

// LiteLLM routes on the exact alias, so a healthy sibling alias that differs by
// a tag must not pass a hub whose requested alias cannot serve.
func TestResolveRole_ExactAliasEntriesWin(t *testing.T) {
	gone, _ := listingUpstream(t, listing("other-model:7b"))
	live, _ := listingUpstream(t, listing("coder:7b"))
	hub := namedHub(t, []string{"coder", "coder:7b"},
		entry("coder", "openai/coder", gone, nil),
		entry("coder:7b", "openai/coder:7b", live, nil))
	dec, err := resolveModel(t, &config.AIConfig{Backends: []config.AIBackendConfig{hubBackend("hub", hub)}}, "coder", true)
	if !errors.Is(err, llmrouter.ErrModelUnlisted) || !strings.Contains(err.Error(), "no upstream serves it") {
		t.Fatalf("err=%v decision=%+v", err, dec)
	}
	if len(dec.Upstreams) != 1 || dec.Upstreams[0].APIBase != gone {
		t.Fatalf("upstreams=%+v want only the exact alias's deployment", dec.Upstreams)
	}
}

// One hub whose upstream is dead and one whose upstream lacks the model: the
// strict error names both reasons.
func TestResolveRole_DeadAndUnlistedHubsNameBoth(t *testing.T) {
	dead := fakeHub(t, map[string]string{"m": deadUpstream(t)})
	up, _ := listingUpstream(t, listing("other-model:latest"))
	unlisted := namedHub(t, []string{"m"}, entry("m", "openai/m", up, nil))
	cfg := hubConfig([]string{"dead", "unlisted"}, hubBackend("dead", dead), hubBackend("unlisted", unlisted))
	_, err := resolveFast(t, cfg, true)
	if !errors.Is(err, llmrouter.ErrModelUnlisted) || !strings.HasSuffix(err.Error(), "(listed, but no upstream answers or serves it)") {
		t.Fatalf("strict error = %v", err)
	}
}
