package agent

import (
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
)

// injectTurnContext puts per-turn context (prompt evidence, the live cluster
// snapshot) at the start of the newest user message instead of inside the
// system prompt.
//
// Why: the system prompt and the tool definitions are the same on every run
// and every turn (about 10k tokens with the default tool set). Local servers
// such as llama.cpp reuse a cached prompt only up to the first byte that
// differs. Text that changes every turn (the snapshot's age, free memory)
// inside the system prompt makes everything after it, including all tool
// definitions, look new, so the whole prompt is re-read on every turn.
// Measured on a 27B model on an 11 GB GPU: 12,008 prompt tokens re-read in 16.5 s
// per turn with the snapshot in the system prompt; 1,048 tokens in 1.8 s once
// it moved next to the user message.
//
// Blank blocks are dropped. If there is no user message (callers normally
// guarantee one), the context falls back to the first system message, then
// to a new leading system message, matching the previous behaviour.
func injectTurnContext(msgs []chat.Message, blocks ...string) []chat.Message {
	var parts []string
	for _, b := range blocks {
		if strings.TrimSpace(b) != "" {
			parts = append(parts, b)
		}
	}
	if len(parts) == 0 {
		return msgs
	}
	ctx := strings.Join(parts, "\n\n")

	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == chat.RoleUser {
			out := make([]chat.Message, len(msgs))
			copy(out, msgs)
			if out[i].Content != "" {
				out[i].Content = ctx + "\n\n" + out[i].Content
			} else {
				out[i].Content = ctx
			}
			return out
		}
	}
	for i := range msgs {
		if msgs[i].Role == chat.RoleSystem {
			out := make([]chat.Message, len(msgs))
			copy(out, msgs)
			if out[i].Content != "" {
				out[i].Content += "\n\n" + ctx
			} else {
				out[i].Content = ctx
			}
			return out
		}
	}
	return append([]chat.Message{{Role: chat.RoleSystem, Content: ctx}}, msgs...)
}

// emptyToolResult replaces a tool result with no text. An empty tool message
// is rejected by some servers (LiteLLM: "All non-assistant messages must
// contain 'content'"), and once stored it broke every later request.
const emptyToolResult = "(tool returned no output)"

// emptyUserMessage replaces a user message with no text for the same reason.
const emptyUserMessage = "(empty message)"

func toolResultContent(result string) string {
	if strings.TrimSpace(result) == "" {
		return emptyToolResult
	}
	return result
}

// fillEmptyContent returns msgs with every empty tool or user message given
// placeholder text. Assistant messages may be empty (tool calls only) and are
// left alone. msgs is not modified.
func fillEmptyContent(msgs []chat.Message) []chat.Message {
	var out []chat.Message
	for i, m := range msgs {
		if m.Content != "" || (m.Role != chat.RoleTool && m.Role != chat.RoleUser) {
			continue
		}
		if out == nil {
			out = make([]chat.Message, len(msgs))
			copy(out, msgs)
		}
		if m.Role == chat.RoleTool {
			out[i].Content = emptyToolResult
		} else {
			out[i].Content = emptyUserMessage
		}
	}
	if out == nil {
		return msgs
	}
	return out
}

// currentDateLine tells the model today's local date. Without it, a model
// guessed a date six days ahead for a "since" argument, got no results, and
// reasoned from that.
func currentDateLine(now time.Time) string {
	return "Today's date (local): " + now.Format("2006-01-02 (Monday), MST -0700") + ". Use it for any date or time argument."
}

func (a *Agent) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}
