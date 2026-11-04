package execution

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/state"
)

// Reload the actual state file: an in-memory observation alone does not
// establish that placement will see the measurement on the next invocation.
func TestRecordExecutionOutcomeReloadsVRAM(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"success", nil},
		{"failure", errors.New("task failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AXIS_HOME", t.TempDir())
			st := &state.ClusterState{}
			reqs := prepareRequirements("git status", ModeExec, Intent{})
			resp := GuardedExecutionResult{
				ExecID: "task-vram", Node: "node-1", Description: "git status",
				ObservedVRAMMB: 2048, ExitCode: exitCode(tc.err),
			}
			recordExecutionOutcome(st, reqs, resp, tc.err, time.Second, 512)
			loaded, err := state.Load()
			if err != nil {
				t.Fatal(err)
			}
			scope := placement.ObservationScopeForRequirements(resp.Node, reqs, resp.Tool)
			for name, got := range map[string]*state.ClusterState{"memory": st, "disk": loaded} {
				if len(got.TaskHistory) != 1 || got.TaskHistory[0].ObservedVRAMMB != 2048 {
					t.Errorf("%s task history = %+v, want one entry with 2048 MiB", name, got.TaskHistory)
				}
				obs, ok := got.Observation(scope)
				if !ok || obs.ObservedVRAMMB != 2048 {
					t.Errorf("%s observation = %+v, want 2048 MiB", name, obs)
				}
			}
		})
	}
}

// A CPU-only command must not inherit a resident server's device memory.
// Both query forms are simulated so this test runs without a physical GPU.
func TestCPUOnlyTaskDoesNotInheritDeviceVRAM(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"success", nil},
		{"failure", errors.New("task failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AXIS_HOME", t.TempDir())
			t.Setenv("HOME", t.TempDir())
			bin := t.TempDir()
			shim := "#!/bin/sh\ncase \"$1\" in\n" +
				"--query-gpu=memory.used) echo 8192 ;;\n" +
				"--query-compute-apps=pid,used_memory) echo '999999999, 8192' ;;\n" +
				"*) exit 1 ;;\nesac\n"
			if err := os.WriteFile(filepath.Join(bin, "nvidia-smi"), []byte(shim), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			prevShell, prevStream := RunLocalShell, StreamLocalShell
			defer func() { RunLocalShell, StreamLocalShell = prevShell, prevStream }()
			run := func() (int64, error) {
				// Allow two 500 ms sampler ticks while the CPU-only task runs.
				time.Sleep(1100 * time.Millisecond)
				return 128, tc.err
			}
			RunLocalShell = func(context.Context, string, []string) ([]byte, int64, error) {
				rss, err := run()
				return nil, rss, err
			}
			StreamLocalShell = func(context.Context, string, []string, io.Writer, io.Writer) (int64, error) {
				return run()
			}
			node := models.NodeFacts{
				Name: "node-1", Hostname: "localhost", Status: models.StatusComplete,
				Resources: &models.Resources{RAMTotalMB: 16384, RAMFreeMB: 12000, Pressure: "low", CPUCores: 8},
				Tools:     []models.ToolInfo{{Name: "git", Path: "/usr/bin/git"}},
			}
			rt := testGuardedRuntime(t, []models.NodeFacts{node})
			resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
				Description: "git status", Mode: ModeExec, Confirm: ConfirmWord,
			})
			if !errors.Is(err, tc.err) {
				t.Fatalf("run error = %v, want %v", err, tc.err)
			}
			if resp.ObservedVRAMMB != 0 {
				t.Errorf("CPU-only task attributed %d MiB from unrelated GPU processes", resp.ObservedVRAMMB)
			}
			loaded, err := state.Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.TaskHistory) != 1 || loaded.TaskHistory[0].ObservedVRAMMB != resp.ObservedVRAMMB {
				t.Errorf("reloaded history = %+v, response VRAM = %d", loaded.TaskHistory, resp.ObservedVRAMMB)
			}
		})
	}
}
