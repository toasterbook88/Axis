package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
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

func TestRunBackgroundRemoteRequiresRunOnNode(t *testing.T) {
	a := newAsyncTestAgent(t) // no RunOnNode
	_, err := a.dispatchRunBackground(context.Background(), json.RawMessage(`{"command":"echo x","node":"nixos"}`))
	if err == nil || !strings.Contains(err.Error(), "Layer 4") {
		t.Fatalf("expected Layer 4 contract error, got %v", err)
	}
}

func TestRunBackgroundWhitespaceNodeReportsLocally(t *testing.T) {
	// Whitespace-only node must execute locally and report "locally", not "on  ".
	a := newAsyncTestAgent(t)
	out, err := a.dispatchRunBackground(context.Background(), json.RawMessage(`{"command":"echo ws","node":"   "}`))
	if err != nil {
		t.Fatalf("dispatchRunBackground: %v", err)
	}
	if !strings.Contains(out, "locally") {
		t.Fatalf("expected locally in status message, got: %s", out)
	}
	if strings.Contains(out, "on  ") || strings.Contains(out, "on \t") {
		t.Fatalf("should not report remote location for whitespace node: %s", out)
	}
}

func TestRunBackgroundRemoteUsesRunOnNode(t *testing.T) {
	var sawNode, sawCmd string
	a := New(Config{
		Backend:     &scriptedBackend{responses: []chat.Message{{Role: "assistant", Content: "done"}}},
		MaxTurns:    1,
		MaxTokens:   4096,
		Output:      io.Discard,
		Confirm:     func(_, _ string, _ int) ConfirmResult { return ConfirmYes },
		ToolContext: NewToolContext(&RuntimeView{}, nil),
		RunOnNode: func(_ context.Context, node, command string) (string, error) {
			sawNode, sawCmd = node, command
			return `{"ok":true,"node":"nixos","output":"remote-bg"}`, nil
		},
	})
	out, err := a.dispatchRunBackground(context.Background(), json.RawMessage(`{"command":"uname -a","node":"nixos"}`))
	if err != nil {
		t.Fatalf("dispatchRunBackground: %v", err)
	}
	if !strings.Contains(out, "bg-1") {
		t.Fatalf("expected task id, got %s", out)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, _ := a.dispatchCheckTask(context.Background(), json.RawMessage(`{"id":"bg-1"}`))
		if strings.Contains(s, "completed") {
			if sawNode != "nixos" || sawCmd != "uname -a" {
				t.Fatalf("RunOnNode saw node=%q cmd=%q", sawNode, sawCmd)
			}
			if !strings.Contains(s, "remote-bg") {
				t.Fatalf("completed missing remote output: %s", s)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("remote background task did not complete")
}

func TestSetRunShellAndRunOnNodeRefresh(t *testing.T) {
	var shellCalls, nodeCalls int
	a := New(Config{
		Output:  io.Discard,
		Confirm: alwaysConfirm(),
		RunShell: func(context.Context, string) (string, error) {
			shellCalls++
			return "old-shell", nil
		},
		RunOnNode: func(context.Context, string, string) (string, error) {
			nodeCalls++
			return "old-node", nil
		},
	})
	a.SetRunShell(func(context.Context, string) (string, error) {
		shellCalls += 10
		return "new-shell", nil
	})
	a.SetRunOnNode(func(context.Context, string, string) (string, error) {
		nodeCalls += 10
		return "new-node", nil
	})
	out, err := a.shellRunner()(context.Background(), "echo")
	if err != nil || out != "new-shell" || shellCalls != 10 {
		t.Fatalf("shell refresh failed: out=%q calls=%d err=%v", out, shellCalls, err)
	}
	out, err = a.nodeRunner()(context.Background(), "n", "echo")
	if err != nil || out != "new-node" || nodeCalls != 10 {
		t.Fatalf("node refresh failed: out=%q calls=%d err=%v", out, nodeCalls, err)
	}
}

func TestBackgroundTaskSnapshotsRunnerAcrossModelRefresh(t *testing.T) {
	// Background jobs must keep the runner snapshotted at launch even if /model
	// swaps runners concurrently (race detector + determinism).
	started := make(chan struct{})
	release := make(chan struct{})
	var which atomic.Int32
	a := New(Config{
		Output:  io.Discard,
		Confirm: alwaysConfirm(),
		RunShell: func(context.Context, string) (string, error) {
			which.Store(1)
			close(started)
			<-release
			return "runner-v1", nil
		},
	})
	out, err := a.dispatchRunBackground(context.Background(), json.RawMessage(`{"command":"sleep-like"}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(out, "bg-1") {
		t.Fatalf("got %s", out)
	}
	<-started
	// Swap runners while the background job is mid-flight.
	a.SetRunShell(func(context.Context, string) (string, error) {
		which.Store(2)
		return "runner-v2", nil
	})
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, _ := a.dispatchCheckTask(context.Background(), json.RawMessage(`{"id":"bg-1"}`))
		if strings.Contains(s, "completed") {
			if !strings.Contains(s, "runner-v1") {
				t.Fatalf("expected snapshotted runner-v1 output, got: %s", s)
			}
			if which.Load() != 1 {
				t.Fatalf("expected only v1 runner invoked, which=%d", which.Load())
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("background task did not complete")
}

func TestBackgroundRunnerRefreshRace(t *testing.T) {
	// Stress: concurrent background launches + SetRunShell must not race under -race.
	a := New(Config{
		Output:  io.Discard,
		Confirm: alwaysConfirm(),
		RunShell: func(context.Context, string) (string, error) {
			time.Sleep(5 * time.Millisecond)
			return "ok", nil
		},
	})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = a.dispatchRunBackground(context.Background(), json.RawMessage(`{"command":"echo race"}`))
		}()
		go func(n int) {
			defer wg.Done()
			a.SetRunShell(func(context.Context, string) (string, error) {
				return fmt.Sprintf("v%d", n), nil
			})
		}(i)
	}
	wg.Wait()
	// Drain tasks briefly so goroutines finish.
	time.Sleep(200 * time.Millisecond)
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
