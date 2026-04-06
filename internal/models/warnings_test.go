package models

import "testing"

func TestAppendWarningIfMissingDeduplicates(t *testing.T) {
	snap := &ClusterSnapshot{
		Warnings: []Warning{
			{Kind: "cache", Message: "stale", Node: "n1"},
		},
	}

	AppendWarningIfMissing(snap, Warning{Kind: "cache", Message: "stale", Node: "n1"})
	AppendWarningIfMissing(snap, Warning{Kind: "cache", Message: "stale", Node: "n2"})

	if len(snap.Warnings) != 2 {
		t.Fatalf("expected deduped warnings, got %#v", snap.Warnings)
	}
}
