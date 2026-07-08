package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
)

func newAsyncTestAgent(t *testing.T) *Agent {
	t.Helper()
	return New(Config{
		Backend:     &scriptedBackend{responses: []chat.Message{{Role: "assistant", Content: "done"}}},
		MaxTurns:    1,
		MaxTokens:   4096,
		Output:      io.Discard,
		Confirm:     func(_, _ string, _ int) ConfirmResult { return ConfirmYes },
		ToolContext: NewToolContext(&RuntimeView{}, nil),
	})
}

func TestRunBackgroundReturnsIDAndRunsCommand(t *testing.T) {
	a := newAsyncTestAgent(t)
	out, err := a.dispatchRunBackground(context.Background(), json.RawMessage(`{"command":"echo hello-async"}`))
	if err != nil {
		t.Fatalf("dispatchRunBackground: %v", err)
	}
	if !strings.Contains(out, "bg-1") {
		t.Fatalf("expected task id bg-1, got: %s", out)
	}
	// Wait for completion and check output.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, _ := a.dispatchCheckTask(context.Background(), json.RawMessage(`{"id":"bg-1"}`))
		if strings.Contains(s, "completed") {
			if !strings.Contains(s, "hello-async") {
				t.Fatalf("completed task missing output: %s", s)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("task did not complete in time")
}

func TestCheckTaskRunningState(t *testing.T) {
	a := newAsyncTestAgent(t)
	// Start a command that sleeps briefly so we can observe the running state.
	_, err := a.dispatchRunBackground(context.Background(), json.RawMessage(`{"command":"sleep 0.4 && echo done-sleeping"}`))
	if err != nil {
		t.Fatalf("dispatchRunBackground: %v", err)
	}
	// Immediately check — should be running with no/empty output.
	s, err := a.dispatchCheckTask(context.Background(), json.RawMessage(`{"id":"bg-1"}`))
	if err != nil {
		t.Fatalf("check_task: %v", err)
	}
	if !strings.Contains(s, "running") {
		t.Fatalf("expected running state, got: %s", s)
	}
	// Wait for completion and confirm output appears.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, _ = a.dispatchCheckTask(context.Background(), json.RawMessage(`{"id":"bg-1"}`))
		if strings.Contains(s, "completed") {
			if !strings.Contains(s, "done-sleeping") {
				t.Fatalf("completed task missing output: %s", s)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("sleep task did not complete")
}

func TestCheckTaskUnknownIDErrors(t *testing.T) {
	a := newAsyncTestAgent(t)
	_, err := a.dispatchCheckTask(context.Background(), json.RawMessage(`{"id":"ghost"}`))
	if err == nil || !strings.Contains(err.Error(), "no background task") {
		t.Fatalf("expected unknown-id error, got %v", err)
	}
}

func TestCheckTaskEmptyIDErrors(t *testing.T) {
	a := newAsyncTestAgent(t)
	_, err := a.dispatchCheckTask(context.Background(), json.RawMessage(`{"id":""}`))
	if err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("expected empty-id error, got %v", err)
	}
}

func TestListBackgroundTasks(t *testing.T) {
	a := newAsyncTestAgent(t)
	// Empty initially.
	if s := a.listBackgroundTasks(); !strings.Contains(s, "No background tasks") {
		t.Fatalf("expected empty list, got: %s", s)
	}
	// Start one.
	_, _ = a.dispatchRunBackground(context.Background(), json.RawMessage(`{"command":"sleep 0.5"}`))
	s := a.listBackgroundTasks()
	if !strings.Contains(s, "bg-1") || !strings.Contains(s, "running") {
		t.Fatalf("expected bg-1 running in list, got: %s", s)
	}
}

func TestRunBackgroundEmptyCommandErrors(t *testing.T) {
	a := newAsyncTestAgent(t)
	_, err := a.dispatchRunBackground(context.Background(), json.RawMessage(`{"command":"  "}`))
	if err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("expected empty-command error, got %v", err)
	}
}

func TestRunBackgroundBlockedSession(t *testing.T) {
	a := newAsyncTestAgent(t)
	a.blockAll = true
	_, err := a.dispatchRunBackground(context.Background(), json.RawMessage(`{"command":"echo x"}`))
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected blocked error, got %v", err)
	}
}
