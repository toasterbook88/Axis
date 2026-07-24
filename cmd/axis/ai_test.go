package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAICommandSurfaceWiresSubcommands(t *testing.T) {
	cmd := aiCmd()
	if got := cmd.Name(); got != "ai" {
		t.Fatalf("name=%q", got)
	}
	for _, name := range []string{"backends", "roles", "route"} {
		if _, _, err := cmd.Find([]string{name}); err != nil {
			t.Fatalf("missing %q: %v", name, err)
		}
	}
}

func TestAIRouteSkipProbeText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ai.yaml")
	body := `
backends:
  - name: local-ollama
    kind: ollama
    base_url: http://127.0.0.1:11434
    node: node-a
roles:
  default:
    prefer: [local-ollama]
    model: coder:latest
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := aiCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"route", "default", "--ai-config", path, "--skip-probe"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\n%s", err, stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "backend:    local-ollama") {
		t.Fatalf("output:\n%s", out)
	}
	if !strings.Contains(out, "dry-run") {
		t.Fatalf("expected dry-run banner:\n%s", out)
	}
}

func TestAIRolesListsConfigured(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ai.yaml")
	body := `
backends:
  - name: hub
    kind: openai-compatible
    base_url: http://127.0.0.1:4000/v1
roles:
  fast:
    prefer: [hub]
    model: fast-chat
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := aiCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"roles", "--ai-config", path})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stdout.String(), "fast") || !strings.Contains(stdout.String(), "fast-chat") {
		t.Fatalf("output:\n%s", stdout.String())
	}
}
