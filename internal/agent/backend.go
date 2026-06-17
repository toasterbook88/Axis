package agent

import (
	"context"
	"io"

	"github.com/toasterbook88/axis/internal/chat"
)

// ChatBackend abstracts the language model backend (e.g., local Ollama or cloud providers).
type ChatBackend interface {
	ChatStream(ctx context.Context, msgs []chat.Message, tools []chat.ToolDef, w io.Writer) (chat.Message, error)
}
