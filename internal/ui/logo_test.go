package ui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/fatih/color"
)

var ansiEsc = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestPrintLogoColor(t *testing.T) {
	color.NoColor = false
	defer func() { color.NoColor = true }()
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("LC_ALL", "C.UTF-8")

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
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LC_ALL", "C.UTF-8")

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

func TestLogoLines_Depths(t *testing.T) {
	plain := LogoLines(ColorNone)
	if len(plain) != len(axisLogo) {
		t.Fatalf("ColorNone rows = %d, want %d", len(plain), len(axisLogo))
	}
	for i, l := range plain {
		if l != axisLogo[i] {
			t.Errorf("ColorNone row %d = %q, want %q", i, l, axisLogo[i])
		}
		if strings.Contains(l, "\x1b") {
			t.Errorf("ColorNone row %d contains escape", i)
		}
	}
	for _, d := range []ColorDepth{Color16, Color256, ColorTrue} {
		for i, l := range LogoLines(d) {
			if !strings.HasSuffix(l, "\x1b[0m") {
				t.Errorf("depth %d row %d lacks trailing reset: %q", d, i, l)
			}
			if got := ansiEsc.ReplaceAllString(l, ""); got != axisLogo[i] {
				t.Errorf("depth %d row %d strips to %q, want %q", d, i, got, axisLogo[i])
			}
		}
	}
	join := func(d ColorDepth) string { return strings.Join(LogoLines(d), "\n") }
	if s := join(Color256); strings.Contains(s, "38;2;") || !strings.Contains(s, "38;5;") {
		t.Error("Color256 must use 38;5; and never 38;2;")
	}
	if !strings.Contains(join(ColorTrue), "38;2;") {
		t.Error("ColorTrue must use 38;2;")
	}
	if s := join(Color16); strings.Contains(s, "38;") {
		t.Error("Color16 must not use extended color sequences")
	}
}

func TestASCIILogoLines(t *testing.T) {
	rows := ASCIILogoLines()
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want 5", len(rows))
	}
	for i, r := range rows {
		for _, c := range r {
			if c != '#' && c != ' ' {
				t.Errorf("row %d has rune %q", i, c)
			}
		}
	}
	if LogoWidth() != 27 {
		t.Errorf("LogoWidth = %d, want 27", LogoWidth())
	}
	if ASCIILogoWidth() != 23 {
		t.Errorf("ASCIILogoWidth = %d, want 23", ASCIILogoWidth())
	}
}

// legacyPrintLogo is the pre-refactor truecolor renderer, kept verbatim so the
// byte-identical guarantee for PrintLogo is asserted against the original.
func legacyPrintLogo(version string) string {
	var sb strings.Builder
	rows := len(axisLogo)
	width := 0
	for _, l := range axisLogo {
		if r := len([]rune(l)); r > width {
			width = r
		}
	}
	for row := range rows {
		runes := []rune(axisLogo[row])
		rowFrac := float64(row) / float64(rows-1)
		for col := range runes {
			ch := runes[col]
			if ch == ' ' || ch == '_' {
				sb.WriteString(string(ch))
				continue
			}
			colFrac := float64(col) / float64(width-1)
			t := 0.75*colFrac + 0.25*rowFrac
			if t > 1 {
				t = 1
			}
			r := logoStartR + t*(logoEndR-logoStartR)
			g := logoStartG + t*(logoEndG-logoStartG)
			b := logoStartB + t*(logoEndB-logoStartB)
			fmt.Fprintf(&sb, "\033[38;2;%d;%d;%dm%c", int(r), int(g), int(b), ch)
		}
		sb.WriteString("\033[0m\n")
	}
	plain := "v" + version
	pad := (width - len([]rune(plain))) / 2
	fmt.Fprintf(&sb, "%s%s\n", strings.Repeat(" ", pad), Dim(plain))
	return sb.String()
}

func TestPrintLogoTruecolorByteIdentical(t *testing.T) {
	color.NoColor = false
	defer func() { color.NoColor = true }()
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("LC_ALL", "C.UTF-8")

	var sb strings.Builder
	PrintLogo(&sb, "0.12.3")
	if want := legacyPrintLogo("0.12.3"); sb.String() != want {
		t.Errorf("PrintLogo output differs from legacy\n got %q\nwant %q", sb.String(), want)
	}
	if got := strings.Join(LogoLines(ColorTrue), "\n") + "\n"; !strings.HasPrefix(legacyPrintLogo("x"), got) {
		t.Error("LogoLines(ColorTrue) must equal the legacy logo rows")
	}
}

func TestPrintLogoASCIIFallback(t *testing.T) {
	color.NoColor = true
	t.Setenv("TERM", "xterm")
	t.Setenv("LC_ALL", "C")
	var sb strings.Builder
	PrintLogo(&sb, "1.0.0")
	out := sb.String()
	if strings.ContainsAny(out, "█╗╔═╝║╚") {
		t.Errorf("ASCII fallback emitted box glyphs: %q", out)
	}
	if !strings.Contains(out, "#####") || !strings.Contains(out, "v1.0.0") {
		t.Errorf("ASCII fallback missing logo or version: %q", out)
	}
}
