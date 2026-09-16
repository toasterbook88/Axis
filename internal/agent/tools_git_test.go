package agent

import (
	"strings"
	"testing"
)

func TestDiffFragmentChangedFile(t *testing.T) {
	old := "alpha\nbeta\ngamma"
	new := "alpha\ndelta\ngamma"
	got := diffFragment(old, new)
	if !strings.Contains(got, "1 removed, 1 added") {
		t.Fatalf("net counts missing: %q", got)
	}
	if !strings.Contains(got, "- beta") || !strings.Contains(got, "+ delta") {
		t.Fatalf("first changed lines missing: %q", got)
	}
	if strings.Contains(got, "alpha") {
		t.Fatalf("unchanged prefix leaked into the fragment: %q", got)
	}
}

func TestDiffFragmentNewFile(t *testing.T) {
	got := diffFragment("", "line1\nline2\nline3")
	if !strings.Contains(got, "0 removed, 3 added") || !strings.Contains(got, "+ line1") {
		t.Fatalf("new-file fragment wrong: %q", got)
	}
}

func TestDiffFragmentIdenticalIsEmpty(t *testing.T) {
	if got := diffFragment("same", "same\n"); got != "" {
		t.Fatalf("no-op write produced a fragment: %q", got)
	}
}

func TestDiffFragmentGrowAndShrink(t *testing.T) {
	// Grow: more added lines than removed — the suffix scan must not
	// underflow.
	got := diffFragment("alpha\nbeta", "alpha\nx\ny\nz\nbeta")
	if !strings.Contains(got, "0 removed, 3 added") || !strings.Contains(got, "+ x") {
		t.Fatalf("grow shape wrong: %q", got)
	}

	// Shrink: more removed lines than added.
	got = diffFragment("alpha\nx\ny\nz\nbeta", "alpha\nbeta")
	if !strings.Contains(got, "3 removed, 0 added") || !strings.Contains(got, "- x") {
		t.Fatalf("shrink shape wrong: %q", got)
	}
}

func TestDiffFragmentEmptyNewContent(t *testing.T) {
	got := diffFragment("one\ntwo\nthree", "")
	if !strings.Contains(got, "3 removed, 0 added") {
		t.Fatalf("clear-file fragment wrong: %q", got)
	}
	if !strings.Contains(got, "- one") {
		t.Fatalf("first removed line missing: %q", got)
	}
}

func TestDiffFragmentMultiLineBlocks(t *testing.T) {
	old := "keep\nold1\nold2\nold3\nkeep2"
	new := "keep\nnew1\nnew2\nkeep2"
	got := diffFragment(old, new)
	if !strings.Contains(got, "3 removed, 2 added") {
		t.Fatalf("block counts wrong: %q", got)
	}
	// First removed/first added must come from the change region, not the
	// shared prefix or suffix.
	if !strings.Contains(got, "- old1") || !strings.Contains(got, "+ new1") {
		t.Fatalf("block first lines wrong: %q", got)
	}
	if strings.Contains(got, "keep") {
		t.Fatalf("shared context leaked into the fragment: %q", got)
	}
}
