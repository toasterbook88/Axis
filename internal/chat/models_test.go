package chat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestChoosePreferredModel(t *testing.T) {
	got, ok := choosePreferredModel([]string{"llama3.2:3b", "qwen3:0.6b"})
	if !ok {
		t.Fatal("expected preferred model match")
	}
	if got != "qwen3:0.6b" {
		t.Fatalf("expected qwen3:0.6b, got %s", got)
	}
}

func TestFormatModelCatalogIncludesCloudHint(t *testing.T) {
	out := FormatModelCatalog(ModelCatalog{
		Current:            "qwen3:1.7b",
		Default:            "qwen3:1.7b",
		InstalledAvailable: true,
		Installed:          []string{"qwen3:1.7b"},
		RecommendedLocal:   recommendedLocalModels,
		RecommendedCloud:   recommendedCloudModels,
	})

	if !strings.Contains(out, "qwen3-coder:480b-cloud") {
		t.Fatalf("expected cloud model listing, got %q", out)
	}
	if !strings.Contains(out, "/model <tag>") {
		t.Fatalf("expected switch hint, got %q", out)
	}
	if !strings.Contains(out, "[installed, default]") {
		t.Fatalf("expected default marker, got %q", out)
	}
}

func TestResolveDefaultModelPrefersInstalledRecommendedModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:0.6b"},{"name":"custom:latest"}]}`))
	}))
	defer server.Close()

	restore := stubDefaultHTTPClient(t, rewriteClientToServer(t, server.URL))
	defer restore()

	if got := ResolveDefaultModel(context.Background()); got != "qwen3:0.6b" {
		t.Fatalf("ResolveDefaultModel() = %q, want qwen3:0.6b", got)
	}
}

func TestResolveDefaultModelFallsBackWhenModelListingFails(t *testing.T) {
	restore := stubDefaultHTTPClient(t, &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("boom")
		}),
	})
	defer restore()

	if got := ResolveDefaultModel(context.Background()); got != recommendedLocalModels[0].Name {
		t.Fatalf("ResolveDefaultModel() = %q, want fallback %q", got, recommendedLocalModels[0].Name)
	}
}

func TestBuildModelCatalogIncludesInstalledState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:1.7b"},{"name":"custom:latest"}]}`))
	}))
	defer server.Close()

	restore := stubDefaultHTTPClient(t, rewriteClientToServer(t, server.URL))
	defer restore()

	catalog := BuildModelCatalog(context.Background(), "custom:latest")
	if !catalog.InstalledAvailable {
		t.Fatal("expected installed models to be available")
	}
	if catalog.Default != "qwen3:1.7b" {
		t.Fatalf("catalog.Default = %q, want qwen3:1.7b", catalog.Default)
	}
	if len(catalog.Installed) != 2 {
		t.Fatalf("expected 2 installed models, got %v", catalog.Installed)
	}
}

func TestListInstalledModelsReturnsStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	_, err := listInstalledModels(context.Background(), server.URL)
	if err == nil {
		t.Fatal("expected status error")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("expected 502 status in error, got %v", err)
	}
}

func TestNewEngineCreatesHybridEngineWithRequestedModel(t *testing.T) {
	engine := NewEngine("phi4")
	hybrid, ok := engine.(*HybridEngine)
	if !ok {
		t.Fatalf("expected HybridEngine, got %T", engine)
	}
	if hybrid.model != "phi4" {
		t.Fatalf("HybridEngine model = %q, want phi4", hybrid.model)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func stubDefaultHTTPClient(t *testing.T, client *http.Client) func() {
	t.Helper()
	prev := http.DefaultClient
	http.DefaultClient = client
	return func() {
		http.DefaultClient = prev
	}
}

func rewriteClientToServer(t *testing.T, rawURL string) *http.Client {
	t.Helper()
	target, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	base := http.DefaultTransport
	return &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			req = req.Clone(req.Context())
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			return base.RoundTrip(req)
		}),
	}
}
