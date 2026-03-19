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
		// Fallback to basic template if Ollama is unconditionally broken
		fallback := fmt.Sprintf("\n[AXIS Fallback] The auto-start sequence for Ollama failed.\nError: %v\n\nEnsure 'ollama' is installed on this node, or fall back to the raw context block generator (`axis task context`).\n", err)
		fmt.Fprint(w, fallback)
	}
	return nil
}
