package chat

import (
	"strings"
	"testing"
)

// The pre-send check shortens old tool results one at a time, oldest first,
// and never touches the newest 4 messages.
func TestTruncateOldestToolResult(t *testing.T) {
	long := strings.Repeat("r", 5000)
	c := NewConversation(0)
	c.Append(Message{Role: RoleSystem, Content: "s"})
	c.Append(Message{Role: RoleTool, ToolCallID: "a", Content: long})
	c.Append(Message{Role: RoleTool, ToolCallID: "b", Content: long})
	for i := 0; i < 4; i++ {
		c.Append(Message{Role: RoleTool, ToolCallID: "new", Content: long})
	}

	if !c.TruncateOldestToolResult() {
		t.Fatal("first call truncated nothing")
	}
	msgs := c.Messages()
	if len(msgs[1].Content) >= 200 || msgs[2].Content != long {
		t.Fatalf("first call: a=%d b=%d bytes, want a truncated and b whole", len(msgs[1].Content), len(msgs[2].Content))
	}
	if !c.TruncateOldestToolResult() {
		t.Fatal("second call truncated nothing")
	}
	if c.TruncateOldestToolResult() {
		t.Fatal("third call truncated a protected message")
	}
	for _, m := range c.Messages()[3:] {
		if m.Content != long {
			t.Fatal("a protected newest message changed")
		}
	}
}
