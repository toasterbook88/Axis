package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// toolCapMarker is the marker a capped tool result ends with.
func toolCapMarker(total, kept int) string {
	return fmt.Sprintf("… [truncated: tool output was %d bytes; the first %d are kept]", total, kept)
}

func lastTool(t *testing.T, c *Conversation) string {
	t.Helper()
	msgs := c.Messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == RoleTool {
			return msgs[i].Content
		}
	}
	t.Fatal("no tool message")
	return ""
}

var markerRe = regexp.MustCompile(`\n… \[truncated: tool output was (\d+) bytes; the first (\d+) are kept\]$`)

// requireCapped checks that got is original cut to at most capBytes in all,
// marker included, and that the marker reports the original size.
func requireCapped(t *testing.T, got, original string, capBytes int) {
	t.Helper()
	requireCappedFrom(t, got, original, len(original), capBytes)
}

func requireCappedFrom(t *testing.T, got, original string, total, capBytes int) {
	t.Helper()
	if len(got) > capBytes {
		t.Fatalf("capped result is %d bytes, want at most %d", len(got), capBytes)
	}
	m := markerRe.FindStringSubmatchIndex(got)
	if m == nil {
		t.Fatalf("missing marker; tail %q", got[max(0, len(got)-120):])
	}
	kept := got[:m[0]]
	if want := toolCapMarker(total, len(kept)); got[m[0]+1:] != want {
		t.Fatalf("marker %q, want %q", got[m[0]+1:], want)
	}
	if !strings.HasPrefix(original, kept) {
		t.Fatal("kept part is not a prefix of the original")
	}
	if len(kept) < capBytes-200 {
		t.Fatalf("kept %d bytes, want close to %d", len(kept), capBytes)
	}
	if !utf8.ValidString(kept) {
		t.Fatal("cut inside a UTF-8 rune")
	}
}

// One oversized tool result must not fill every later request: it is capped
// when it enters the conversation.
func TestAppendCapsToolResult(t *testing.T) {
	for _, tc := range []struct {
		name      string
		maxTokens int
		size      int
		keep      int // 0: unchanged
	}{
		{"10MB at default budget", 32768, 10 << 20, 32 << 10},
		{"20000 bytes unchanged", 32768, 20000, 0},
		{"read_file output unchanged", 32768, 32037, 0},
		{"small budget lowers the cap", 8192, 40000, 8192},
		{"read_file output cut below the default", 16384, 32037, 16384},
		{"large budget keeps what fits", 65536, 60000, 0},
		{"large budget caps at maxTokens bytes", 65536, 10 << 20, 65536},
		{"floor of 2048", 1000, 40000, 2048},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewConversation(tc.maxTokens)
			original := strings.Repeat("a", tc.size)
			c.Append(Message{Role: RoleTool, ToolCallID: "t1", Content: original})
			got := lastTool(t, c)
			if tc.keep == 0 {
				if got != original {
					t.Fatalf("result changed: len %d, want %d", len(got), len(original))
				}
				return
			}
			requireCapped(t, got, original, tc.keep)
		})
	}
}

func TestAppendCapCutsOnRuneBoundary(t *testing.T) {
	c := NewConversation(8192)
	original := strings.Repeat("é", 20000) // 40,000 bytes
	c.Append(Message{Role: RoleTool, ToolCallID: "t1", Content: original})
	requireCapped(t, lastTool(t, c), original, 8192)
}

func TestAppendDoesNotCapOtherRoles(t *testing.T) {
	long := strings.Repeat("b", 40000)
	for _, role := range []string{RoleSystem, RoleUser, RoleAssistant} {
		c := NewConversation(8192)
		c.Append(Message{Role: role, Content: long})
		if got := c.Messages()[0].Content; got != long {
			t.Fatalf("%s message changed: len %d", role, len(got))
		}
	}
}

