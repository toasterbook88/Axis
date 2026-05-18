package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// conversationHistory stores a serializable conversation record.
type conversationHistory struct {
	Messages []Message `json:"messages"`
}

// PersistPath returns the default path for conversation history files.
func PersistPath(name string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".axis", name+"-history.json")
}

// SaveToFile writes the conversation (excluding system messages) to the given path.
func (c *Conversation) SaveToFile(path string) error {
	var hist conversationHistory
	for _, m := range c.messages {
		// Skip system messages — they are reconstructed on load.
		if m.Role == RoleSystem {
			continue
		}
		hist.Messages = append(hist.Messages, m)
	}
	data, err := json.MarshalIndent(hist, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal conversation: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create history directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write history file: %w", err)
	}
	return nil
}

// LoadFromFile restores non-system messages from a file into the conversation.
// If the file does not exist, this is a no-op.
func (c *Conversation) LoadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read history file: %w", err)
	}
	var hist conversationHistory
	if err := json.Unmarshal(data, &hist); err != nil {
		return fmt.Errorf("unmarshal conversation: %w", err)
	}
	for _, m := range hist.Messages {
		// Skip system messages to avoid duplicates.
		if m.Role == RoleSystem {
			continue
		}
		c.messages = append(c.messages, m)
	}
	// Compact if needed.
	if c.maxChars > 0 {
		c.compact()
	}
	return nil
}

// HistoryCount returns the number of non-system messages in the conversation.
func (c *Conversation) HistoryCount() int {
	n := 0
	for _, m := range c.messages {
		if m.Role != RoleSystem {
			n++
		}
	}
	return n
}
