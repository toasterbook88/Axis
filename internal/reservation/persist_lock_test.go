package reservation

import (
	"testing"
	"time"
)

func TestSaveDoesNotHoldMutexDuringIO(t *testing.T) {
	t.Skip("RED: pending fix #4 — see EXECUTION-PLAN.md")

	ledger := NewLedger(DefaultLimits(), nil)
	done := make(chan error, 1)
	go func() { done <- ledger.Save() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Save: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Save remained blocked during persistence I/O")
	}
}
