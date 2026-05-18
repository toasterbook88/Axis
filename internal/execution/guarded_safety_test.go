//go:build safety_scaffolded

package execution

import (
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

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
	rt := testGuardedRuntime([]models.NodeFacts{node})
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
	if !strings.Contains(resp.Result.BlockReason, "recursive-delete") {
		t.Fatalf("expected safety block reason, got: %v", resp.Result.BlockReason)
	}
}
