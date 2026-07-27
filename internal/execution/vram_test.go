package execution

import (
	"context"
	"testing"
	"time"
)

// TestSamplePeakVRAMReturnsZeroWhenNvidiaSmiMissing verifies the sampler
// degrades cleanly on machines without nvidia-smi (Apple Silicon, Intel-only
// nodes). It does this by pointing PATH at an empty directory so the binary
// can't be found.
func TestSamplePeakVRAMReturnsZeroWhenNvidiaSmiMissing(t *testing.T) {
	t.Setenv("PATH", "/nonexistent/empty-path")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := samplePeakVRAM(ctx, 20*time.Millisecond)
	// Give the ticker a couple of beats to attempt (and fail) a sample.
	time.Sleep(80 * time.Millisecond)
	peak := stop()
	if peak != 0 {
		t.Fatalf("expected 0 peak when nvidia-smi missing, got %d", peak)
	}
}

// TestSamplePeakVRAMStopsCleanly verifies that calling stop() returns a
// non-negative int64 and doesn't block or panic, even if the goroutine never
// took a successful sample.
func TestSamplePeakVRAMStopsCleanly(t *testing.T) {
	t.Setenv("PATH", "/nonexistent/empty-path")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := samplePeakVRAM(ctx, 50*time.Millisecond)
	peak := stop()
	if peak < 0 {
		t.Fatalf("expected non-negative peak, got %d", peak)
	}
	// Calling stop twice must not panic on double-close of the channel.
	// (We don't call it twice here — the contract is single-stop — but
	// we verify the returned value is sane.)
}

// TestQueryTotalVRAMUsedHandlesMissingBinary verifies the query helper
// returns (0, false) when nvidia-smi isn't on PATH, not an error.
func TestQueryTotalVRAMUsedHandlesMissingBinary(t *testing.T) {
	t.Setenv("PATH", "/nonexistent/empty-path")
	total, ok := queryTotalVRAMUsed(context.Background())
	if ok {
		t.Fatalf("expected ok=false when nvidia-smi missing, got total=%d", total)
	}
	if total != 0 {
		t.Fatalf("expected total=0 when missing, got %d", total)
	}
}