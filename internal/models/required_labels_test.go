package models

import (
	"reflect"
	"strings"
	"testing"
)

func TestSatisfiesRequiredLabels_EmptyRequirement(t *testing.T) {
	n := NodeFacts{Name: "a"}
	ok, reasons := n.SatisfiesRequiredLabels(nil)
	if !ok || len(reasons) != 0 {
		t.Fatalf("empty requirement must satisfy any node, got ok=%v reasons=%v", ok, reasons)
	}
	ok, reasons = n.SatisfiesRequiredLabels(map[string]string{})
	if !ok || len(reasons) != 0 {
		t.Fatalf("empty map requirement must satisfy any node, got ok=%v reasons=%v", ok, reasons)
	}
}

func TestSatisfiesRequiredLabels_Conjunction(t *testing.T) {
	// SAM Sep 2026 semantics: required labels are a CONJUNCTION — ALL pairs
	// must match exactly. One check, all predicates ANDed.
	n := NodeFacts{Name: "a", Labels: map[string]string{
		"os":     "linux",
		"egress": "lan-only",
		"gpu":    "none",
	}}

	ok, reasons := n.SatisfiesRequiredLabels(map[string]string{"os": "linux"})
	if !ok || len(reasons) != 0 {
		t.Fatalf("single matching pair must pass, got reasons=%v", reasons)
	}

	ok, reasons = n.SatisfiesRequiredLabels(map[string]string{
		"os":     "linux",
		"egress": "lan-only",
	})
	if !ok || len(reasons) != 0 {
		t.Fatalf("all pairs matching must pass (conjunction), got reasons=%v", reasons)
	}

	ok, reasons = n.SatisfiesRequiredLabels(map[string]string{
		"os":     "linux",
		"egress": "internet",
	})
	if ok {
		t.Fatal("any mismatch must fail the whole conjunction")
	}
	if len(reasons) != 1 {
		t.Fatalf("expected exactly 1 exclusion reason, got %d: %v", len(reasons), reasons)
	}

	// Missing key must be reported even when other pairs match.
	ok, reasons = n.SatisfiesRequiredLabels(map[string]string{
		"os":     "linux",
		"tenant": "acme",
	})
	if ok {
		t.Fatal("missing key must fail")
	}
	if len(reasons) != 1 || !strings.HasPrefix(reasons[0], "missing required label: tenant=acme") {
		t.Fatalf("wrong reason: %v", reasons)
	}
}

func TestSatisfiesRequiredLabels_CaseSensitive(t *testing.T) {
	// Exact, case-sensitive match (SAM parity).
	n := NodeFacts{Name: "a", Labels: map[string]string{"OS": "Linux"}}

	ok, _ := n.SatisfiesRequiredLabels(map[string]string{"OS": "Linux"})
	if !ok {
		t.Fatal("exact match must pass")
	}

	ok, _ = n.SatisfiesRequiredLabels(map[string]string{"OS": "linux"})
	if ok {
		t.Fatal("value match must be case-sensitive")
	}

	ok, _ = n.SatisfiesRequiredLabels(map[string]string{"os": "Linux"})
	if ok {
		t.Fatal("key match must be case-sensitive")
	}
}

func TestSatisfiesRequiredLabels_DeterministicReasons(t *testing.T) {
	// Reasons are sorted by required key, so output ORDER is deterministic
	// across map iteration orders (Go map range is unordered).
	n := NodeFacts{Name: "a"}
	_, reasons1 := n.SatisfiesRequiredLabels(map[string]string{"b": "1", "a": "1", "c": "1"})
	_, reasons2 := n.SatisfiesRequiredLabels(map[string]string{"a": "1", "c": "1", "b": "1"})

	if !reflect.DeepEqual(reasons1, reasons2) {
		t.Fatalf("reason order differs across map orderings:\n%v\n%v", reasons1, reasons2)
	}
	if len(reasons1) != 3 {
		t.Fatalf("expected 3 missing reasons, got %d", len(reasons1))
	}
	// Explicit order contract: keys sorted a < b < c.
	want := []string{
		"missing required label: a=1",
		"missing required label: b=1",
		"missing required label: c=1",
	}
	if !reflect.DeepEqual(reasons1, want) {
		t.Fatalf("reason order mismatch:\n got %v\nwant %v", reasons1, want)
	}
}
