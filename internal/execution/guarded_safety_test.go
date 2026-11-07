package execution

import (
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/safety"
)

// A specific risk (an AskAlways prompt) carries score 75 and its reason, not
// blocked, so full autonomy asks before axis_run_task runs it; an unknown
// command keeps score 0.
func TestPrepareGuardedExecutionScoresAskAlwaysPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	node := models.NodeFacts{
		Name: "studio", Hostname: "localhost", Status: models.StatusComplete,
		Resources: &models.Resources{RAMTotalMB: 8192, RAMFreeMB: 4096, Pressure: "low", CPUCores: 8},
	}
	rt := testGuardedRuntime(t, []models.NodeFacts{node})
	for _, tc := range []struct {
		cmd    string
		score  int
		reason string
	}{
		{"git push origin main", safety.ScoreAskAlways, "git-push"},
		{"foo --bar", 0, ""},
	} {
		resp, err := PrepareGuardedExecution(context.Background(), rt, GuardedExecutionRequest{
			Description: tc.cmd,
			Mode:        ModeExec,
			Confirm:     ConfirmWord,
		})
		if err != nil {
			t.Fatalf("%q: %v", tc.cmd, err)
		}
		if resp.Result.Blocked || resp.Result.DumbScore != tc.score || !strings.Contains(resp.Result.BlockReason, tc.reason) {
			t.Fatalf("%q: blocked %v score %d reason %q; want not blocked, score %d, reason with %q",
				tc.cmd, resp.Result.Blocked, resp.Result.DumbScore, resp.Result.BlockReason, tc.score, tc.reason)
		}
	}
}

func TestPrepareGuardedExecutionSafetyDeny(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	node := models.NodeFacts{
		Name:     "studio",
		Hostname: "localhost",
		Status:   models.StatusComplete,
		Resources: &models.Resources{
			RAMTotalMB: 8192,
			RAMFreeMB:  4096,
			Pressure:   "low",
			CPUCores:   8,
		},
	}
	rt := testGuardedRuntime(t, []models.NodeFacts{node})
	resp, err := PrepareGuardedExecution(context.Background(), rt, GuardedExecutionRequest{
		Description: "rm -rf /",
		Mode:        ModeExec,
		Confirm:     ConfirmWord,
	})
	if err != nil {
		t.Fatalf("expected safety block without error, got: %v", err)
	}
	if !resp.Result.Blocked {
		t.Fatal("expected Blocked=true for safety deny")
	}
	if !strings.Contains(resp.Result.BlockReason, "legacy-hardblock-root-rm") &&
		!strings.Contains(resp.Result.BlockReason, "recursive") {
		t.Fatalf("expected recursive-rm safety block reason, got: %v", resp.Result.BlockReason)
	}
}
