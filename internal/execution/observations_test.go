package execution

import (
	"context"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/failures"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
)

func TestRunGuardedPropagatesPeakRAMMBToObservation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	node := models.NodeFacts{
		Name:     "studio",
		Hostname: "localhost",
		Status:   models.StatusComplete,
		Resources: &models.Resources{
			RAMTotalMB: 16384,
			RAMFreeMB:  12000,
			Pressure:   "low",
			CPUCores:   8,
		},
		Tools: []models.ToolInfo{{Name: "git", Path: "/usr/bin/git"}},
	}
	rt := testGuardedRuntime([]models.NodeFacts{node})
	reqs := prepareRequirements("git status", ModeExec, Intent{})
	scope := placement.ObservationScopeForRequirements("studio", reqs, "")

	prevShell := RunLocalShell
	RunLocalShell = func(context.Context, string, []string) ([]byte, error) {
		return []byte("ok"), nil
	}
	defer func() { RunLocalShell = prevShell }()

	prevPeak := captureChildrenPeakRAMMB
	captureChildrenPeakRAMMB = func() int64 { return 512 }
	defer func() { captureChildrenPeakRAMMB = prevPeak }()

	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description: "git status",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err != nil {
		t.Fatalf("RunGuarded() error = %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected successful response, got %+v", resp)
	}

	obs, ok := rt.State.Observation(scope)
	if !ok || obs == nil {
		t.Fatal("expected execution observation to be persisted")
	}
	if obs.PeakRAMMB != 512 {
		t.Errorf("PeakRAMMB = %d, want 512", obs.PeakRAMMB)
	}
}

func TestRunGuardedRecordsObservationAndClearsMatchingFailuresOnSuccess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	node := models.NodeFacts{
		Name:     "studio",
		Hostname: "localhost",
		Status:   models.StatusComplete,
		Resources: &models.Resources{
			RAMTotalMB: 16384,
			RAMFreeMB:  12000,
			Pressure:   "low",
			CPUCores:   8,
		},
		Tools: []models.ToolInfo{{Name: "git", Path: "/usr/bin/git"}},
	}
	rt := testGuardedRuntime([]models.NodeFacts{node})
	rt.State.Failures = failures.NewStore()
	reqs := prepareRequirements("git status", ModeExec, Intent{})
	scope := placement.ObservationScopeForRequirements("studio", reqs, "")
	rt.State.Failures.Record(models.FailureTimeout, models.FailureScope{
		Node:     "studio",
		Workload: reqs.Workload.Class,
	}, "previous crash", []string{"exit code 1"})

	prevShell := RunLocalShell
	RunLocalShell = func(context.Context, string, []string) ([]byte, error) {
		time.Sleep(5 * time.Millisecond)
		return []byte("ok"), nil
	}
	defer func() { RunLocalShell = prevShell }()

	prevPeak := captureChildrenPeakRAMMB
	captureChildrenPeakRAMMB = func() int64 { return 0 }
	defer func() { captureChildrenPeakRAMMB = prevPeak }()

	resp, err := RunGuarded(context.Background(), rt, GuardedExecutionRequest{
		Description: "git status",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err != nil {
		t.Fatalf("RunGuarded() error = %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected successful response, got %+v", resp)
	}

	obs, ok := rt.State.Observation(scope)
	if !ok || obs == nil {
		t.Fatal("expected execution observation to be persisted")
	}
	if !obs.LastSuccess {
		t.Fatal("expected successful run to record last_success=true")
	}
	if obs.WallTimeMS <= 0 {
		t.Fatalf("wall_time_ms = %d, want positive", obs.WallTimeMS)
	}
	if obs.PeakVRAMMB != 0 {
		t.Fatalf("expected unknown vram peak to remain unset, got %d", obs.PeakVRAMMB)
	}
	if _, blocked := rt.State.Failures.NarrowestMatch(models.FailureScope{
		Node:     "studio",
		Workload: reqs.Workload.Class,
	}); blocked {
		t.Fatal("expected matching failure memory to be cleared on success")
	}
}
