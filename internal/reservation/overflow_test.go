package reservation

import (
	"bytes"
	"math"
	"os"
	"reflect"
	"testing"
)

func TestReserveRejectsAccountingOverflowWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		unlimited bool
		otherNode bool
		parent    bool
	}{
		{name: "capacity check"},
		{name: "unlimited", unlimited: true},
		{name: "cluster total", unlimited: true, otherNode: true},
		{name: "parent credit", parent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limits := DefaultLimits()
			if tc.unlimited {
				limits.MaxOvercommitRatio = 0
			}
			l := setupTestLedger(t, limits)
			l.SetNodeCapacity("node-a", 16384)
			l.SetNodeCapacity("node-b", 16384)
			if _, err := l.Reserve(Entry{ID: "first", Node: "node-a", RAMMB: 1024}); err != nil {
				t.Fatal(err)
			}
			if tc.parent {
				t.Setenv("AXIS_EXECUTION_PARENT_ID", "first")
			}
			if err := l.Load(); err != nil {
				t.Fatal(err)
			}
			entries := l.Entries()
			before, err := os.ReadFile(Path())
			if err != nil {
				t.Fatal(err)
			}
			node := "node-a"
			if tc.otherNode {
				node = "node-b"
			}
			entry, err := l.Reserve(Entry{ID: "overflow", Node: node, RAMMB: math.MaxInt64})
			if err == nil || entry != nil {
				t.Errorf("overflow accepted: entry=%v error=%v", entry, err)
			}
			if got := l.Entries(); !reflect.DeepEqual(got, entries) {
				t.Errorf("entries changed: %v", got)
			}
			if got := l.Summary().TotalReservedMB; got != 1024 {
				t.Errorf("total=%d, want 1024", got)
			}
			after, err := os.ReadFile(Path())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Error("rejected request changed persisted accounting")
			}
		})
	}
}

func TestReserveAcceptsRepresentableAccountingBoundary(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxOvercommitRatio = 0
	l := setupTestLedger(t, limits)
	l.SetNodeCapacity("node-a", 16384)
	if _, err := l.Reserve(Entry{ID: "first", Node: "node-a", RAMMB: math.MaxInt64 - 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reserve(Entry{ID: "last", Node: "node-a", RAMMB: 1}); err != nil {
		t.Fatal(err)
	}
	if got := l.Summary().TotalReservedMB; got != math.MaxInt64 {
		t.Fatalf("total=%d, want MaxInt64", got)
	}
}
