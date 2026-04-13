package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestResolveChatModelUsesConfigDefault verifies that when no --model flag is
// set, resolveChatModel reads the chat.default_model field from nodes.yaml and
// returns it without calling the Ollama auto-detect path.
func TestResolveChatModelUsesConfigDefault(t *testing.T) {
	// Write a minimal nodes.yaml with chat.default_model set.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "nodes.yaml")
	if err := os.WriteFile(cfgPath, []byte(`nodes:
  - name: node-a
    hostname: node-a.local
    ssh_user: user
chat:
  default_model: "llama3.2:latest"
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Stub the Ollama auto-detect so if it is called the test fails.
	prev := resolveDefaultChatModel
	resolveDefaultChatModel = func(_ context.Context) string {
		t.Error("resolveDefaultChatModel should not be called when config provides a default model")
		return ""
	}
	defer func() { resolveDefaultChatModel = prev }()

	// Temporarily redirect the config load to our temp file by overriding the
	// env-independent path used in resolveChatModel.  Because resolveChatModel
	// calls config.Load(config.DefaultConfigPath()) we swap the function var to
	// a wrapper that calls Load with our temp path instead.
	got := resolveChatModelFromPath("", cfgPath)
	if got != "llama3.2:latest" {
		t.Fatalf("resolveChatModel() = %q, want %q", got, "llama3.2:latest")
	}
}
