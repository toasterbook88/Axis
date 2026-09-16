package agent

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

// stubBackend records which calls it served and returns a fixed message.
type stubBackend struct {
	mu       sync.Mutex
	name     string
	calls    int
	response chat.Message
}

func (s *stubBackend) ChatStream(ctx context.Context, msgs []chat.Message, tools []chat.ToolDef, w io.Writer) (chat.Message, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return s.response, nil
}

func (s *stubBackend) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestRoutingBackendUsesCheapForSimplePrompt(t *testing.T) {
	primary := &stubBackend{name: "primary", response: chat.Message{Role: chat.RoleAssistant, Content: "P"}}
	cheap := &stubBackend{name: "cheap", response: chat.Message{Role: chat.RoleAssistant, Content: "C"}}
	rb := NewRoutingBackend(primary, cheap, nil)

	msgs := []chat.Message{{Role: chat.RoleUser, Content: "what is the cluster status?"}}
	resp, err := rb.ChatStream(context.Background(), msgs, nil, io.Discard)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if resp.Content != "C" {
		t.Fatalf("expected cheap response, got %q", resp.Content)
	}
	if cheap.callCount() != 1 || primary.callCount() != 0 {
		t.Fatalf("expected cheap=1 primary=0, got cheap=%d primary=%d", cheap.callCount(), primary.callCount())
	}
	if resp.RouterChoice != "cheap" {
		t.Fatalf("expected RouterChoice=cheap, got %q", resp.RouterChoice)
	}
}

func TestRoutingBackendUsesPrimaryForCodeKeyword(t *testing.T) {
	primary := &stubBackend{name: "primary", response: chat.Message{Role: chat.RoleAssistant, Content: "P"}}
	cheap := &stubBackend{name: "cheap", response: chat.Message{Role: chat.RoleAssistant, Content: "C"}}
	rb := NewRoutingBackend(primary, cheap, nil)

	msgs := []chat.Message{{Role: chat.RoleUser, Content: "implement a new function to parse yaml"}}
	resp, err := rb.ChatStream(context.Background(), msgs, nil, io.Discard)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if resp.Content != "P" {
		t.Fatalf("expected primary response, got %q", resp.Content)
	}
	if primary.callCount() != 1 || cheap.callCount() != 0 {
		t.Fatalf("expected primary=1 cheap=0, got primary=%d cheap=%d", primary.callCount(), cheap.callCount())
	}
}

func TestRoutingBackendPrimaryAfterMutatingTool(t *testing.T) {
	primary := &stubBackend{name: "primary", response: chat.Message{Role: chat.RoleAssistant, Content: "P"}}
	cheap := &stubBackend{name: "cheap", response: chat.Message{Role: chat.RoleAssistant, Content: "C"}}
	rb := NewRoutingBackend(primary, cheap, nil)

	// Previous assistant turn called edit_file → next turn should go to primary
	// even with a short, simple prompt (the model is mid-flow on hard work).
	msgs := []chat.Message{
		{Role: chat.RoleUser, Content: "fix the typo"},
		{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{toolCall("1", "edit_file", `{"path":"a.go"}`)}},
		{Role: chat.RoleTool, Content: "done"},
		{Role: chat.RoleUser, Content: "ok"},
	}
	resp, _ := rb.ChatStream(context.Background(), msgs, nil, io.Discard)
	if resp.Content != "P" {
		t.Fatalf("expected primary after mutating tool, got %q", resp.Content)
	}
}

func TestRoutingBackendNilCheapFallsBackToPrimary(t *testing.T) {
	primary := &stubBackend{name: "primary", response: chat.Message{Role: chat.RoleAssistant, Content: "P"}}
	rb := NewRoutingBackend(primary, nil, nil)
	msgs := []chat.Message{{Role: chat.RoleUser, Content: "anything"}}
	resp, err := rb.ChatStream(context.Background(), msgs, nil, io.Discard)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if resp.Content != "P" {
		t.Fatalf("expected primary when cheap is nil, got %q", resp.Content)
	}
	if resp.RouterChoice != "primary" {
		t.Fatalf("expected RouterChoice=primary, got %q", resp.RouterChoice)
	}
}

func TestDefaultRouterClassifierCases(t *testing.T) {
	cases := []struct {
		name  string
		msgs  []chat.Message
		cheap bool
	}{
		{"short simple prompt", []chat.Message{{Role: chat.RoleUser, Content: "show cluster status"}}, true},
		{"code keyword", []chat.Message{{Role: chat.RoleUser, Content: "implement a parser"}}, false},
		{"long prompt", []chat.Message{{Role: chat.RoleUser, Content: string(make([]byte, 700))}}, false},
		{"no user message", []chat.Message{{Role: chat.RoleSystem, Content: "sys"}}, false},
		{"after edit_file", []chat.Message{
			{Role: chat.RoleUser, Content: "fix it"},
			{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{toolCall("1", "edit_file", "{}")}},
			{Role: chat.RoleUser, Content: "ok"},
		}, false},
		{"after read_file stays cheap", []chat.Message{
			{Role: chat.RoleUser, Content: "look at config"},
			{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{toolCall("1", "read_file", "{}")}},
			{Role: chat.RoleUser, Content: "thanks"},
		}, true},
	}
	for _, c := range cases {
		got, _ := DefaultRouterClassifier(c.msgs)
		if got != c.cheap {
			t.Errorf("%s: got useCheap=%v, want %v", c.name, got, c.cheap)
		}
	}
}
