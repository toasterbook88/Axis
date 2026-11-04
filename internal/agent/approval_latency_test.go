package agent

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
)

// latencyObserver records the elapsed time reported for each tool result.
type latencyObserver struct {
	mu        sync.Mutex
	failed    map[string]time.Duration
	succeeded map[string]time.Duration
}

func newLatencyObserver() *latencyObserver {
	return &latencyObserver{failed: map[string]time.Duration{}, succeeded: map[string]time.Duration{}}
}

func (o *latencyObserver) TurnStarted(int, int)                          {}
func (o *latencyObserver) CompactionSkipped(error)                       {}
func (o *latencyObserver) ToolCalled(string, string, string)             {}
func (o *latencyObserver) ToolSkipped(string, string, string)            {}
func (o *latencyObserver) ShellExecuting(string, string, string, string) {}
func (o *latencyObserver) MaxTurnsReached(int)                           {}
func (o *latencyObserver) ToolSucceeded(id, _, _ string, _ int, elapsed time.Duration) {
	o.mu.Lock()
	o.succeeded[id] = elapsed
	o.mu.Unlock()
}
func (o *latencyObserver) ToolFailed(id, _ string, _ error, elapsed time.Duration) {
	o.mu.Lock()
	o.failed[id] = elapsed
	o.mu.Unlock()
}

// Tool latency measures execution only. Time spent waiting for an operator
// answer is not tool latency: a call declined after a long prompt never ran,
// and an approved call ran for as long as the tool took.
func TestToolLatencyExcludesApprovalWait(t *testing.T) {
	const wait = 300 * time.Millisecond
	const ceiling = 150 * time.Millisecond

	for _, tc := range []struct {
		name   string
		answer ConfirmResult
	}{
		{name: "declined", answer: ConfirmNo},
		{name: "approved", answer: ConfirmYes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obs := newLatencyObserver()
			a := New(Config{
				Model: "test-model",
				Backend: &scriptedBackend{responses: []chat.Message{
					{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{toolCall("w1", "slow_answer_tool", `{}`)}},
					{Role: chat.RoleAssistant, Content: "done"},
				}},
				MaxTurns:  4,
				MaxTokens: 8192,
				Output:    io.Discard,
				Observer:  obs,
				Autonomy:  AutonomyDefault,
				Confirm: func(_, _ string, _ int) ConfirmResult {
					time.Sleep(wait)
					return tc.answer
				},
			})
			a.tools.allowExtra("slow_answer_tool")
			a.tools.add("slow_answer_tool", "test tool that needs approval", json.RawMessage(`{"type":"object"}`),
				func(context.Context, json.RawMessage) (string, error) { return "ok", nil })

			if err := a.Run(context.Background(), "use the tool"); err != nil {
				t.Fatalf("run: %v", err)
			}
			obs.mu.Lock()
			defer obs.mu.Unlock()
			elapsed, ok := obs.failed["w1"]
			if tc.answer == ConfirmYes {
				elapsed, ok = obs.succeeded["w1"]
			}
			if !ok {
				t.Fatalf("no %s result for w1 (failed=%v succeeded=%v); did the tool ask for approval?", tc.name, obs.failed, obs.succeeded)
			}
			if elapsed >= ceiling {
				t.Fatalf("latency %v includes the %v approval wait; want under %v", elapsed, wait, ceiling)
			}
		})
	}
}
