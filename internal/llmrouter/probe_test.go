package llmrouter_test

import (
	"context"
	"net/http"
	"net/http/httptest"
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
