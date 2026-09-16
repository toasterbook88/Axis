package chat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChoosePreferredModel(t *testing.T) {
	got, ok := ChoosePreferredModel([]string{"llama3.2:3b", "qwen3:0.6b"})
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
	got, ok := ChoosePreferredModel(installed)
	if !ok {
		t.Fatal("expected ok=true when installed models exist but none are recommended")
	}
	if got != "llama3.2:latest" {
		t.Fatalf("expected deterministic installed model %q, got %q", "llama3.2:latest", got)
	}
}

// TestChoosePreferredModelEmptyReturnsFalse confirms the only case where
// ChoosePreferredModel legitimately returns false: no models installed at all.
func TestChoosePreferredModelEmptyReturnsFalse(t *testing.T) {
	_, ok := ChoosePreferredModel(nil)
	if ok {
		t.Fatal("expected ok=false for empty installed list")
	}
	_, ok = ChoosePreferredModel([]string{})
	if ok {
		t.Fatal("expected ok=false for empty installed list")
	}
}

func TestChoosePreferredModelPrefersToolCapable(t *testing.T) {
	// pickToolCapable should prefer a tool-capable model over a
	// non-tool-capable one even when the non-tool model is first
	// alphabetically.
	got, ok := ChoosePreferredModel([]string{"gemma3n:e2b", "llama3.1:8b", "qwen3:4b"})
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
	got, ok := ChoosePreferredModel([]string{"all-minilm:latest", "gemma3n:e2b"})
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
