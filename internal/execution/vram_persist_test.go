package execution

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/state"
)

func stubTaskVRAMSampler(peak int64) func() {
	prev := startTaskVRAMSampler
	startTaskVRAMSampler = func(context.Context, time.Duration, func() int) func() int64 {
		return func() int64 { return peak }
	}
	return func() { startTaskVRAMSampler = prev }
}

func stubLocalShells(rss int64, runErr error) func() {
	prevShell, prevStream := RunLocalShell, StreamLocalShell
	RunLocalShell = func(context.Context, string, []string) ([]byte, int64, error) {
		return []byte("ok"), rss, runErr
	}
	StreamLocalShell = func(_ context.Context, _ string, _ []string, stdout, _ io.Writer) (int64, error) {
		if stdout != nil {
			_, _ = stdout.Write([]byte("ok"))
		}
		return rss, runErr
	}
	return func() { RunLocalShell, StreamLocalShell = prevShell, prevStream }
}

// Task-attributed VRAM measured during a local run must reach the persisted
// task history and the execution observation, on success and on failure.
func TestRunGuardedPersistsObservedVRAM(t *testing.T) {
	for _, tc := range []struct {
		name   string
		runErr error
	}{
		{"success", nil},
		{"failure", errors.New("exit status 3")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			node := models.NodeFacts{
				Name:     "studio",
				Hostname: "localhost",
				Status:   models.StatusComplete,
				Resources: &models.Resources{
					RAMTotalMB: 16384, RAMFreeMB: 12000, Pressure: "low", CPUCores: 8,
				},
				Tools: []models.ToolInfo{{Name: "git", Path: "/usr/bin/git"}},
			}
			rt := testGuardedRuntime(t, []models.NodeFacts{node})
			reqs := prepareRequirements("git status", ModeExec, Intent{})
			scope := placement.ObservationScopeForRequirements("studio", reqs, "")

			defer stubLocalShells(512, tc.runErr)()
			defer stubTaskVRAMSampler(2048)()

			resp, _ := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
				Description: "git status",
				Mode:        ModeExec,
				Confirm:     ConfirmWord,
			})
			if resp.ObservedVRAMMB != 2048 {
				t.Fatalf("response ObservedVRAMMB = %d, want 2048", resp.ObservedVRAMMB)
			}
			hist := rt.State.TaskHistory
			if len(hist) == 0 {
				t.Fatal("no task execution recorded")
			}
			if got := hist[len(hist)-1].ObservedVRAMMB; got != 2048 {
				t.Errorf("persisted task history ObservedVRAMMB = %d, want 2048", got)
			}
			obs, ok := rt.State.Observation(scope)
			if !ok || obs == nil {
				t.Fatal("expected execution observation")
			}
			if obs.ObservedVRAMMB != 2048 {
				t.Errorf("observation ObservedVRAMMB = %d, want 2048", obs.ObservedVRAMMB)
			}
			loaded, err := state.Load()
			if err != nil {
				t.Fatalf("reload state.json: %v", err)
			}
			if len(loaded.TaskHistory) != 1 || loaded.TaskHistory[0].ObservedVRAMMB != 2048 {
				t.Errorf("reloaded task history = %+v, want one entry with 2048 MiB", loaded.TaskHistory)
			}
			persisted, ok := loaded.Observation(scope)
			if !ok || persisted.ObservedVRAMMB != 2048 || persisted.LastSuccess != (tc.runErr == nil) {
				t.Errorf("reloaded observation = %+v, want 2048 MiB with last_success=%v", persisted, tc.runErr == nil)
			}
		})
	}
}
