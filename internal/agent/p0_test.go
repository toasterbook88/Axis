package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
)

// scriptedBackend returns canned responses in order, one per ChatStream call.
type scriptedBackend struct {
	mu        sync.Mutex
	responses []chat.Message
	calls     int
	// toolCallsSeen records the tools slice length for each call (to distinguish
	// summarization calls, which pass nil tools, from main-loop calls).
	toolsSeen []int
}

func (s *scriptedBackend) ChatStream(ctx context.Context, msgs []chat.Message, tools []chat.ToolDef, w io.Writer) (chat.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if tools == nil {
		s.toolsSeen = append(s.toolsSeen, 0)
	} else {
		s.toolsSeen = append(s.toolsSeen, len(tools))
	}
	if s.calls >= len(s.responses) {
		return chat.Message{Role: chat.RoleAssistant}, nil
	}
	r := s.responses[s.calls]
	s.calls++
	return r, nil
}

func toolCall(id, name string, args string) chat.ToolCall {
	return chat.ToolCall{
		ID: id,
		Function: chat.ToolCallFunction{
			Name:      name,
			Arguments: json.RawMessage(args),
		},
	}
}

func TestParallelDispatchPreservesToolCallOrder(t *testing.T) {
	// Each call returns its own index so we can verify the conversation received
	// results aligned with the correct tool_call_id.
	a := New(Config{
		Backend: &scriptedBackend{responses: []chat.Message{
			{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{
				toolCall("a", "idx_probe", `{"i":1}`),
				toolCall("b", "idx_probe", `{"i":2}`),
				toolCall("c", "idx_probe", `{"i":3}`),
			}},
			{Role: chat.RoleAssistant, Content: "done"},
		}},
		MaxTurns:    5,
		MaxTokens:   8192,
		Output:      io.Discard,
		Confirm:     func(_, _ string, _ int) ConfirmResult { return ConfirmYes },
		ToolContext: NewToolContext(&RuntimeView{}, nil),
	})
	a.tools.add("idx_probe",
		"Returns the index passed in.",
		json.RawMessage(`{"type":"object","properties":{"i":{"type":"integer"}},"required":["i"]}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				I int `json:"i"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			// Stagger so completion order differs from call order.
			time.Sleep(time.Duration(50*(4-p.I)) * time.Millisecond)
			return strings.Repeat("x", p.I), nil
		},
	)

	if err := a.Run(context.Background(), "run probes"); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	msgs := a.conv.Messages()
	var ordered []string
	for _, m := range msgs {
		if m.Role == chat.RoleTool {
			ordered = append(ordered, m.Content)
		}
	}
	want := []string{"x", "xx", "xxx"}
	if len(ordered) != 3 || ordered[0] != want[0] || ordered[1] != want[1] || ordered[2] != want[2] {
		t.Fatalf("tool results out of order: got %v, want %v", ordered, want)
	}
}

func TestDryRunSkipsConcurrentDispatch(t *testing.T) {
	a := New(Config{
		Backend: &scriptedBackend{responses: []chat.Message{
			{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{
				toolCall("d1", "slow_probe", `{"n":1}`),
				toolCall("d2", "slow_probe", `{"n":2}`),
			}},
			{Role: chat.RoleAssistant, Content: "done"},
		}},
		MaxTurns:    5,
		MaxTokens:   8192,
		Output:      io.Discard,
		DryRun:      true,
		ToolContext: NewToolContext(&RuntimeView{}, nil),
	})
	called := int32(0)
	a.tools.add("slow_probe", "test", json.RawMessage(`{"type":"object"}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			atomic.AddInt32(&called, 1)
			return "should-not-run", nil
		},
	)
	if err := a.Run(context.Background(), "dry run"); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if called != 0 {
		t.Fatalf("dry-run executed tools %d times", called)
	}
}

func TestCompactContextNoopBelowThreshold(t *testing.T) {
	bk := &scriptedBackend{}
	a := New(Config{
		Backend:     bk,
		MaxTurns:    2,
		MaxTokens:   8192,
		Output:      io.Discard,
		ToolContext: NewToolContext(&RuntimeView{}, nil),
	})
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "hi"})
	a.conv.Append(chat.Message{Role: chat.RoleAssistant, Content: "hello"})

	before := a.conv.Len()
	if err := a.compactContext(context.Background()); err != nil {
		t.Fatalf("compactContext failed: %v", err)
	}
	if a.conv.Len() != before {
		t.Fatalf("compaction should be a no-op below threshold: before=%d after=%d", before, a.conv.Len())
	}
	if len(bk.toolsSeen) != 0 {
		t.Fatalf("no backend call expected below threshold, got %v", bk.toolsSeen)
	}
}
