package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

func newBranchTestAgent(t *testing.T) *Agent {
	t.Helper()
	return New(Config{
		Backend:     &scriptedBackend{responses: []chat.Message{{Role: chat.RoleAssistant, Content: "ok"}}},
		MaxTurns:    1,
		MaxTokens:   4096,
		Output:      io.Discard,
		Confirm:     func(_, _ string, _ int) ConfirmResult { return ConfirmYes },
		ToolContext: NewToolContext(&RuntimeView{}, nil),
	})
}

func TestBranchAndRollbackRestoresConversation(t *testing.T) {
	a := newBranchTestAgent(t)
	// Seed the conversation with a system + user + assistant turn.
	a.conv.Append(chat.Message{Role: chat.RoleSystem, Content: "sys"})
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "original task"})
	a.conv.Append(chat.Message{Role: chat.RoleAssistant, Content: "first answer"})
	before := a.conv.Len()

	// Branch.
	out, err := a.branchSession("attempt-A")
	if err != nil {
		t.Fatalf("branchSession: %v", err)
	}
	if !strings.Contains(out, "attempt-A") {
		t.Fatalf("expected label in output, got: %s", out)
	}

	// Simulate a risky path: add turns that we'll discard.
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "risky follow-up"})
	a.conv.Append(chat.Message{Role: chat.RoleAssistant, Content: "bad answer"})
	a.conv.Append(chat.Message{Role: chat.RoleTool, Content: "garbage"})
	if got := a.conv.Len(); got != before+3 {
		t.Fatalf("post-risky len = %d, want %d", got, before+3)
	}

	// Rollback to the branch — conversation should return to `before` messages.
	out, err = a.rollbackSession("attempt-A")
	if err != nil {
		t.Fatalf("rollbackSession: %v", err)
	}
	if !strings.Contains(out, "attempt-A") {
		t.Fatalf("expected label in rollback output, got: %s", out)
	}
	if got := a.conv.Len(); got != before {
		t.Fatalf("after rollback len = %d, want %d", got, before)
	}
	// The discarded content must be gone.
	last := a.conv.Messages()[a.conv.Len()-1]
	if last.Content != "first answer" {
		t.Fatalf("last message after rollback = %q, want %q", last.Content, "first answer")
	}
}

func TestRollbackWithoutBranchErrors(t *testing.T) {
	a := newBranchTestAgent(t)
	_, err := a.rollbackSession("")
	if err == nil || !strings.Contains(err.Error(), "no session branches") {
		t.Fatalf("expected no-branch error, got %v", err)
	}
}

func TestRollbackUnknownLabelErrors(t *testing.T) {
	a := newBranchTestAgent(t)
	a.conv.Append(chat.Message{Role: chat.RoleSystem, Content: "sys"})
	if _, err := a.branchSession("real"); err != nil {
		t.Fatalf("branch: %v", err)
	}
	_, err := a.rollbackSession("ghost")
	if err == nil || !strings.Contains(err.Error(), "no branch labeled") {
		t.Fatalf("expected unknown-label error, got %v", err)
	}
}

func TestRollbackMostRecentWhenNoLabel(t *testing.T) {
	a := newBranchTestAgent(t)
	a.conv.Append(chat.Message{Role: chat.RoleSystem, Content: "sys"})
	a.branchSession("first")
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "u1"})
	a.branchSession("second")
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "u2"})
	// Rollback with no label → restores "second" (the most recent).
	out, err := a.rollbackSession("")
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if !strings.Contains(out, "second") {
		t.Fatalf("expected most-recent branch 'second', got: %s", out)
	}
	// Only "first" remains on the stack.
	if len(a.branchStack) != 1 || a.branchStack[0].label != "first" {
		t.Fatalf("expected 'first' to remain, got %v", a.branchStack)
	}
}

func TestBranchSnapshotIsIndependentOfLaterEdits(t *testing.T) {
	a := newBranchTestAgent(t)
	a.conv.Append(chat.Message{Role: chat.RoleSystem, Content: "sys"})
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "seed"})
	a.branchSession("snap")
	// Mutate the live conversation (append + ToolCalls).
	a.conv.Append(chat.Message{Role: chat.RoleAssistant, Content: "live", ToolCalls: []chat.ToolCall{toolCall("1", "x", "{}")}})
	// The branch stack snapshot must NOT reflect the live append.
	snap := a.branchStack[0].messages
	if len(snap) != 3 {
		t.Fatalf("snapshot len = %d, want 3 (New's system + test system + user)", len(snap))
	}
	// Rollback should restore exactly the 3-message state.
	a.rollbackSession("snap")
	if a.conv.Len() != 3 {
		t.Fatalf("post-rollback len = %d, want 3", a.conv.Len())
	}
}

func TestDispatchBranchSessionViaToolCall(t *testing.T) {
	a := newBranchTestAgent(t)
	a.conv.Append(chat.Message{Role: chat.RoleSystem, Content: "sys"})
	// Special-cased dispatch path (as the agent loop would invoke it).
	out, err := a.dispatchToolCall(context.Background(), chat.ToolCall{
		Function: chat.ToolCallFunction{Name: "branch_session", Arguments: json.RawMessage(`{"label":"via-dispatch"}`)},
	})
	if err != nil {
		t.Fatalf("dispatch branch_session: %v", err)
	}
	if !strings.Contains(out, "via-dispatch") {
		t.Fatalf("expected label in dispatch output, got: %s", out)
	}
	out, err = a.dispatchToolCall(context.Background(), chat.ToolCall{
		Function: chat.ToolCallFunction{Name: "rollback_session", Arguments: json.RawMessage(`{"label":"via-dispatch"}`)},
	})
	if err != nil {
		t.Fatalf("dispatch rollback_session: %v", err)
	}
	if !strings.Contains(out, "via-dispatch") {
		t.Fatalf("expected label in rollback output, got: %s", out)
	}
}
