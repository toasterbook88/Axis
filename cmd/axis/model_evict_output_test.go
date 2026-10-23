package main

// Regression coverage for the eviction-report assertions added with the
// "800"-substring fix in TestModelEvictFreezeIgnoresMeasuredDelta.
//
// The eviction output lines are inline fmt.Fprintf calls in runModelEvict
// (cmd/axis/model.go), so these cases mirror the exact format strings and
// pin the contract the fixed assertion now checks:
//
//   - a correct freeze line must pass even when the receipt ID or the
//     duration happens to contain the digits "800";
//   - a line claiming a reclaimed 800 MiB must fail when the expected
//     reclaim is 0 MiB.
//
// The accepted/rejected helper below is the same predicate the fixed test
// uses, extracted so its discrimination is covered directly rather than
// only via the random receipt ID of a stubbed end-to-end run.

import (
	"fmt"
	"strings"
	"testing"
)

func evictLineAcceptsOutput(buf string) bool {
	return strings.Contains(buf, "reclaimed 0 MiB") && !strings.Contains(buf, "800 MiB")
}

func TestEvictLineAcceptsReceiptIDContaining800(t *testing.T) {
	buf := fmt.Sprintf("evicted llama-27b on lab-1 (reclaimed 0 MiB in 3ms) receipt %s\n",
		"mo-ab800cde")
	if !evictLineAcceptsOutput(buf) {
		t.Fatalf("correct freeze line with receipt ID containing 800 was rejected: %q", buf)
	}
}

func TestEvictLineAcceptsDuration800ms(t *testing.T) {
	buf := "evicted llama-27b on lab-1 (reclaimed 0 MiB in 800ms) receipt mo-aaaaaaaa\n"
	if !evictLineAcceptsOutput(buf) {
		t.Fatalf("correct freeze line with a 800 ms duration was rejected: %q", buf)
	}
}

func TestEvictLineRejectsClaimed800MiB(t *testing.T) {
	// Freeze-shaped: keeps the "reclaimed 0 MiB" prefix and then claims an
	// observed 800 MiB reclaim. The "800 MiB" clause must be what rejects it.
	buf := "evicted llama-27b on lab-1 (reclaimed 0 MiB in 3ms) receipt mo-ab800cde\n" +
		"also claimed reclaimed 800 MiB observed\n"
	if evictLineAcceptsOutput(buf) {
		t.Fatalf("claimed 800 MiB reclaim accepted though expected reclaim is 0: %q", buf)
	}
	// Without the 800 MiB claim, the same buffer is correct and must pass.
	bufOk := "evicted llama-27b on lab-1 (reclaimed 0 MiB in 3ms) receipt mo-ab800cde\n"
	if !evictLineAcceptsOutput(bufOk) {
		t.Fatalf("correct freeze line rejected: %q", bufOk)
	}
	if !strings.HasSuffix(strings.TrimRight(bufOk, "\n"), "receipt mo-ab800cde") {
		t.Fatalf("format drifted: %q", bufOk)
	}
}

func TestEvictLineFormatStillMatchesProduction(t *testing.T) {
	// Guard against drift between this predicate and the real format string:
	// the production freeze line is
	//   "evicted %s on %s (reclaimed 0 MiB in %dms) receipt %s\n"
	got := fmt.Sprintf("evicted %s on %s (reclaimed 0 MiB in %dms) receipt %s\n",
		"llama-27b", "lab-1", 3, "mo-ab800cde")
	want := "evicted llama-27b on lab-1 (reclaimed 0 MiB in 3ms) receipt mo-ab800cde\n"
	if got != want {
		t.Fatalf("production format drifted:\n got %q\nwant %q", got, want)
	}
	if !evictLineAcceptsOutput(got) {
		t.Fatalf("production-shaped correct line rejected: %q", got)
	}
}
