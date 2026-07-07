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

	// Resolve the model using the temporary config path directly so the test
	// stays independent of the default config location.
	got := resolveChatModelFromPath("", cfgPath, nil)
	if got != "llama3.2:latest" {
		t.Fatalf("resolveChatModel() = %q, want %q", got, "llama3.2:latest")
	}
}
