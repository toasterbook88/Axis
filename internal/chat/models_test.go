package chat

import (
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
