package main

import (
	"strings"
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

func TestRequireLabelFlagKeepsCommaInValue(t *testing.T) {
	cmd := taskPlaceCmd()
	if err := cmd.ParseFlags([]string{"--require-label", "capabilities=cpu,gpu"}); err != nil {
		t.Fatal(err)
	}
	vals, err := cmd.Flags().GetStringArray("require-label")
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != 1 || vals[0] != "capabilities=cpu,gpu" {
		t.Fatalf("flag split the value: %#v", vals)
	}
	parsed, err := parseRequireLabels(vals)
	if err != nil {
		t.Fatal(err)
	}
	if parsed["capabilities"] != "cpu,gpu" {
		t.Fatalf("parsed %#v", parsed)
	}
}

func TestParseRequireLabels_RejectsIllegalCharset(t *testing.T) {
	longKey := strings.Repeat("k", 64)
	for _, bad := range []string{
		"os=linux box",
		"role:gpu=yes",
		"egress=lan:only",
		"a=b=c",
		longKey + "=v",
		"os=lin\u00fcx",
	} {
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
