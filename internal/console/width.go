package console

import (
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// clipWidth truncates s to at most w display columns. It measures per rune so
// a wide rune that would straddle the limit is dropped whole rather than split.
// Some clusters are wider than their runes (an emoji followed by VS16 draws two
// columns from runes that measure one each), so the result is re-measured and
// trimmed until it fits.
func clipWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	used := 0
	out := s
	for i, r := range s {
		rw := lipgloss.Width(string(r))
		if used+rw > w {
			out = s[:i]
			break
		}
		used += rw
	}
	for out != "" && lipgloss.Width(out) > w {
		_, size := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-size]
	}
	return out
}
