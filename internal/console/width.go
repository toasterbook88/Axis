package console

import "github.com/charmbracelet/lipgloss"

// clipWidth truncates s to at most w display columns. It measures per rune so
// a wide rune that would straddle the limit is dropped whole rather than split.
func clipWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	used := 0
	for i, r := range s {
		rw := lipgloss.Width(string(r))
		if used+rw > w {
			return s[:i]
		}
		used += rw
	}
	return s
}
