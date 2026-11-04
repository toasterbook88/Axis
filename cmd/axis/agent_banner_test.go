package main

import (
	"bytes"
	"strings"
	"testing"
)

// The session banner must name cluster mode instead of falling through to
// the strict or auto-approve wording, with or without --auto-approve.
func TestPrintAgentSessionDetailsNamesClusterMode(t *testing.T) {
	for _, autoApprove := range []bool{false, true} {
		var buf bytes.Buffer
		printAgentSessionDetails(&buf, ModelChoice{Model: "m"}, autoApprove, "cluster", 0, 10)
		got := buf.String()
		if !strings.Contains(got, "Autonomy: cluster") {
			t.Fatalf("auto-approve=%v: banner does not name cluster mode: %q", autoApprove, got)
		}
		if strings.Contains(got, "Strict Operator Approval") || strings.Contains(got, "Auto-Approve safe") {
			t.Fatalf("auto-approve=%v: banner fell through to non-cluster wording: %q", autoApprove, got)
		}
	}
}
