package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

type compactionThenFailureBackend struct {
	summaries int
}

func (b *compactionThenFailureBackend) ChatStream(_ context.Context, _ []chat.Message, tools []chat.ToolDef, _ io.Writer) (chat.Message, error) {
	if tools == nil {
		b.summaries++
		return chat.Message{Role: chat.RoleAssistant, Content: "older conversation summary"}, nil
	}
	return chat.Message{}, errors.New("inference failed after compaction")
}

func TestFailedTurnAfterCompactionRemovesPromptAndKeepsLogEvidence(t *testing.T) {
	log, err := OpenDailySessionLog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &compactionThenFailureBackend{}
	a := New(Config{Backend: backend, Model: "test-model", MaxTurns: 2, MaxTokens: 200, Output: io.Discard, SessionLog: log})
	for i := 0; i < 6; i++ {
		a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "older prompt"})
		a.conv.Append(chat.Message{Role: chat.RoleAssistant, Content: "older answer"})
		a.conv.Append(chat.Message{Role: chat.RoleTool, ToolCallID: "old", Content: strings.Repeat("old tool output ", 40)})
	}
	const prompt = "current prompt that must be rolled back"
	if err := a.Run(context.Background(), prompt); err == nil {
		t.Fatal("expected inference failure")
	}
	if backend.summaries != 1 {
		t.Fatalf("compaction calls = %d, want 1", backend.summaries)
	}
	var summary bool
	for _, msg := range a.conv.Messages() {
		if msg.Content == prompt {
			t.Error("failed prompt remains in conversation after compaction")
		}
		if strings.Contains(msg.Content, "older conversation summary") {
			summary = true
		}
	}
	if !summary {
		t.Error("rollback removed the compacted history")
	}
	raw, _, _ := readSessionSegments(t, log.dir)
	started, finished := startedRecords(t, raw), finishedRecords(t, raw)
	if len(started) != 1 || started[0].Prompt != prompt || len(finished) != 1 {
		t.Fatalf("missing turn evidence: %s", raw)
	}
	if finished[0].Stop != "error" || finished[0].Answer != "" || !strings.Contains(finished[0].Error, "inference failed") {
		t.Fatalf("failure reused older answer or lost error: %+v", finished[0])
	}
	if strings.Count(string(raw), prompt) != 1 || strings.Contains(string(raw), "older answer") || strings.Contains(string(raw), "older conversation summary") {
		t.Fatalf("log duplicated the prompt or copied older history: %s", raw)
	}
}

func TestSessionLogShellStartDoesNotClaimSuccess(t *testing.T) {
	log, err := OpenDailySessionLog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	a := New(Config{
		Backend: &scriptedBackend{responses: []chat.Message{
			{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{toolCall("shell-1", "run_shell", `{"command":"echo example"}`)}},
			{Role: chat.RoleAssistant, Content: "command failed"},
		}},
		Model: "test-model", MaxTurns: 4, MaxTokens: 8192, Output: io.Discard,
		Autonomy: AutonomyFull, SessionLog: log,
		RunShell: func(context.Context, string) (string, error) {
			ran = true
			return "", errors.New("command exited 1")
		},
	})
	if err := a.Run(context.Background(), "run the check"); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("shell failure was not exercised")
	}
	raw, _, _ := readSessionSegments(t, log.dir)
	var start, failure bool
	for _, record := range recordsOfKind(t, raw, "tool") {
		if record.Tool == nil {
			continue
		}
		switch record.Tool.Event {
		case "shell":
			start = true
			if record.Tool.OK {
				t.Error("shell start claimed success before the command failed")
			}
		case "failed":
			failure = record.Tool.ID == "shell-1" && !record.Tool.OK
		case "succeeded":
			t.Error("failed shell recorded a successful result")
		}
	}
	if !start || !failure {
		t.Fatalf("missing shell start or failure evidence: %s", raw)
	}
}
