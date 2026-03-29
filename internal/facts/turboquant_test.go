package facts

import (
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestInferTurboQuantSupport_MLXOnAppleSilicon(t *testing.T) {
	info := inferTurboQuantSupport("darwin", "arm64", []models.ToolInfo{
		{Name: "mlx_lm", Path: "/opt/homebrew/bin/mlx_lm", Version: "usage: mlx_lm [OPTIONS]"},
	}, &models.Resources{}, &models.OllamaInfo{Installed: true})
	if info == nil || !info.Supported {
		t.Fatal("expected mlx turboquant support to be detected")
	}
	if len(info.Backends) != 1 || info.Backends[0] != "mlx" {
		t.Fatalf("backends = %v, want [mlx]", info.Backends)
	}
	if !info.Verified {
		t.Fatal("expected mlx backend to be marked verified from probe output")
	}
	if len(info.Capabilities) == 0 {
		t.Fatal("expected capabilities to be populated")
	}
}

func TestInferTurboQuantSupport_LlamaCPP(t *testing.T) {
	info := inferTurboQuantSupport("linux", "amd64", []models.ToolInfo{
		{Name: "llama-server", Path: "/usr/bin/llama-server", Version: "0.0.1"},
	}, &models.Resources{GPUs: []string{"RTX 4090"}}, nil)
	if info == nil || !info.Supported {
		t.Fatal("expected llama.cpp turboquant support to be detected")
	}
	if len(info.Backends) != 1 || info.Backends[0] != "llama.cpp" {
		t.Fatalf("backends = %v, want [llama.cpp]", info.Backends)
	}
	if !info.Verified {
		t.Fatal("expected llama.cpp backend to be verified")
	}
}

func TestInferTurboQuantSupport_DetectedButUnverified(t *testing.T) {
	info := inferTurboQuantSupport("linux", "amd64", []models.ToolInfo{
		{Name: "llama-server", Path: "/usr/bin/llama-server"},
	}, &models.Resources{}, nil)
	if info == nil || !info.Supported {
		t.Fatal("expected supported backend")
	}
	if info.Verified {
		t.Fatal("expected backend without probe output to remain unverified")
	}
}

func TestInferTurboQuantSupport_None(t *testing.T) {
	info := inferTurboQuantSupport("linux", "amd64", []models.ToolInfo{
		{Name: "ollama"},
	}, &models.Resources{}, nil)
	if info != nil {
		t.Fatalf("expected nil turboquant info, got %+v", info)
	}
}
