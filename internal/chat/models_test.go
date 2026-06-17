package chat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
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

// TestChoosePreferredModelFallsBackToSortedInstalled verifies that when none
// of the hardcoded recommended models are present, the lexicographically first
// installed model is returned instead of ("", false). listInstalledModels
// sorts the installed set so this fallback stays deterministic across runs.
func TestChoosePreferredModelFallsBackToSortedInstalled(t *testing.T) {
	installed := []string{"llama3.2:latest", "mistral:7b", "qwen3:4b"}
	got, ok := choosePreferredModel(installed)
	if !ok {
		t.Fatal("expected ok=true when installed models exist but none are recommended")
	}
	if got != "llama3.2:latest" {
		t.Fatalf("expected deterministic installed model %q, got %q", "llama3.2:latest", got)
	}
}

// TestChoosePreferredModelEmptyReturnsFalse confirms the only case where
// choosePreferredModel legitimately returns false: no models installed at all.
func TestChoosePreferredModelEmptyReturnsFalse(t *testing.T) {
	_, ok := choosePreferredModel(nil)
	if ok {
		t.Fatal("expected ok=false for empty installed list")
	}
	_, ok = choosePreferredModel([]string{})
	if ok {
		t.Fatal("expected ok=false for empty installed list")
	}
}

func TestChoosePreferredModelPrefersToolCapable(t *testing.T) {
	// pickToolCapable should prefer a tool-capable model over a
	// non-tool-capable one even when the non-tool model is first
	// alphabetically.
	got, ok := choosePreferredModel([]string{"gemma3n:e2b", "llama3.1:8b", "qwen3:4b"})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != "llama3.1:8b" {
		t.Fatalf("expected tool-capable fallback llama3.1:8b, got %q", got)
	}
}

func TestChoosePreferredModelFallsBackAlphabeticallyWhenNoToolCapable(t *testing.T) {
	// When none of the installed models match a known tool-capable family,
	// the fallback returns the alphabetically first model.
	// Note: listInstalledModels sorts results, so pass them sorted.
	got, ok := choosePreferredModel([]string{"all-minilm:latest", "gemma3n:e2b"})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got != "all-minilm:latest" {
		t.Fatalf("expected alphabetical fallback, got %q", got)
	}
}

// TestResolveDefaultModelPicksDeterministicInstalledWhenNoneRecommended
// verifies the full ResolveDefaultModel path for a node that has its own
// models but none from the recommended list.
func TestResolveDefaultModelPicksDeterministicInstalledWhenNoneRecommended(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:4b"},{"name":"llama3.2:latest"}]}`))
	}))
	defer server.Close()

	restore := stubDefaultHTTPClient(t, rewriteClientToServer(t, server.URL))
	defer restore()

	got := ResolveDefaultModel(context.Background())
	// Should pick the deterministic sorted fallback rather than the hardcoded
	// "qwen3:1.7b" which is absent.
	if got == recommendedLocalModels[0].Name {
		t.Fatalf("ResolveDefaultModel() = %q — selected hardcoded fallback that is not installed; want deterministic installed model", got)
	}
	if got != "llama3.2:latest" {
		t.Fatalf("ResolveDefaultModel() = %q, want deterministic installed model %q", got, "llama3.2:latest")
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

func TestListInstalledModelsSortsResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:4b"},{"name":"llama3.2:latest"},{"name":"mistral:7b"}]}`))
	}))
	defer server.Close()

	got, err := listInstalledModels(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("listInstalledModels() error = %v", err)
	}

	want := []string{"llama3.2:latest", "mistral:7b", "qwen3:4b"}
	if !slices.Equal(got, want) {
		t.Fatalf("listInstalledModels() = %v, want %v", got, want)
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

func TestIsModelToolCapable(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"qwen3.5:4b", true},
		{"llama3.1:8b", true},
		{"gemma3n:e2b", false},
		{"custom-unknown", false},
		{"", false},
	}

	for _, tt := range tests {
		got := IsModelToolCapable(tt.model)
		if got != tt.want {
			t.Errorf("IsModelToolCapable(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}
