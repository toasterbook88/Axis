package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

// subAgentBackend returns a canned final assistant message (no tool calls) so
// the sub-agent loop completes in one turn without needing real tools/SSH.
type subAgentBackend struct {
	mu     sync.Mutex
	answer string
	calls  int
}

func (b *subAgentBackend) ChatStream(ctx context.Context, msgs []chat.Message, tools []chat.ToolDef, w io.Writer) (chat.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	return chat.Message{Role: chat.RoleAssistant, Content: b.answer}, nil
}

func TestSpawnSubagentReturnsChildAnswer(t *testing.T) {
	backend := &subAgentBackend{answer: "tests passed on nixos: 42/42 ok"}
	parent := New(Config{
		Backend:     backend,
		MaxTurns:    3,
		MaxTokens:   4096,
		Output:      io.Discard,
		Confirm:     func(_, _ string, _ int) ConfirmResult { return ConfirmYes },
		ToolContext: NewToolContext(&RuntimeView{}, nil),
	})
	out, err := parent.dispatchSubagent(context.Background(), json.RawMessage(mustJSON(t, map[string]any{
		"prompt":      "run go test on nixos and report",
		"target_node": "nixos",
		"max_turns":   2,
	})))
	if err != nil {
		t.Fatalf("dispatchSubagent: %v", err)
	}
	if !strings.Contains(out, "tests passed") || !strings.Contains(out, "42/42") {
		t.Fatalf("expected child answer, got: %q", out)
	}
	if backend.calls == 0 {
		t.Fatalf("expected the sub-agent backend to be called")
	}
}

func TestSpawnSubagentEmptyPromptErrors(t *testing.T) {
	parent := New(Config{Backend: &subAgentBackend{}, Output: io.Discard, ToolContext: NewToolContext(&RuntimeView{}, nil)})
	_, err := parent.dispatchSubagent(context.Background(), json.RawMessage(mustJSON(t, map[string]any{"prompt": "   "})))
	if err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("expected empty-prompt error, got %v", err)
	}
}

func TestSpawnSubagentRecursionLimit(t *testing.T) {
	parent := New(Config{Backend: &subAgentBackend{}, Output: io.Discard, ToolContext: NewToolContext(&RuntimeView{}, nil)})
	parent.subAgentDepth = maxSubAgentDepth
	_, err := parent.dispatchSubagent(context.Background(), json.RawMessage(mustJSON(t, map[string]any{"prompt": "x"})))
	if err == nil || !strings.Contains(err.Error(), "recursion limit") {
		t.Fatalf("expected recursion-limit error, got %v", err)
	}
}

func TestFinalAssistantText(t *testing.T) {
	conv := chat.NewConversation(4096)
	conv.Append(chat.Message{Role: chat.RoleSystem, Content: "sys"})
	conv.Append(chat.Message{Role: chat.RoleUser, Content: "hi"})
	conv.Append(chat.Message{Role: chat.RoleAssistant, Content: "hello"})
	if got := finalAssistantText(conv); got != "hello" {
		t.Fatalf("got %q", got)
	}
	// Tool-call assistant messages are skipped; final text wins.
	conv2 := chat.NewConversation(4096)
	conv2.Append(chat.Message{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{toolCall("x", "t", "{}")}})
	conv2.Append(chat.Message{Role: chat.RoleTool, Content: "result"})
	conv2.Append(chat.Message{Role: chat.RoleAssistant, Content: "final answer"})
	if got := finalAssistantText(conv2); got != "final answer" {
		t.Fatalf("got %q", got)
	}
}

func TestBuildSubAgentSystemPromptMentionsTarget(t *testing.T) {
	s := buildSubAgentSystemPrompt("foundry", "extra context here")
	for _, want := range []string{"extra context here", "sub-agent", `target node is "foundry"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("prompt missing %q: %q", want, s)
		}
	}
}
