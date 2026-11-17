package llmrouter_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
)

func TestViewLocality(t *testing.T) {
	here := llmrouter.ViewLocality(config.AIBackendConfig{
		Name:    "nemotron",
		Kind:    config.AIBackendOpenAICompatible,
		BaseURL: "http://127.0.0.1:8081/v1",
	})
	if here != "here" {
		t.Fatalf("loopback untitled node = %q, want here", here)
	}
	peer := llmrouter.ViewLocality(config.AIBackendConfig{
		Name:    "nemotron",
		Kind:    config.AIBackendOpenAICompatible,
		BaseURL: "http://127.0.0.1:8081/v1",
		Node:    "some-other-node",
	})
	if peer != "peer" {
		t.Fatalf("named other node = %q, want peer", peer)
	}
	cloud := llmrouter.ViewLocality(config.AIBackendConfig{
		Name:    "openrouter",
		Kind:    config.AIBackendOpenAICompatible,
		BaseURL: "https://openrouter.ai/api/v1",
	})
	if cloud != "cloud" {
		t.Fatalf("public URL no node = %q, want cloud", cloud)
	}
	logicalLocal := llmrouter.ViewLocalityForNodes(config.AIBackendConfig{
		Name:    "nemotron",
		Kind:    config.AIBackendOpenAICompatible,
		BaseURL: "http://127.0.0.1:8081/v1",
		Node:    "node-a",
	}, &config.Config{Nodes: []config.NodeConfig{{
		Name:     "node-a",
		Hostname: "127.0.0.1",
	}}})
	if logicalLocal != "here" {
		t.Fatalf("logical local node = %q, want here", logicalLocal)
	}
}

func TestProbeBackend_OllamaTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"coder:latest"},{"name":"small:latest"}]}`))
	}))
	t.Cleanup(srv.Close)

	b := config.AIBackendConfig{
		Name:    "local-ollama",
		Kind:    config.AIBackendOllama,
		BaseURL: srv.URL,
	}
	p := llmrouter.ProbeBackend(context.Background(), b, srv.Client())
	if !p.OK {
		t.Fatalf("probe: %+v", p)
	}
	if len(p.Models) != 2 {
		t.Fatalf("models=%v", p.Models)
	}
	if p.Latency <= 0 || p.Latency > time.Second {
		t.Fatalf("latency=%v", p.Latency)
	}
}

func TestProbeBackend_OpenAICompatibleModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"fast-chat"},{"id":"long-context"}]}`))
	}))
	t.Cleanup(srv.Close)

	b := config.AIBackendConfig{
		Name:    "hub",
		Kind:    config.AIBackendOpenAICompatible,
		BaseURL: srv.URL + "/v1",
	}
	p := llmrouter.ProbeBackend(context.Background(), b, srv.Client())
	if !p.OK {
		t.Fatalf("probe: %+v", p)
	}
	if !llmrouter.ModelListed(p.Models, "fast-chat") {
		t.Fatalf("missing fast-chat in %v", p.Models)
	}
}

func TestProbeBackend_OffBoxUsesAdvertiseURL(t *testing.T) {
	// The backend belongs to another node: base_url is loopback on that
	// node, advertise_url is the reachable Host. The probe must hit the
	// advertise_url, not the loopback base_url.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"nemotron"}]}`))
	}))
	t.Cleanup(srv.Close)

	b := config.AIBackendConfig{
		Name:         "local-nemotron",
		Kind:         config.AIBackendOpenAICompatible,
		BaseURL:      "http://127.0.0.1:8081/v1",
		AdvertiseURL: srv.URL + "/v1",
		Node:         "some-other-node",
	}
	p := llmrouter.ProbeBackend(context.Background(), b, srv.Client())
	if !p.OK {
		t.Fatalf("probe via advertise_url: %+v", p)
	}
	if !llmrouter.ModelListed(p.Models, "nemotron") {
		t.Fatalf("missing nemotron in %v", p.Models)
	}
}

func TestProbeBackend_LocalNodeBindingKeepsBaseURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"nemotron"}]}`))
	}))
	t.Cleanup(srv.Close)

	b := config.AIBackendConfig{
		Name:         "local-nemotron",
		Kind:         config.AIBackendOpenAICompatible,
		BaseURL:      srv.URL + "/v1",
		AdvertiseURL: "http://127.0.0.1:1/v1",
		Node:         "node-a",
	}
	nodes := &config.Config{Nodes: []config.NodeConfig{{
		Name:     "node-a",
		Hostname: "127.0.0.1",
	}}}
	p := llmrouter.ProbeBackendForNodes(context.Background(), b, srv.Client(), nodes)
	if !p.OK {
		t.Fatalf("probe via base_url: %+v", p)
	}
	if p.ProbedURL != srv.URL+"/v1/models" {
		t.Fatalf("probed URL = %q, want local base_url", p.ProbedURL)
	}
}

func TestProbeAllBackends_IndependentBudgets(t *testing.T) {
	// Two hangers that wait on the request context, then a healthy backend.
	// A shared parent deadline used to cancel later probes; each backend now
	// gets its own timeout, and once the overall budget is spent later rows
	// are Skipped (not a false observed failure).
	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(hang.Close)
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"alive"}]}`))
	}))
	t.Cleanup(ok.Close)

	cfg := &config.AIConfig{Backends: []config.AIBackendConfig{
		{Name: "hang-a", Kind: config.AIBackendOpenAICompatible, BaseURL: hang.URL + "/v1"},
		{Name: "hang-b", Kind: config.AIBackendOpenAICompatible, BaseURL: hang.URL + "/v1"},
		{Name: "alive", Kind: config.AIBackendOpenAICompatible, BaseURL: ok.URL + "/v1"},
	}}
	// Parent shorter than two per-backend budgets (3s each): first hang is
	// observed; later rows are Skipped after the parent expires.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	probes := llmrouter.ProbeAllBackendsForNodes(ctx, cfg, nil, nil)
	if len(probes) != 3 {
		t.Fatalf("probes=%d, want 3", len(probes))
	}
	if probes[0].Skipped || probes[0].OK {
		t.Fatalf("hang-a should be an observed failure, got %+v", probes[0])
	}
	if !probes[2].Skipped {
		t.Fatalf("alive must be Skipped after overall budget is spent, got %+v", probes[2])
	}
}

