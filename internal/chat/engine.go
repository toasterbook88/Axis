package chat

import (
	"context"
	"fmt"
	"io"
)

// Engine abstracts the chat completion generation.
// This is the legacy single-prompt interface preserved for backward compatibility.
// New code should use Client.ChatStream directly with structured messages.
type Engine interface {
	GenerateStream(ctx context.Context, prompt string, w io.Writer) error
}

// NewEngine creates a new chat engine utilizing the hybrid approach.
// Retained for backward compat — new callers should use NewClient + ChatStream.
func NewEngine(model string) Engine {
	return &HybridEngine{
		model:  model,
		client: NewClient(DefaultEndpoint, model),
	}
}

// DefaultEndpoint is the standard local Ollama address.
const DefaultEndpoint = "http://localhost:11434"

// HybridEngine wraps the new Client, falling back to an error message when
// Ollama is unavailable.
type HybridEngine struct {
	model  string
	client *Client
}

func (e *HybridEngine) GenerateStream(ctx context.Context, prompt string, w io.Writer) error {
	msgs := []Message{
		{Role: RoleSystem, Content: BuildSystemPrompt(nil, "")},
		{Role: RoleUser, Content: prompt},
	}

	_, err := e.client.ChatStream(ctx, msgs, nil, w)
	if err != nil {
		fallback := fmt.Sprintf("\n[AXIS Fallback] Local Ollama is not ready for this request.\nError: %v\n\nEnsure 'ollama' is installed, the daemon is running, and the requested model exists locally. You can also fall back to `axis task context`.\n", err)
		fmt.Fprint(w, fallback)
	}
	return nil
}
