package agent

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

func TestRunShellConfirmDoesNotHoldDispatchMu(t *testing.T) {
	a := &Agent{
		safety: func(string) (bool, string, int) { return true, "", 0 },
		output: io.Discard,
	}
	a.SetRunShell(func(context.Context, string) (string, error) {
		return "ok", nil
	})
	a.SetConfirm(func(string, string, int) ConfirmResult {
		done := make(chan struct{})
		go func() {
			_ = a.Autonomy()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("Autonomy blocked while the shell prompt was open")
		}
		return ConfirmYes
	})

	got, err := a.dispatchShell(context.Background(), json.RawMessage(`{"command":"echo hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok" {
		t.Fatalf("result = %q, want ok", got)
	}
}

// TestRunTaskConfirmDoesNotHoldDispatchMu is the axis_run_task counterpart to
// TestRunShellConfirmDoesNotHoldDispatchMu. The guarded-exec tool the charter
// names (task.run.request) had the same lock-held confirm bug #455 fixed for
// run_shell: dispatchRunTask used to call a.confirm while holding dispatchMu,
// so the footer could not paint/mutate while the prompt waited.
//
// Route through the real dispatcher (dispatchRunTask → PrepareGuardedExecution)
// rather than a hand-built candidate, and assert that Autonomy() is free to run
// while the axis_run_task prompt is open.
func TestRunTaskConfirmDoesNotHoldDispatchMu(t *testing.T) {
	// Minimal-but-real guarded execution fixture: one complete node with enough
	// RAM that prepare resolves to it without hitting reserved/pressure paths.
	view := &RuntimeView{
		Config: &config.Config{
			Nodes: []config.NodeConfig{
				{Name: "local", Hostname: "localhost", SSHUser: "me"},
			},
		},
		Snapshot: &models.ClusterSnapshot{
			Status: models.SnapshotHealthy,
			Nodes: []models.NodeFacts{
				{
					Name:     "local",
					Hostname: "localhost",
					Status:   models.StatusComplete,
					Resources: &models.Resources{
						RAMTotalMB: 8192,
						RAMFreeMB:  4096,
						Pressure:   "low",
						CPUCores:   8,
					},
				},
			},
			Summary: models.ClusterSummary{TotalNodes: 1, ReachableNodes: 1},
		},
		State:  &state.ClusterState{Nodes: map[string]state.NodeState{}},
		Skills: &skills.Store{},
	}

	a := &Agent{
		safety:      func(string) (bool, string, int) { return true, "", 0 },
		output:      io.Discard,
		toolContext: NewToolContext(view, nil),
		runTask: func(ctx context.Context, prepared execution.PreparedExecution) (string, error) {
			return "ok", nil
		},
	}
	a.SetConfirm(func(string, string, int) ConfirmResult {
		done := make(chan struct{})
		go func() {
			_ = a.Autonomy()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("Autonomy blocked while the axis_run_task prompt was open")
		}
		return ConfirmYes
	})

	got, err := a.dispatchRunTask(context.Background(), json.RawMessage(`{"description":"echo hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok" {
		t.Fatalf("result = %q, want ok", got)
	}
}
