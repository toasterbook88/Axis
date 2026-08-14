package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
)

func writeAI(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ai.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestLoadAI_ValidExampleShape(t *testing.T) {
	path := writeAI(t, `
backends:
  - name: local-ollama
    kind: ollama
    base_url: http://127.0.0.1:11434
    node: node-a
  - name: local-hub
    kind: openai-compatible
    base_url: http://127.0.0.1:4000/v1
    enabled: true
roles:
  default:
    prefer: [local-hub, local-ollama]
    model: coder:latest
  fast:
    prefer: [local-hub]
    model: fast-chat
`)
	cfg, err := config.LoadAI(path)
	if err != nil {
		t.Fatalf("LoadAI: %v", err)
	}
	if len(cfg.Backends) != 2 {
		t.Fatalf("backends=%d", len(cfg.Backends))
	}
	if !cfg.Backends[0].IsEnabled() {
		t.Fatal("expected default enabled")
	}
	if cfg.Roles["default"].Model != "coder:latest" {
		t.Fatalf("model=%q", cfg.Roles["default"].Model)
	}
}

func TestLoadAI_MissingFileOrEmpty(t *testing.T) {
	cfg, err := config.LoadAIOrEmpty(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("LoadAIOrEmpty: %v", err)
	}
	if len(cfg.Backends) != 0 || len(cfg.Roles) != 0 {
		t.Fatalf("expected empty config, got %+v", cfg)
	}
}

func TestLoadAI_UnknownFieldRejected(t *testing.T) {
	path := writeAI(t, `
backends:
  - name: x
    kind: ollama
    base_url: http://127.0.0.1:11434
    not_a_field: true
roles: {}
`)
	if _, err := config.LoadAI(path); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestLoadAI_UnknownBackendInPrefer(t *testing.T) {
	path := writeAI(t, `
backends:
  - name: local-ollama
    kind: ollama
    base_url: http://127.0.0.1:11434
roles:
  default:
    prefer: [missing]
    model: m
`)
	_, err := config.LoadAI(path)
	if err == nil || !strings.Contains(err.Error(), "unknown backend") {
		t.Fatalf("expected unknown backend error, got %v", err)
	}
}

func TestLoadAI_UnsupportedKind(t *testing.T) {
	path := writeAI(t, `
backends:
  - name: bad
    kind: magic
    base_url: http://127.0.0.1:1
roles: {}
`)
	_, err := config.LoadAI(path)
	if err == nil || !strings.Contains(err.Error(), "unsupported kind") {
		t.Fatalf("expected unsupported kind, got %v", err)
	}
}

func TestLoadAI_DisabledBackend(t *testing.T) {
	path := writeAI(t, `
backends:
  - name: off
    kind: ollama
    base_url: http://127.0.0.1:11434
    enabled: false
roles: {}
`)
	cfg, err := config.LoadAI(path)
	if err != nil {
		t.Fatalf("LoadAI: %v", err)
	}
	if cfg.Backends[0].IsEnabled() {
		t.Fatal("expected disabled")
	}
	if len(cfg.BackendByName(true)) != 0 {
		t.Fatal("enabledOnly map should be empty")
	}
}

func TestLoadAI_AdvertiseURL(t *testing.T) {
	path := writeAI(t, `
backends:
  - name: local-nemotron
    kind: openai-compatible
    base_url: http://127.0.0.1:8081/v1
    advertise_url: http://nemotron.lan.axismcp.org/v1
    node: cranium
roles:
  nemotron:
    prefer: [local-nemotron]
    model: nemotron-3.5-lightning
`)
	cfg, err := config.LoadAI(path)
	if err != nil {
		t.Fatalf("LoadAI: %v", err)
	}
	if len(cfg.Backends) != 1 {
		t.Fatalf("backends=%d", len(cfg.Backends))
	}
	got := cfg.Backends[0].AdvertiseURL
	want := "http://nemotron.lan.axismcp.org/v1"
	if got != want {
		t.Fatalf("AdvertiseURL = %q, want %q", got, want)
	}
}
