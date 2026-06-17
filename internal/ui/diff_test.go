package ui

import (
	"strings"
	"testing"
)

func TestDiff(t *testing.T) {
	oldStr := "line1\nline2\nline3"
	newStr := "line1\nline2 modified\nline3\nline4"

	diff := Diff(oldStr, newStr)
	if len(diff) == 0 {
		t.Fatal("expected non-empty diff")
	}

	hasDelete := false
	hasAdd := false
	for _, l := range diff {
		if l.Type == DiffDeleted && l.Text == "line2" {
			hasDelete = true
		}
		if l.Type == DiffAdded && l.Text == "line2 modified" {
			hasAdd = true
		}
	}

	if !hasDelete {
		t.Error("expected deletion of 'line2'")
	}
	if !hasAdd {
		t.Error("expected addition of 'line2 modified'")
	}
}

func TestFormatDiff(t *testing.T) {
	oldStr := "one\ntwo\nthree\nfour\nfive\nsix"
	newStr := "one\ntwo\nthree modified\nfour\nfive\nsix"

	formatted := FormatDiff(oldStr, newStr)
	if !strings.Contains(formatted, "three modified") {
		t.Error("expected formatted diff to show the modification")
	}
	if !strings.Contains(formatted, "@@ ... @@") {
		t.Error("expected formatted diff to contain hunk markers")
	}
}
