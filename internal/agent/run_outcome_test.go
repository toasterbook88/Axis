package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

type failingBackend struct{}

func (failingBackend) ChatStream(context.Context, []chat.Message, []chat.ToolDef, io.Writer) (chat.Message, error) {
	return chat.Message{}, errors.New("backend down")
}

func toolCallingBackend() *hookBackend {
	return &hookBackend{response: &chat.Message{
		Role:    chat.RoleAssistant,
		Content: "still checking",
		ToolCalls: []chat.ToolCall{{ID: "c1", Function: chat.ToolCallFunction{
			Name: "cluster_status", Arguments: json.RawMessage(`{}`),
		}}},
	}}
}

func TestLastRunOutcomeZeroBeforeRun(t *testing.T) {
	a := New(Config{Backend: &hookBackend{}, MaxTurns: 3, MaxTokens: 100000, Output: io.Discard})
	if got := a.LastRunOutcome(); got.Stop != RunStopNone {
		t.Fatalf("outcome before any run = %+v, want zero", got)
	}
}

func TestLastRunOutcomeAnswered(t *testing.T) {
	a := New(Config{Backend: &hookBackend{}, MaxTurns: 3, MaxTokens: 100000, Output: io.Discard})
	if err := a.Run(context.Background(), "show nodes"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := a.LastRunOutcome()
	if got != (RunOutcome{Stop: RunStopAnswered, Turns: 1, MaxTurns: 3}) {
		t.Fatalf("outcome = %+v", got)
	}
}

// The ceiling keeps Run's nil return (interactive surfaces rely on it) but is
// no longer indistinguishable from an answer.
func TestLastRunOutcomeTurnCeiling(t *testing.T) {
	be := toolCallingBackend()
	a := New(Config{Backend: be, MaxTurns: 2, MaxTokens: 100000, Output: io.Discard, DryRun: true})
	if err := a.Run(context.Background(), "show nodes"); err != nil {
		t.Fatalf("Run at the ceiling must still return nil, got %v", err)
	}
	got := a.LastRunOutcome()
	if got != (RunOutcome{Stop: RunStopTurnCeiling, Turns: 2, MaxTurns: 2}) {
		t.Fatalf("outcome = %+v", got)
	}
	if n := be.callCount(); n != 2 {
		t.Fatalf("backend rounds = %d, want 2", n)
	}
}

func TestLastRunOutcomeFailed(t *testing.T) {
	a := New(Config{Backend: failingBackend{}, MaxTurns: 3, MaxTokens: 100000, Output: io.Discard})
	if err := a.Run(context.Background(), "show nodes"); err == nil {
		t.Fatal("Run with a failing backend must return an error")
	}
	if got := a.LastRunOutcome(); got.Stop != RunStopFailed || got.Turns != 0 {
		t.Fatalf("outcome = %+v", got)
	}
}

// Each run replaces the previous outcome.
func TestLastRunOutcomeResetsPerRun(t *testing.T) {
	be := toolCallingBackend()
	a := New(Config{Backend: be, MaxTurns: 1, MaxTokens: 100000, Output: io.Discard, DryRun: true})
	_ = a.Run(context.Background(), "first")
	if got := a.LastRunOutcome().Stop; got != RunStopTurnCeiling {
		t.Fatalf("first run = %q", got)
	}
	be.mu.Lock()
	be.response = nil // default answer
	be.mu.Unlock()
	_ = a.Run(context.Background(), "second")
	if got := a.LastRunOutcome().Stop; got != RunStopAnswered {
		t.Fatalf("second run = %q, want answered", got)
	}
}
