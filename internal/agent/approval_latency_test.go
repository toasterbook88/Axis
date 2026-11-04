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

// A synchronous nested dispatch (spawn_subagent runs a child agent on the
// parent's context) must not count the child's approval waits as the parent
// tool's execution time, and overlapping child waits count once.
//
// Timeline inside the outer handler: inner A prompts 0-300ms then runs
// 300-500ms; inner B queues behind A 0-300ms, prompts 300-600ms, runs
// 600-800ms. Waits cover 0-600ms, so the outer tool executed ~200ms.
// Without propagation the outer reports ~800ms; summing overlapping waits
// (300+600) would hide the 200ms entirely.
func TestToolLatencyExcludesNestedApprovalWait(t *testing.T) {
	const prompt = 300 * time.Millisecond
	const work = 200 * time.Millisecond
	slowYes := func(_, _ string, _ int) ConfirmResult {
		time.Sleep(prompt)
		return ConfirmYes
	}
	child := New(Config{Model: "test-model", MaxTurns: 1, MaxTokens: 8192, Output: io.Discard, Autonomy: AutonomyDefault, Confirm: slowYes})
	child.tools.allowExtra("inner_tool")
	child.tools.add("inner_tool", "needs approval", json.RawMessage(`{"type":"object"}`),
		func(context.Context, json.RawMessage) (string, error) {
			time.Sleep(work)
			return "inner", nil
		})

	parent := New(Config{Model: "test-model", MaxTurns: 1, MaxTokens: 8192, Output: io.Discard, Autonomy: AutonomyDefault,
		Confirm: func(string, string, int) ConfirmResult { return ConfirmYes }})
	parent.tools.allowExtra("outer_tool")
	parent.tools.add("outer_tool", "runs a nested agent", json.RawMessage(`{"type":"object"}`),
		func(ctx context.Context, _ json.RawMessage) (string, error) {
			var wg sync.WaitGroup
			for _, id := range []string{"a", "b"} {
				wg.Add(1)
				go func(id string) {
					defer wg.Done()
					if _, _, err := child.timedDispatch(ctx, toolCall(id, "inner_tool", `{}`)); err != nil {
						t.Errorf("inner %s: %v", id, err)
					}
				}(id)
			}
			wg.Wait()
			return "outer", nil
		})

	_, elapsed, err := parent.timedDispatch(context.Background(), toolCall("outer", "outer_tool", `{}`))
	if err != nil {
		t.Fatalf("outer: %v", err)
	}
	if elapsed < 100*time.Millisecond || elapsed > 450*time.Millisecond {
		t.Fatalf("outer latency %v; want about %v (child approval waits excluded once)", elapsed, work)
	}
}
