package main

// Regression coverage for the freeze-mode eviction output check used by
// TestModelEvictFreezeIgnoresMeasuredDelta. The lines below are produced by
// the real writeModelEvictReceipt, and the freeze test calls the same
// evictLineAcceptsOutput predicate, so reverting the predicate to a bare
// "800" substring check makes these tests fail:
//
//   - a correct freeze line must pass even when the receipt ID or the
//     duration happens to contain the digits "800";
//   - output claiming a reclaimed 800 MiB must fail when the expected
//     reclaim is 0 MiB.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/modellife"
	"github.com/toasterbook88/axis/internal/models"
)

func evictLineAcceptsOutput(buf string) bool {
	return strings.Contains(buf, "reclaimed 0 MiB") && !strings.Contains(buf, "800 MiB")
}

// freezeLine renders a completed one-instance freeze receipt through the
// production formatter.
func freezeLine(t *testing.T, id string, durationMS int64) string {
	t.Helper()
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	receipt := modellife.EvictionReceipt{
		ID:               id,
		Node:             "lab-1",
		Mode:             string(modellife.EvictModeFreeze),
		Status:           models.ModelOperationCompleted,
		DurationMS:       durationMS,
		EvictedInstances: []modellife.EvictedInstanceReceipt{{Model: "llama-27b"}},
	}
	if err := writeModelEvictReceipt(cmd, receipt, "text"); err != nil {
		t.Fatalf("writeModelEvictReceipt: %v", err)
	}
	return buf.String()
}

func TestEvictLineAcceptsReceiptIDContaining800(t *testing.T) {
	buf := freezeLine(t, "mo-ab800cde", 3)
	if !evictLineAcceptsOutput(buf) {
		t.Fatalf("correct freeze line with receipt ID containing 800 was rejected: %q", buf)
	}
}

func TestEvictLineAcceptsDuration800ms(t *testing.T) {
	buf := freezeLine(t, "mo-aaaaaaaa", 800)
	if !evictLineAcceptsOutput(buf) {
		t.Fatalf("correct freeze line with an 800 ms duration was rejected: %q", buf)
	}
}

func TestEvictLineRejectsClaimed800MiB(t *testing.T) {
	// A correct freeze line followed by an observed 800 MiB claim: the
	// "800 MiB" clause must be what rejects it.
	ok := freezeLine(t, "mo-ab800cde", 3)
	if !evictLineAcceptsOutput(ok) {
		t.Fatalf("correct freeze line rejected: %q", ok)
	}
	bad := ok + "also claimed reclaimed 800 MiB observed\n"
	if evictLineAcceptsOutput(bad) {
		t.Fatalf("claimed 800 MiB reclaim accepted though expected reclaim is 0: %q", bad)
	}
}

func TestEvictLineFormatMatchesProductionFreezeOutput(t *testing.T) {
	got := freezeLine(t, "mo-ab800cde", 3)
	want := "evicted llama-27b on lab-1 (reclaimed 0 MiB in 3ms) receipt mo-ab800cde\n"
	if got != want {
		t.Fatalf("freeze output drifted:\n got %q\nwant %q", got, want)
	}
}
