package facts

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestInferTurboQuantSupport_MLXOnAppleSilicon(t *testing.T) {
	info := inferTurboQuantSupport("darwin", "arm64", []models.ToolInfo{
		{Name: "mlx_lm", Path: "/opt/homebrew/bin/mlx_lm"},
	}, &models.Resources{}, &models.OllamaInfo{Installed: true})
	if info == nil || !info.Supported {
		t.Fatal("expected mlx turboquant support to be detected")
	}
	if len(info.Backends) != 1 || info.Backends[0] != "mlx" {
		t.Fatalf("backends = %v, want [mlx]", info.Backends)
	}
	if info.Verified {
		t.Fatal("pure inference should not mark backend verified")
	}
	if !containsCapability(info.Capabilities, "apple-silicon") {
		t.Fatalf("expected apple-silicon capability, got %v", info.Capabilities)
	}
}

func TestDetectTurboQuantSupport_VerifiesMLXProbe(t *testing.T) {
	info := detectTurboQuantSupport(
		context.Background(),
		"darwin",
		"arm64",
		[]models.ToolInfo{{Name: "mlx_lm", Path: "/opt/homebrew/bin/mlx_lm"}},
		&models.Resources{},
		&models.OllamaInfo{Installed: true},
		func(ctx context.Context, cmd string) (string, error) {
			if !strings.Contains(cmd, "mlx_lm") {
				t.Fatalf("unexpected probe cmd: %s", cmd)
			}
			return "mlx_lm generate --help\nmlx_lm serve --help\n", nil
		},
	)
	if info == nil || !info.Verified {
		t.Fatalf("expected verified mlx support, got %+v", info)
	}
	for _, want := range []string{"backend-probed", "generate-mode", "server-mode", "mlx-runtime"} {
		if !containsProbeCapability(info.Capabilities, want) {
			t.Fatalf("expected capability %q in %v", want, info.Capabilities)
		}
	}
}

func TestDetectTurboQuantSupport_VerifiesLlamaProbeAndFlags(t *testing.T) {
	info := detectTurboQuantSupport(
		context.Background(),
		"linux",
		"amd64",
		[]models.ToolInfo{{Name: "llama-server", Path: "/usr/bin/llama-server"}},
		&models.Resources{GPUs: []string{"RTX 4090"}},
		nil,
		func(ctx context.Context, cmd string) (string, error) {
			if !strings.Contains(cmd, "llama-server") {
				t.Fatalf("unexpected probe cmd: %s", cmd)
			}
			return "llama.cpp server --ctx-size --n-gpu-layers --flash-attn --kv-cache\n", nil
		},
	)
	if info == nil || !info.Verified {
		t.Fatalf("expected verified llama.cpp support, got %+v", info)
	}
	for _, want := range []string{"backend-probed", "ctx-size-flag", "gpu-layers-flag", "flash-attn-flag", "kv-cache-controls", "llama.cpp-runtime"} {
		if !containsProbeCapability(info.Capabilities, want) {
			t.Fatalf("expected capability %q in %v", want, info.Capabilities)
		}
	}
}

func TestDetectTurboQuantSupport_LlamaRequiresCtxSizeForVerification(t *testing.T) {
	info := detectTurboQuantSupport(
		context.Background(),
		"linux",
		"amd64",
		[]models.ToolInfo{{Name: "llama-server", Path: "/usr/bin/llama-server"}},
		&models.Resources{GPUs: []string{"RTX 4090"}},
		nil,
		func(ctx context.Context, cmd string) (string, error) {
			if !strings.Contains(cmd, "llama-server") {
				t.Fatalf("unexpected probe cmd: %s", cmd)
			}
			return "llama.cpp server --flash-attn --n-gpu-layers --kv-cache\n", nil
		},
	)
	if info == nil || !info.Supported {
		t.Fatalf("expected supported llama.cpp support, got %+v", info)
	}
	if info.Verified {
		t.Fatalf("expected missing --ctx-size capability to remain unverified, got %+v", info)
	}
	if !containsProbeCapability(info.Capabilities, "flash-attn-flag") {
		t.Fatalf("expected flash-attn capability to still be recorded, got %v", info.Capabilities)
	}
}

func TestDetectTurboQuantSupport_DetectedButUnverified(t *testing.T) {
	info := detectTurboQuantSupport(
		context.Background(),
		"linux",
		"amd64",
		[]models.ToolInfo{{Name: "llama-server", Path: "/usr/bin/llama-server"}},
		&models.Resources{},
		nil,
		func(ctx context.Context, cmd string) (string, error) {
			return "usage: helper wrapper", nil
		},
	)
	if info == nil || !info.Supported {
		t.Fatal("expected supported backend")
	}
	if info.Verified {
		t.Fatal("expected unverified backend when probe output is not recognizable")
	}
}

func TestDetectTurboQuantSupport_ProbeFailureFallsBackToDetected(t *testing.T) {
	info := detectTurboQuantSupport(
		context.Background(),
		"linux",
		"amd64",
		[]models.ToolInfo{{Name: "llama-server", Path: "/usr/bin/llama-server"}},
		&models.Resources{},
		nil,
		func(ctx context.Context, cmd string) (string, error) {
			return "", fmt.Errorf("boom")
		},
	)
	if info == nil || !info.Supported {
		t.Fatal("expected detected backend")
	}
	if info.Verified {
		t.Fatal("expected failed probe to remain unverified")
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

func containsProbeCapability(caps []string, want string) bool {
	for _, cap := range caps {
		if cap == want {
			return true
		}
	}
	return false
}
