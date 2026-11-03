package execution

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
)

func TestRunPreparedDoesNotExecuteWhenReservationCannotPersist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	node := models.NodeFacts{
		Name: "local", Hostname: "localhost", Status: models.StatusComplete,
		Resources: &models.Resources{RAMTotalMB: 16384, RAMFreeMB: 8192, CPUCores: 4},
	}
	rt := testGuardedRuntime(t, []models.NodeFacts{node})
	prepared, err := PrepareGuardedExecution(context.Background(), rt, GuardedExecutionRequest{
		Description: "echo ok", Mode: ModeExec, Confirm: ConfirmWord,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Hold the ledger lock so Reserve reaches its write without re-reading the
	// deliberately unwritable destination. This exercises persistence failure,
	// rather than an earlier failure to load the ledger.
	if err := rt.Ledger.LockFile(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rt.Ledger.UnlockFile()
	if err := os.MkdirAll(reservation.Path(), 0700); err != nil {
		t.Fatal(err)
	}
	called := false
	previous := RunLocalShell
	RunLocalShell = func(context.Context, string, []string) ([]byte, int64, error) {
		called = true
		return nil, 0, nil
	}
	defer func() { RunLocalShell = previous }()
	result, err := RunPreparedExecution(context.Background(), prepared)
	if err == nil || !strings.Contains(result.Error, "persist ledger") {
		t.Errorf("result=%+v error=%v, want ledger persistence failure", result, err)
	}
	if called {
		t.Error("shell ran without a durable reservation")
	}
	if entries := rt.Ledger.Entries(); len(entries) != 0 {
		t.Errorf("unwritten reservation retained: %+v", entries)
	}
	if total := rt.Ledger.Summary().TotalReservedMB; total != 0 {
		t.Errorf("reserved total=%d, want zero", total)
	}
}
