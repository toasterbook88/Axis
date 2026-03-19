package chat

import (
	"context"
	"fmt"
	"io"
)

// Engine abstracts the chat completion generation
type Engine interface {
	GenerateStream(ctx context.Context, prompt string, w io.Writer) error
}

// NewEngine creates a new chat engine utilizing the hybrid approach
func NewEngine(model string) Engine {
	return &HybridEngine{
		model:  model,
		ollama: NewOllamaClient("http://localhost:11434", model),
	}
}

// HybridEngine uses Ollama by default and falls back to a structural message
type HybridEngine struct {
	model  string
	ollama *OllamaClient
}

func (e *HybridEngine) GenerateStream(ctx context.Context, prompt string, w io.Writer) error {
	if err := e.ollama.GenerateStream(ctx, prompt, w); err != nil {
		// Fallback to basic template if Ollama is unavailable or the model is missing.
		fallback := fmt.Sprintf("\n[AXIS Fallback] Local Ollama is not ready for this request.\nError: %v\n\nEnsure 'ollama' is installed, the daemon is running, and the requested model exists locally. You can also fall back to `axis task context`.\n", err)
		fmt.Fprint(w, fallback)
	}
	return nil
}
