package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
)

// strictBackend behaves like LiteLLM in front of an OpenAI-style server: it
// rejects any request holding an empty non-assistant message. It replays
// responses in order, then answers "ok".
type strictBackend struct {
	mu        sync.Mutex
	responses []chat.Message
	calls     [][]chat.Message
}

func (s *strictBackend) ChatStream(ctx context.Context, msgs []chat.Message, tools []chat.ToolDef, w io.Writer) (chat.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, append([]chat.Message(nil), msgs...))
	for _, m := range msgs {
		if m.Role != chat.RoleAssistant && m.Content == "" {
			return chat.Message{}, fmt.Errorf("http status 400: All non-assistant messages must contain 'content'")
		}
	}
	if len(s.responses) == 0 {
		return chat.Message{Role: chat.RoleAssistant, Content: "ok"}, nil
	}
	r := s.responses[0]
	s.responses = s.responses[1:]
	return r, nil
}

func newStrictAgent(backend ChatBackend) *Agent {
	return New(Config{
		Backend: backend, MaxTurns: 4, MaxTokens: 100000, Output: io.Discard,
		Confirm:  func(_, _ string, _ int) ConfirmResult { return ConfirmYes },
		Autonomy: AutonomyDefault,
	})
}

// A tool that returns nothing (an MCP query with no matches) used to be sent
// as an empty tool message. The server rejected it mid-run, the message stayed
// in history, and every later prompt failed, even "hello".
func TestEmptyToolResultDoesNotBreakSession(t *testing.T) {
	backend := &strictBackend{responses: []chat.Message{
		{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{toolCall("c1", "empty_tool", `{}`)}},
		{Role: chat.RoleAssistant, Content: "nothing found"},
	}}
	a := newStrictAgent(backend)
	a.tools.allowExtra("empty_tool")
	a.tools.add("empty_tool", "returns nothing", json.RawMessage(`{"type":"object"}`), func(context.Context, json.RawMessage) (string, error) {
		return "", nil
	})

	if err := a.Run(context.Background(), "find events"); err != nil {
		t.Fatalf("run with empty tool result: %v", err)
	}
	if err := a.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("next prompt after empty tool result: %v", err)
	}
	var stored string
	for _, m := range a.conv.Messages() {
		if m.Role == chat.RoleTool && m.ToolCallID == "c1" {
			stored = m.Content
		}
	}
	if stored != emptyToolResult {
		t.Fatalf("stored tool result = %q, want %q", stored, emptyToolResult)
	}
}

// A history that already holds an empty message (an older session, or one
// loaded with --resume) is repaired when the request is built.
func TestEmptyMessagesInHistoryAreFilledOnSend(t *testing.T) {
	backend := &strictBackend{}
	a := newStrictAgent(backend)
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "earlier"})
	a.conv.Append(chat.Message{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{toolCall("old", "x", `{}`)}})
	a.conv.Append(chat.Message{Role: chat.RoleTool, ToolCallID: "old", Content: ""})
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: ""})

	if err := a.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run over history with empty messages: %v", err)
	}
	sent := backend.calls[len(backend.calls)-1]
	var sawTool, sawUser bool
	for _, m := range sent {
		if m.Role == chat.RoleTool && m.ToolCallID == "old" {
			sawTool = m.Content == emptyToolResult
		}
		if m.Role == chat.RoleUser && m.Content == emptyUserMessage {
			sawUser = true
		}
	}
	if !sawTool || !sawUser {
		t.Fatalf("empty messages not filled (tool=%v user=%v): %+v", sawTool, sawUser, sent)
	}
	// The stored history is not rewritten; only the outgoing copy is.
	for _, m := range a.conv.Messages() {
		if m.Role == chat.RoleTool && m.ToolCallID == "old" && m.Content != "" {
			t.Fatalf("stored history was modified: %q", m.Content)
		}
	}
}

func TestFillEmptyContentLeavesAssistantAndInputAlone(t *testing.T) {
	in := []chat.Message{
		{Role: chat.RoleSystem, Content: "sys"},
		{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{toolCall("a", "x", `{}`)}},
		{Role: chat.RoleTool, ToolCallID: "a", Content: "  "},
	}
	out := fillEmptyContent(in)
	if &out[0] != &in[0] {
		t.Fatal("no empty messages, but the slice was copied")
	}
	if out[1].Content != "" {
		t.Fatalf("assistant tool-call message changed: %q", out[1].Content)
	}
	if got := toolResultContent("  \n"); got != emptyToolResult {
		t.Fatalf("whitespace-only tool result = %q", got)
	}
	if got := toolResultContent("x"); got != "x" {
		t.Fatalf("nonempty tool result changed: %q", got)
	}
}

// The model is told today's date with every prompt, so date arguments (for
// example "events since") are not guessed.
func TestTurnContextCarriesTodaysDate(t *testing.T) {
	backend := &strictBackend{}
	a := newStrictAgent(backend)
	a.now = func() time.Time { return time.Date(2026, 10, 22, 17, 30, 0, 0, time.FixedZone("EDT", -4*3600)) }
	if err := a.Run(context.Background(), "what happened today"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var latestUser string
	for _, m := range backend.calls[0] {
		if m.Role == chat.RoleUser {
			latestUser = m.Content
		}
		if m.Role == chat.RoleSystem && strings.Contains(m.Content, "Today's date") {
			t.Fatal("date line is in the system prompt; it would break prompt-cache reuse across days")
		}
	}
	want := "Today's date (local): 2026-10-22 (Thursday), EDT -0400."
	if !strings.Contains(latestUser, want) || !strings.HasSuffix(latestUser, "what happened today") {
		t.Fatalf("latest user message missing %q: %q", want, latestUser)
	}
}
