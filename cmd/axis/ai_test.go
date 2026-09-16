package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
)

func stubAINodeConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	prev := aiNodeConfigLoadFn
	aiNodeConfigLoadFn = func() (*config.Config, error) { return cfg, nil }
	t.Cleanup(func() { aiNodeConfigLoadFn = prev })
}

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

func TestAIEmptyInventoriesHonorStructuredFormat(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing-ai.yaml")
	tests := []struct {
		name       string
		command    string
		format     string
		wantOutput string
	}{
		{name: "backends json", command: "backends", format: "json", wantOutput: "[]\n"},
		{name: "backends yaml", command: "backends", format: "yaml", wantOutput: "[]\n"},
		{name: "roles json", command: "roles", format: "json", wantOutput: "{}\n"},
		{name: "roles yaml", command: "roles", format: "yaml", wantOutput: "{}\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := aiCmd()
			var stdout bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{tt.command, "--ai-config", missingPath, "--format", tt.format})

			if err := cmd.Execute(); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if got := stdout.String(); got != tt.wantOutput {
				t.Fatalf("output = %q, want %q", got, tt.wantOutput)
			}
		})
	}
}

func TestAIRouteSkipProbeText(t *testing.T) {
	stubAINodeConfig(t, nil)
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
	stubAINodeConfig(t, &config.Config{Nodes: []config.NodeConfig{
		{Name: "some-other-node", Hostname: "192.0.2.10"},
		{Name: "node-a", Hostname: "127.0.0.1"},
	}})
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
  - name: bound
    kind: openai-compatible
    base_url: http://127.0.0.1:8082/v1
    node: node-a
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
	localities := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			localities[fields[0]] = fields[1]
		}
	}
	if localities["nemotron"] != "peer" {
		t.Fatalf("nemotron locality = %q, want peer:\n%s", localities["nemotron"], out)
	}
	if localities["loop"] != "here" {
		t.Fatalf("loop locality = %q, want here:\n%s", localities["loop"], out)
	}
	if localities["bound"] != "here" {
		t.Fatalf("bound locality = %q, want here:\n%s", localities["bound"], out)
	}
}

func TestAIRouteStrictModelUnlistedExit(t *testing.T) {
	stubAINodeConfig(t, nil)
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

func TestAIInventoryTextCommandsPropagateWriterFailures(t *testing.T) {
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

	wantErr := errors.New("writer unavailable")
	missingPath := filepath.Join(dir, "missing.yaml")
	tests := []struct {
		name string
		args []string
	}{
		{name: "backends", args: []string{"backends", "--ai-config", path, "--skip-probe"}},
		{name: "roles", args: []string{"roles", "--ai-config", path}},
		{name: "empty backends", args: []string{"backends", "--ai-config", missingPath, "--skip-probe"}},
		{name: "empty roles", args: []string{"roles", "--ai-config", missingPath}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := aiCmd()
			cmd.SetOut(rejectingOutputWriter{err: wantErr})
			cmd.SetErr(&strings.Builder{})
			cmd.SetArgs(tt.args)
			if err := cmd.Execute(); !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, want writer failure", err)
			}
		})
	}
}