func TestProbeAllBackends_HealthyAfterHangsWhenBudgetRemains(t *testing.T) {
	hang := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(hang.Close)
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"alive"}]}`))
	}))
	t.Cleanup(ok.Close)

	cfg := &config.AIConfig{Backends: []config.AIBackendConfig{
		{Name: "hang-a", Kind: config.AIBackendOpenAICompatible, BaseURL: hang.URL + "/v1"},
		{Name: "hang-b", Kind: config.AIBackendOpenAICompatible, BaseURL: hang.URL + "/v1"},
		{Name: "alive", Kind: config.AIBackendOpenAICompatible, BaseURL: ok.URL + "/v1"},
	}}
	// Parent covers two 3s hang budgets plus the healthy probe.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	probes := llmrouter.ProbeAllBackendsForNodes(ctx, cfg, nil, nil)
	alive := probes[2]
	if alive.Skipped || !alive.OK {
		t.Fatalf("alive should be probed OK when budget remains, got %+v", alive)
	}
}

func TestProbeAllBackends_SkippedWhenParentDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cfg := &config.AIConfig{Backends: []config.AIBackendConfig{
		{Name: "a", Kind: config.AIBackendOpenAICompatible, BaseURL: "http://127.0.0.1:9/v1"},
		{Name: "b", Kind: config.AIBackendOpenAICompatible, BaseURL: "http://127.0.0.1:9/v1"},
	}}
	probes := llmrouter.ProbeAllBackendsForNodes(ctx, cfg, nil, nil)
	if len(probes) != 2 {
		t.Fatalf("probes=%d", len(probes))
	}
	for _, p := range probes {
		if !p.Skipped || p.OK {
			t.Fatalf("want skipped failure, got %+v", p)
		}
		if !strings.Contains(p.Message, "probe skipped") {
			t.Fatalf("message=%q", p.Message)
		}
	}
}

func TestProbeBackend_Disabled(t *testing.T) {
	off := false
	b := config.AIBackendConfig{
		Name:    "off",
		Kind:    config.AIBackendOllama,
		BaseURL: "http://127.0.0.1:9",
		Enabled: &off,
	}
	p := llmrouter.ProbeBackend(context.Background(), b, nil)
	if p.OK || p.Message != "disabled" {
		t.Fatalf("got %+v", p)
	}
}

func TestProbeBackend_Unreachable(t *testing.T) {
	b := config.AIBackendConfig{
		Name:    "down",
		Kind:    config.AIBackendOllama,
		BaseURL: "http://127.0.0.1:1",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	p := llmrouter.ProbeBackend(ctx, b, &http.Client{Timeout: 200 * time.Millisecond})
	if p.OK {
		t.Fatal("expected failure")
	}
}

func TestModelListedTags(t *testing.T) {
	cases := []struct {
		listed []string
		model  string
		want   bool
	}{
		{[]string{"coder:latest"}, "coder", true},
		{[]string{"coder"}, "coder:latest", true},
		{[]string{"coder:small"}, "coder:small", true},
		// Two different explicit tags are different models (#577 review).
		{[]string{"coder:large"}, "coder:small", false},
		{[]string{"coder:large", "coder:small"}, "coder:small", true},
		// A name without a tag still matches the same name with any tag.
		{[]string{"coder:7b"}, "coder", true},
		{[]string{"coder"}, "coder:7b", true},
		{[]string{"other:latest"}, "coder", false},
		// A registry port is not a tag: only the last path segment carries one.
		{[]string{"registry.example:5000/team/coder:latest"}, "registry.example:5000/team/coder", true},
		{[]string{"registry.example:5000/team/coder"}, "registry.example:5000/team/coder:latest", true},
		{[]string{"registry.example:5000/team/coder:large"}, "registry.example:5000/team/coder:small", false},
		{[]string{"registry.example:5000/team/other"}, "registry.example:5000/team/coder", false},
		{[]string{"coder:latest"}, "", false},
	}
	for _, c := range cases {
		if got := llmrouter.ModelListed(c.listed, c.model); got != c.want {
			t.Errorf("ModelListed(%q, %q) = %v, want %v", c.listed, c.model, got, c.want)
		}
	}
}
