package chat

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConversationSaveAndLoad(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "test-history.json")

	c := NewConversation(4096)
	c.Append(Message{Role: RoleSystem, Content: "sys prompt"})
	c.Append(Message{Role: RoleUser, Content: "hello"})
	c.Append(Message{Role: RoleAssistant, Content: "hi there"})
	c.Append(Message{Role: RoleTool, Content: "tool result"})

	if err := c.SaveToFile(tmp); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Verify file exists and does NOT contain system messages.
	data, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) == "" {
		t.Fatal("history file is empty")
	}
	if contains(t, string(data), "sys prompt") {
		t.Error("history file should not contain system messages")
	}

	// Load into fresh conversation.
	c2 := NewConversation(4096)
	c2.Append(Message{Role: RoleSystem, Content: "reconstructed sys prompt"})
	if err := c2.LoadFromFile(tmp); err != nil {
		t.Fatalf("load: %v", err)
	}

	msgs := c2.Messages()
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages after load, got %d", len(msgs))
	}
	if msgs[0].Role != RoleSystem || msgs[0].Content != "reconstructed sys prompt" {
		t.Errorf("system message mismatch: %+v", msgs[0])
	}
	if msgs[1].Content != "hello" {
		t.Errorf("user message mismatch: %q", msgs[1].Content)
	}
	if msgs[2].Content != "hi there" {
		t.Errorf("assistant message mismatch: %q", msgs[2].Content)
	}
	if msgs[3].Content != "tool result" {
		t.Errorf("tool message mismatch: %q", msgs[3].Content)
	}
}

func TestConversationLoadMissingFile(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "nonexistent.json")
	c := NewConversation(4096)
	c.Append(Message{Role: RoleSystem, Content: "sys"})

	err := c.LoadFromFile(tmp)
	if err != nil {
		t.Fatalf("loading missing file should be a no-op, got: %v", err)
	}
	if c.Len() != 1 {
		t.Fatalf("expected 1 message, got %d", c.Len())
	}
}

func TestConversationHistoryCount(t *testing.T) {
	c := NewConversation(4096)
	c.Append(Message{Role: RoleSystem, Content: "sys"})
	c.Append(Message{Role: RoleUser, Content: "a"})
	c.Append(Message{Role: RoleAssistant, Content: "b"})

	if c.HistoryCount() != 2 {
		t.Errorf("expected history count 2, got %d", c.HistoryCount())
	}
}

func contains(t *testing.T, s, substr string) bool {
	t.Helper()
	return len(substr) > 0 && len(s) >= len(substr) && (s == substr || len(s) > len(substr) && s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || findSubstr(s, substr))
}

func findSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