// Invalid UTF-8 is sent as U+FFFD (3 bytes per byte), so the cap measures
// the result as sent and stays stable across save and resume.
func TestAppendCapsInvalidUTF8AsSent(t *testing.T) {
	c := NewConversation(32768)
	c.Append(Message{Role: RoleTool, ToolCallID: "t1", Content: strings.Repeat("\xe9", 10<<20)})
	stored := lastTool(t, c)
	data, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	var sent string
	if err := json.Unmarshal(data, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent) > 32768 {
		t.Fatalf("sent %d bytes, want at most 32768", len(sent))
	}
	if sent != stored {
		t.Fatal("stored result changes when sent")
	}
	if !markerRe.MatchString(stored) {
		t.Fatal("missing marker")
	}
}

func writeHistory(t *testing.T, msgs []Message) string {
	t.Helper()
	data, err := json.Marshal(conversationHistory{Messages: msgs})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "history.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A history saved while a session was wedged, or resumed with a smaller
// budget, is capped on load as well.
func TestLoadFromFileCapsToolResults(t *testing.T) {
	huge := strings.Repeat("c", 10<<20)
	mid := strings.Repeat("d", 32000)
	path := writeHistory(t, []Message{
		{Role: RoleUser, Content: "search"},
		{Role: RoleTool, ToolCallID: "t1", Content: mid},
		{Role: RoleTool, ToolCallID: "t2", Content: huge},
	})

	c := NewConversation(32768)
	if err := c.LoadFromFile(path); err != nil {
		t.Fatal(err)
	}
	requireCapped(t, lastTool(t, c), huge, 32<<10)

	small := NewConversation(8192)
	if err := small.LoadFromFile(path); err != nil {
		t.Fatal(err)
	}
	for _, m := range small.Messages() {
		if m.Role == RoleTool && len(m.Content) > 8192 {
			t.Fatalf("tool result %s not re-capped at the smaller budget: %d bytes", m.ToolCallID, len(m.Content))
		}
	}
	requireCapped(t, lastTool(t, small), huge, 8192)
}

// Capping is idempotent: a capped result is unchanged by a second pass, and
// cutting it again at a smaller cap keeps the original size in the marker.
func TestCapToolResultIdempotent(t *testing.T) {
	original := strings.Repeat("f", 1<<20)
	once := capToolResult(original, 32<<10)
	if twice := capToolResult(once, 32<<10); twice != once {
		t.Fatalf("second pass changed the result: %d -> %d bytes", len(once), len(twice))
	}
	smaller := capToolResult(once, 8192)
	requireCappedFrom(t, smaller, original, len(original), 8192)
}

// After an oversized newest tool result, the conversation fits the default
// budget instead of carrying the result whole in the protected tail.
func TestOversizedToolResultDoesNotWedge(t *testing.T) {
	c := NewConversation(32768)
	c.Append(Message{Role: RoleSystem, Content: "system"})
	c.Append(Message{Role: RoleUser, Content: "search the tree"})
	c.Append(Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t1", Function: ToolCallFunction{Name: "grep_search", Arguments: json.RawMessage(`{"pattern":"x"}`)}}}})
	c.Append(Message{Role: RoleTool, ToolCallID: "t1", Content: strings.Repeat("g", 10<<20)})
	if got := c.EstimateTokens(); got >= 32768 {
		t.Fatalf("estimate %d tokens, want under 32768", got)
	}
}

func TestConversationWithoutLimitUsesDefaultCap(t *testing.T) {
	c := NewConversation(0)
	original := strings.Repeat("h", 40000)
	c.Append(Message{Role: RoleTool, ToolCallID: "t1", Content: original})
	requireCapped(t, lastTool(t, c), original, 32<<10)
}

func TestRestoreAllCapsToolResults(t *testing.T) {
	huge := strings.Repeat("e", 10<<20)
	c := NewConversation(32768)
	c.RestoreAll([]Message{{Role: RoleTool, ToolCallID: "t1", Content: huge}})
	requireCapped(t, lastTool(t, c), huge, 32<<10)
}
