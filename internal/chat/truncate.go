package chat

// TruncateOldestToolResult shortens the oldest tool result outside the
// newest 4 messages, as the character-budget compaction does, and reports
// whether one was shortened. The agent's pre-send check calls it until the
// whole request fits or nothing is left to shorten.
func (c *Conversation) TruncateOldestToolResult() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.compactableCount() == 0 {
		return false
	}
	c.compactOldest()
	return true
}
