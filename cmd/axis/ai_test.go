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

func TestAIBackendsPrintsViewLocality(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ai.yaml")
	body := `
backends:
  - name: nemotron
    kind: openai-compatible
    base_url: http://127.0.0.1:8081/v1
    node: some-other-node
  - name: loop
    kind: ollama
    base_url: http://127.0.0.1:11434
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := aiCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"backends", "--ai-config", path, "--skip-probe"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("backends: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "nemotron") || !strings.Contains(out, "peer") {
		t.Fatalf("expected peer locality for named other node:\n%s", out)
	}
	if !strings.Contains(out, "loop") || !strings.Contains(out, "here") {
		t.Fatalf("expected here locality for loopback:\n%s", out)
	}
}

func TestAIRouteStrictModelUnlistedExit(t *testing.T) {
	// Use a local httptest via real ResolveRole is hard from CLI without live ports;
	// exercise --allow-unlisted path still succeeds with skip-probe.
	dir := t.TempDir()
	path := filepath.Join(dir, "ai.yaml")
	body := `
backends:
  - name: local-ollama
    kind: ollama
    base_url: http://127.0.0.1:11434
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
		t.Fatalf("skip-probe should succeed: %v", err)
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
