package ui

import (
	"fmt"
	"math"
	"strings"
)

// ColorDepth is how many colors the terminal can render, ordered so that a
// larger value is strictly more capable.
type ColorDepth int

const (
	// ColorNone means no color escapes at all (NO_COLOR, dumb or unknown TERM).
	ColorNone ColorDepth = iota
	// Color16 is the basic 16-color ANSI palette.
	Color16
	// Color256 is the xterm 256-color palette.
	Color256
	// ColorTrue is 24-bit RGB.
	ColorTrue
)

// DetectColorDepth infers color support from the environment. getenv is a
// parameter (not os.Getenv) so the decision is a pure function of its input
// and testable without mutating process state.
//
// Order matters: NO_COLOR and an unusable TERM veto everything, because an
// explicit opt-out must beat a COLORTERM left over from a parent shell.
func DetectColorDepth(getenv func(string) string) ColorDepth {
	if getenv("NO_COLOR") != "" {
		return ColorNone
	}
	term := getenv("TERM")
	if term == "" || term == "dumb" {
		return ColorNone
	}
	switch getenv("COLORTERM") {
	case "truecolor", "24bit":
		return ColorTrue
	}
	switch term {
	case "xterm-ghostty", "xterm-kitty", "wezterm":
		return ColorTrue
	}
	if strings.HasSuffix(term, "-direct") {
		return ColorTrue
	}
	if strings.Contains(term, "256color") {
		return Color256
	}
	return Color16
}

// UnicodeSupported reports whether box-drawing and block glyphs are safe to
// print. The Linux virtual console and dumb terminals cannot render them even
// under a UTF-8 locale; otherwise the effective locale decides, using the same
// precedence as the C library (LC_ALL, then LC_CTYPE, then LANG).
func UnicodeSupported(getenv func(string) string) bool {
	switch getenv("TERM") {
	case "linux", "dumb":
		return false
	}
	locale := ""
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := getenv(k); v != "" {
			locale = v
			break
		}
	}
	if locale == "" {
		// No locale configured: modern terminals are UTF-8, so assume it.
		return true
	}
	if locale == "C" || locale == "POSIX" {
		return false
	}
	l := strings.ToLower(locale)
	return strings.Contains(l, "utf-8") || strings.Contains(l, "utf8")
}

// FgRGB returns the SGR prefix that sets the foreground to (r,g,b) at depth d,
// downsampling to the 256-color cube or the 16-color palette as needed. It
// returns "" for ColorNone (and any unknown depth) so callers can concatenate
// unconditionally.
func FgRGB(r, g, b uint8, d ColorDepth) string {
	switch d {
	case ColorTrue:
		return fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
	case Color256:
		n := 16 + 36*cubeLevel(r) + 6*cubeLevel(g) + cubeLevel(b)
		return fmt.Sprintf("\033[38;5;%dm", n)
	case Color16:
		k := 0
		if r > 127 {
			k |= 1
		}
		if g > 127 {
			k |= 2
		}
		if b > 127 {
			k |= 4
		}
		// 90-97 are the bright foreground colors.
		return fmt.Sprintf("\033[9%dm", k)
	}
	return ""
}

// cubeLevel maps a 0-255 channel onto the 0-5 axis of the xterm 6x6x6 cube.
func cubeLevel(x uint8) int {
	return int(math.Round(float64(x) / 255 * 5))
}
