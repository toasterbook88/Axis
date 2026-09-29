package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/secrets"
)

// TestSchemaProbeIsolatesBearerResolution isolates which stage drops the key:
// yaml parse, kind validation, or secrets resolution.
func TestSchemaProbeIsolatesBearerResolution(t *testing.T) {
	dir := t.TempDir()
	kf := filepath.Join(dir, "k")
	if err := os.WriteFile(kf, []byte("e2e-probe-key\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	apiPath := filepath.Join(dir, "ai.yaml")
	aiYaml := "backends:\n- name: test-hub\n  kind: openai-compatible\n  base_url: http://example.invalid/v1\n  api_key_file: " + kf + "\n"
	if err := os.WriteFile(apiPath, []byte(aiYaml), 0o600); err != nil {
		t.Fatalf("write ai.yaml: %v", err)
	}

	cfg, err := config.LoadAIOrEmpty(apiPath)
	if err != nil {
		t.Fatalf("LoadAIOrEmpty: %v", err)
	}
	if cfg == nil {
		t.Fatal("cfg is nil")
	}
	t.Logf("parsed backends=%d", len(cfg.Backends))
	for _, b := range cfg.Backends {
		t.Logf("  backend name=%q kind=%q base_url=%q key_file=%q key_env=%q",
			b.Name, string(b.Kind), b.BaseURL, b.APIKeyFile, b.APIKeyEnv)
	}
	if len(cfg.Backends) == 0 {
		t.Fatal("no backends parsed - the yaml schema/validation rejects this shape")
	}
	key, err := secrets.ResolveOrEmpty(cfg.Backends[0].APIKeyEnv, cfg.Backends[0].APIKeyFile)
	if err != nil {
		t.Fatalf("secrets.ResolveOrEmpty: %v", err)
	}
	t.Logf("resolved key len=%d", len(key))
	if key == "" {
		t.Fatal("key resolution returned empty")
	}
}
