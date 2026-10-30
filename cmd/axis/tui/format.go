package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/toasterbook88/axis/internal/models"
)

// linkLabel names the observed network class. Only an observed direct-LAN
// path is called "lan"; an empty class is "not observed", never a guess.
func linkLabel(c models.NetworkClass) string {
	switch c {
	case models.NetworkClassDirectLAN:
		return "lan"
	case models.NetworkClassTailscale:
		return "tailnet"
	case models.NetworkClassVPN:
		return "vpn"
	case models.NetworkClassRelayed:
		return "relayed"
	case "":
		return "not observed"
	default:
		return "unknown"
	}
}

// formatAge renders how long ago t was, coarsely. A zero time is
// "age unknown" rather than a fabricated reading; clock skew that puts t in
// the future reads as "just now".
func formatAge(t, now time.Time) string {
	if t.IsZero() {
		return "age unknown"
	}
	d := now.Sub(t)
	switch {
	case d < 30*time.Second:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", max(int(d/time.Minute), 1))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}

// ageLevel grades snapshot staleness: the daemon refreshes well inside five
// minutes, so older data deserves a warning and half an hour is stale.
func ageLevel(t, now time.Time) level {
	if t.IsZero() {
		return levelWarn
	}
	switch d := now.Sub(t); {
	case d > 30*time.Minute:
		return levelBad
	case d > 5*time.Minute:
		return levelWarn
	default:
		return levelOK
	}
}

// gb formats megabytes as gigabytes: one decimal below 10 GB.
func gb(mb int64) string {
	g := float64(mb) / 1024
	if g < 10 {
		return fmt.Sprintf("%.1f GB", g)
	}
	return fmt.Sprintf("%.0f GB", g)
}

// truncPlain shortens unstyled text to a display width, marking the cut.
func truncPlain(s string, width int, ellipsis string) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	ew := lipgloss.Width(ellipsis)
	if width <= ew {
		ellipsis, ew = "", 0
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+ew > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + ellipsis
}

// padPlain pads unstyled text to exactly width display columns.
func padPlain(s string, width int, ellipsis string) string {
	s = truncPlain(s, width, ellipsis)
	if gap := width - lipgloss.Width(s); gap > 0 {
		s += strings.Repeat(" ", gap)
	}
	return s
}

// fitLine clips a styled line to width (ANSI-aware) and pads it so every
// frame line is exactly width columns.
func fitLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(line) > width {
		line = lipgloss.NewStyle().MaxWidth(width).Render(line)
	}
	if gap := width - lipgloss.Width(line); gap > 0 {
		line += strings.Repeat(" ", gap)
	}
	return line
}

// fitBlock forces a rendered block to exactly width × height.
func fitBlock(block string, width, height int) []string {
	var lines []string
	if block != "" {
		lines = strings.Split(block, "\n")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for i := range lines {
		lines[i] = fitLine(lines[i], width)
	}
	return lines
}

// bar draws a utilization gauge: a colored fill on a faint track.
func bar(frac float64, width int, g glyphSet, bg lipgloss.Style) string {
	if width <= 0 {
		return ""
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac*float64(width) + 0.5)
	if frac > 0 && filled == 0 {
		filled = 1
	}
	fill := usageLevel(frac).style().Inherit(bg)
	track := lipgloss.NewStyle().Foreground(colTrack).Inherit(bg)
	return fill.Render(strings.Repeat(g.barFull, filled)) +
		track.Render(strings.Repeat(g.barEmpty, width-filled))
}

// gauge is a bar plus a right-aligned percentage, exactly width columns.
func gauge(frac float64, width int, g glyphSet, bg lipgloss.Style) string {
	pct := fmt.Sprintf("%3d%%", min(int(frac*100+0.5), 999))
	if width < 6 {
		return padPlain(pct, width, g.ellipsis)
	}
	bw := width - 1 - len(pct)
	return bar(frac, bw, g, bg) + bg.Render(" ") + usageLevel(frac).style().Inherit(bg).Render(pct)
}

// joinFit joins segments with sep, dropping trailing segments until the
// result fits width. Segments are pre-styled.
func joinFit(segs []string, sep string, width int) string {
	for n := len(segs); n > 0; n-- {
		out := strings.Join(segs[:n], sep)
		if lipgloss.Width(out) <= width {
			return out
		}
	}
	return ""
}

// ramUsedFrac is observed RAM in use (total − free) as a fraction.
func ramUsedFrac(r *models.Resources) (float64, bool) {
	if r == nil || r.RAMTotalMB <= 0 {
		return 0, false
	}
	used := r.RAMTotalMB - r.RAMFreeMB
	if used < 0 {
		used = 0
	}
	return float64(used) / float64(r.RAMTotalMB), true
}

// cpuLoadFrac is 1-minute load per core. It can exceed 1 when oversubscribed;
// callers clamp only the bar, not the number.
func cpuLoadFrac(r *models.Resources) (float64, bool) {
	if r == nil || r.CPUCores <= 0 {
		return 0, false
	}
	return r.Load1M / float64(r.CPUCores), true
}

func statusGlyph(s models.NodeStatus, g glyphSet) (string, level) {
	switch s {
	case models.StatusComplete:
		return g.ok, levelOK
	case models.StatusPartial:
		return g.warn, levelWarn
	case models.StatusUnreachable, models.StatusError:
		return g.bad, levelBad
	default:
		return g.unknown, levelNone
	}
}

func statusWord(s models.NodeStatus) string {
	switch s {
	case models.StatusComplete:
		return "ok"
	case "":
		return "not observed"
	default:
		return string(s)
	}
}

// reachable counts nodes that answered (complete or partial).
func reachable(nodes []models.NodeFacts) int {
	n := 0
	for _, node := range nodes {
		if node.Status == models.StatusComplete || node.Status == models.StatusPartial {
			n++
		}
	}
	return n
}
