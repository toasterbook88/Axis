package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
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

func TestLastRunOutcomeReturnsIndependentSnapshot(t *testing.T) {
	a := &Agent{scopeRefused: []string{"run_shell"}}
	a.setRunOutcome(RunOutcome{Stop: RunStopAnswered})
	got := a.LastRunOutcome()
	got.ScopeRefused[0] = "changed-by-caller"
	if next := a.LastRunOutcome(); next.ScopeRefused[0] != "run_shell" {
		t.Fatal("returned slice modified the stored run outcome")
	}
}

func TestFullVisibilityPredictionRequiresConsultResolver(t *testing.T) {
	a := New(Config{MaxTurns: 1, Output: io.Discard})
	a.tools.SetScope(ScopeExec)
	if a.tools.Visible(consultModelTool) {
		t.Fatal("test requires an absent consult resolver")
	}
	if a.tools.visibleUnderFull(consultModelTool) {
		t.Fatal("full-scope prediction exposed a tool without its resolver")
	}
	a.noteScopeRefused(consultModelTool)
	a.setRunOutcome(RunOutcome{Stop: RunStopAnswered})
	if got := a.LastRunOutcome(); len(got.ScopeRefused) != 0 {
		t.Fatal("a resolver-less tool was recorded as curable by full autonomy")
	}
}

func TestLastRunOutcomeAnswered(t *testing.T) {
	a := New(Config{Backend: &hookBackend{}, MaxTurns: 3, MaxTokens: 100000, Output: io.Discard})
	if err := a.Run(context.Background(), "show nodes"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := a.LastRunOutcome()
	if want := (RunOutcome{Stop: RunStopAnswered, Turns: 1, MaxTurns: 3}); !reflect.DeepEqual(got, want) {
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
	if want := (RunOutcome{Stop: RunStopTurnCeiling, Turns: 2, MaxTurns: 2}); !reflect.DeepEqual(got, want) {
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

// A call the mode hides is recorded when --autonomy full would expose the
// tool, once per run, and never for a tool no mode exposes.
func TestLastRunOutcomeScopeRefused(t *testing.T) {
	calls := func(names ...string) *chat.Message {
		m := &chat.Message{Role: chat.RoleAssistant}
		for i, n := range names {
			m.ToolCalls = append(m.ToolCalls, chat.ToolCall{ID: string(rune('a' + i)), Function: chat.ToolCallFunction{
				Name: n, Arguments: json.RawMessage(`{"command":"git push origin main"}`),
			}})
		}
		return m
	}
	be := &hookBackend{response: calls("run_shell", "run_shell", "spawn_subagent")}
	a := New(Config{Backend: be, MaxTurns: 1, MaxTokens: 100000, Output: io.Discard, ToolContext: &ToolContext{}})
	if err := a.Run(context.Background(), "push it"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := a.LastRunOutcome().ScopeRefused; !reflect.DeepEqual(got, []string{"run_shell"}) {
		t.Fatalf("ScopeRefused = %v, want [run_shell]", got)
	}
	be.response = &chat.Message{Role: chat.RoleAssistant, Content: "done"}
	if err := a.Run(context.Background(), "again"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := a.LastRunOutcome().ScopeRefused; got != nil {
		t.Fatalf("ScopeRefused carried into the next run: %v", got)
	}
}

// The deferred bridge checks the target's scope too, and records it.
func TestLastRunOutcomeScopeRefusedThroughBridge(t *testing.T) {
	be := &hookBackend{response: &chat.Message{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{{
		ID: "b1", Function: chat.ToolCallFunction{
			Name: "tool_call", Arguments: json.RawMessage(`{"name":"run_shell","arguments":{"command":"ls"}}`),
		},
	}}}}
	// An eager allowlist defers every other observe tool, so the bridge is
	// callable; the default mode still hides run_shell.
	a := New(Config{Backend: be, MaxTurns: 1, MaxTokens: 100000, Output: io.Discard, ToolContext: &ToolContext{}, EagerTools: []string{"read_file"}})
	if !a.tools.Visible("tool_call") {
		t.Fatal("bridge hidden; the test needs a deferred tool")
	}
	if err := a.Run(context.Background(), "list files"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := a.LastRunOutcome().ScopeRefused; !reflect.DeepEqual(got, []string{"run_shell"}) {
		t.Fatalf("ScopeRefused = %v, want [run_shell]", got)
	}
}
