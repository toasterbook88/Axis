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

func TestSpawnSubagentChildHasBackgroundTaskStore(t *testing.T) {
	// Regression: children must own a backgroundTasks store — the registry
	// exposes run_background/check_task/list_* and used to nil-deref without it.
	parent := New(Config{
		Backend:     &subAgentBackend{answer: "done"},
		MaxTurns:    2,
		Output:      io.Discard,
		Confirm:     alwaysConfirm(),
		ToolContext: NewToolContext(&RuntimeView{}, nil),
		RunShell: func(context.Context, string) (string, error) {
			return "ok", nil
		},
	})
	child := parent.buildChildAgent(2, "nixos", "extra")
	if child.backgroundTasks == nil {
		t.Fatal("buildChildAgent left backgroundTasks nil")
	}
	out, err := child.dispatchRunBackground(context.Background(), json.RawMessage(`{"command":"echo child-bg"}`))
	if err != nil {
		t.Fatalf("child run_background: %v", err)
	}
	if !strings.Contains(out, "bg-1") {
		t.Fatalf("expected task id, got %s", out)
	}
	// list_background_tasks must not panic
	_ = child.listBackgroundTasks()
	// check_task must not panic
	if _, err := child.dispatchCheckTask(context.Background(), json.RawMessage(`{"id":"bg-1"}`)); err != nil {
		t.Fatalf("check_task: %v", err)
	}
}

func TestBuildChildAgentInheritsDryRunAndEvidenceFlags(t *testing.T) {
	parent := New(Config{
		Backend:                 &subAgentBackend{answer: "done"},
		Output:                  io.Discard,
		Confirm:                 alwaysConfirm(),
		ToolContext:             NewToolContext(&RuntimeView{}, nil),
		DryRun:                  true,
		AllowRawCommandEvidence: true,
	})
	child := parent.buildChildAgent(2, "nixos", "extra")
	if !child.dryRun {
		t.Fatal("child dryRun should inherit parent --dry-run")
	}
	if !child.allowRawCommandEvidence {
		t.Fatal("child allowRawCommandEvidence should inherit parent setting")
	}
	// Parent defaults: child should get false when parent is false.
	parent2 := New(Config{
		Backend:     &subAgentBackend{},
		Output:      io.Discard,
		ToolContext: NewToolContext(&RuntimeView{}, nil),
	})
	child2 := parent2.buildChildAgent(1, "", "")
	if child2.dryRun || child2.allowRawCommandEvidence {
		t.Fatalf("child2 unexpectedly true: dryRun=%v evidence=%v", child2.dryRun, child2.allowRawCommandEvidence)
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
