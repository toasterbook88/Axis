package chat

import "encoding/json"

// Role constants for structured conversation messages.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// ToolCall represents a tool invocation requested by the model.
type ToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction is the function name and arguments within a tool call.
type ToolCallFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Message is a single turn in a conversation (compatible with Ollama /api/chat).
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// Conversation holds an ordered sequence of messages with token-budget awareness.
type Conversation struct {
	messages []Message
	maxChars int // approximate token budget expressed as chars (4 chars ≈ 1 token)
}

// NewConversation creates a conversation with an approximate token limit.
// A limit of 0 disables truncation.
func NewConversation(maxTokens int) *Conversation {
	return &Conversation{maxChars: maxTokens * 4}
}

// Append adds a message and compacts older tool-result payloads when the
// conversation exceeds the character budget.
func (c *Conversation) Append(m Message) {
	c.messages = append(c.messages, m)
	if c.maxChars > 0 {
		c.compact()
	}
}

// Messages returns the current message list (read-only copy).
func (c *Conversation) Messages() []Message {
	out := make([]Message, len(c.messages))
	copy(out, c.messages)
	return out
}

// Clear removes all non-system messages, keeping the system prompt.
func (c *Conversation) Clear() {
	var kept []Message
	for _, m := range c.messages {
		if m.Role == RoleSystem {
			kept = append(kept, m)
		}
	}
	c.messages = kept
}

// Len returns the number of messages.
func (c *Conversation) Len() int { return len(c.messages) }

// EstimateTokens returns a rough token count (chars / 4).
func (c *Conversation) EstimateTokens() int {
	return c.charLen() / 4
}

func (c *Conversation) charLen() int {
	n := 0
	for _, m := range c.messages {
		n += len(m.Content)
		for _, tc := range m.ToolCalls {
			n += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
	}
	return n
}

// compact replaces the content of older tool-result messages with a truncated
// summary when the conversation exceeds the character budget.  System messages
// and the most recent 4 messages are never compacted.
func (c *Conversation) compact() {
	for c.charLen() > c.maxChars && c.compactableCount() > 0 {
		c.compactOldest()
	}
}

func (c *Conversation) compactableCount() int {
	n := 0
	protect := len(c.messages) - 4
	if protect < 0 {
		protect = 0
	}
	for i, m := range c.messages {
		if i >= protect {
			break
		}
		if m.Role == RoleSystem {
			continue
		}
		if m.Role == RoleTool && len(m.Content) > 200 {
			n++
		}
	}
	return n
}

func (c *Conversation) compactOldest() {
	protect := len(c.messages) - 4
	if protect < 0 {
		protect = 0
	}
	for i, m := range c.messages {
		if i >= protect {
			return
		}
		if m.Role == RoleTool && len(m.Content) > 200 {
			c.messages[i].Content = m.Content[:180] + "... [truncated]"
			return
		}
	}
}
