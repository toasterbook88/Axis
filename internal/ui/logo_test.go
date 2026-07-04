package ui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/fatih/color"
)

var ansiEsc = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestPrintLogoColor(t *testing.T) {
	color.NoColor = false
	defer func() { color.NoColor = true }()

	var sb strings.Builder
	PrintLogo(&sb, "0.12.3")
	out := sb.String()

	// Strip SGR escapes: each colored line must reconstruct the original
	// letterform line exactly (no byte-fragmentation from per-rune coloring).
	plain := ansiEsc.ReplaceAllString(out, "")
	if !strings.Contains(plain, axisLogo[0]) {
		t.Errorf("color output did not reconstruct first letterform line; got %q", plain)
	}
	if !strings.Contains(out, "\x1b[38;2;") {
		t.Error("color output missing truecolor SGR sequence")
	}
	if !strings.Contains(out, "\x1b[0m\n") {
		t.Error("color output missing end-of-line reset")
	}
	if !strings.Contains(out, "v0.12.3") {
		t.Error("color output missing version label")
	}
}

func TestPrintLogoNoColor(t *testing.T) {
	color.NoColor = true

	var sb strings.Builder
	PrintLogo(&sb, "0.12.3")
	out := sb.String()

	if strings.Contains(out, "\x1b[") {
		t.Errorf("no-color output contains escape codes; got %q", out)
	}
	if !strings.Contains(out, axisLogo[0]) {
		t.Errorf("no-color output missing first letterform line; got %q", out)
	}
	if !strings.Contains(out, "v0.12.3") {
		t.Error("no-color output missing version label")
	}
	// First line carries no trailing spaces (line ends in glyphs, not padding).
	lines := strings.Split(out, "\n")
	if lines[0] != axisLogo[0] {
		t.Errorf("no-color first line = %q, want %q", lines[0], axisLogo[0])
	}
}
