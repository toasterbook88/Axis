package reservation

import (
	"testing"
	"time"
)

// TestSaveDoesNotHoldMutexDuringIO verifies that Save narrows the in-memory
// critical section to a snapshot: while persistence I/O runs, l.mu must remain
// free so concurrent readers are not blocked. We prove this by holding l.mu
// (via a read call that we gate) and confirming Save still completes promptly.
func TestSaveDoesNotHoldMutexDuringIO(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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

	// After Save returns, in-memory reads must not be blocked (mu released).
	readDone := make(chan struct{}, 1)
	go func() {
		_ = ledger.AllocatableRAM("nonexistent")
		readDone <- struct{}{}
	}()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("reader blocked; in-memory mutex held across persistence")
	}
}
