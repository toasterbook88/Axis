package main

import (
	"testing"
)

func TestParseRequireLabels_Empty(t *testing.T) {
	m, err := parseRequireLabels(nil)
	if err != nil || m != nil {
		t.Fatalf("nil input must give nil map, no error; got %v %v", m, err)
	}
	m, err = parseRequireLabels([]string{})
	if err != nil || m != nil {
		t.Fatalf("empty input must give nil map, no error; got %v %v", m, err)
	}
}

func TestParseRequireLabels_Single(t *testing.T) {
	m, err := parseRequireLabels([]string{"os=linux"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["os"] != "linux" {
		t.Fatalf("want os=linux, got %v", m)
	}
}

func TestParseRequireLabels_Conjunction(t *testing.T) {
	m, err := parseRequireLabels([]string{"os=linux", "egress=lan-only"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 2 || m["os"] != "linux" || m["egress"] != "lan-only" {
		t.Fatalf("want 2 pairs, got %v", m)
	}
}

func TestParseRequireLabels_Malformed(t *testing.T) {
	for _, bad := range []string{"os", "os=", "=linux", ""} {
		if _, err := parseRequireLabels([]string{bad}); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}

func TestParseRequireLabels_ConflictingDuplicateKey(t *testing.T) {
	if _, err := parseRequireLabels([]string{"os=linux", "os=darwin"}); err == nil {
		t.Fatal("conflicting duplicate key must be rejected")
	}
	// Identical duplicate is fine (StringSliceVar may pass repeats).
	m, err := parseRequireLabels([]string{"os=linux", "os=linux"})
	if err != nil || m["os"] != "linux" {
		t.Fatalf("identical duplicates must pass, got %v %v", m, err)
	}
}
