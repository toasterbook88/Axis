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

// GenerateStream attempts generation via local Ollama daemon, and handles fallback gracefully
func (e *HybridEngine) GenerateStream(ctx context.Context, prompt string, w io.Writer) error {
	if err := e.ollama.GenerateStream(ctx, prompt, w); err != nil {
		// Fallback to basic template if Ollama is unavailable
		fallback := fmt.Sprintf("\n[AXIS Fallback] The local Ollama daemon at http://localhost:11434 is currently unreachable or model %q is not pulled.\n\nError: %v\n\nPlease ensure Ollama is running (`ollama serve`) and the model is pulled (`ollama pull %s`) to use the full AI chat experience.\n", e.model, err, e.model)
		fmt.Fprint(w, fallback)
	}
	return nil
}
