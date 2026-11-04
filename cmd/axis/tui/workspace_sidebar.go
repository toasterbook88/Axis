package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/models"
)

// renderSidebar draws the cluster context column: health, memory, every
// node with its RAM use, and the snapshot's warnings. Exactly width ×
// height, with a faint rule on its left edge. Values come only from the
// cached snapshot the dashboard loaded.
func renderSidebar(d Model, width, height int) []string {
	g := d.glyphs
	inner := width - 3 // rule, space, ..., space
	section := func(title string) string { return styleColHdr.Render(strings.ToUpper(title)) }

	lines := []string{brand() + styleFaint.Render("  v"+buildinfo.Version), ""}
	switch {
	case d.snapshot == nil && d.loading:
		lines = append(lines, styleMuted.Render("loading snapshot"+g.ellipsis))
	case d.snapshot == nil:
		lines = append(lines, styleBad.Render("no snapshot"), styleMuted.Render("ctrl+x d for setup steps"))
	default:
		lines = append(lines, sidebarCluster(d, inner)...)
	}

	keys := []string{
		"",
		section("Keys"),
		styleKey.Render("ctrl+o") + styleMuted.Render(" commands"),
		styleKey.Render("ctrl+x d") + styleMuted.Render(" dashboard"),
		styleKey.Render("ctrl+x h") + styleMuted.Render(" help"),
	}
	// Keys stay pinned to the bottom when there is room for them.
	if len(lines)+len(keys) <= height {
		for len(lines)+len(keys) < height {
			lines = append(lines, "")
		}
		lines = append(lines, keys...)
	}

	out := fitBlock(strings.Join(lines, "\n"), inner, height)
	rule := styleFaint.Render(lipgloss.NormalBorder().Left)
	if !g.unicode {
		rule = styleFaint.Render("|")
	}
	for i := range out {
		out[i] = rule + " " + out[i] + " "
	}
	return out
}

func sidebarCluster(d Model, inner int) []string {
	g := d.glyphs
	snap := d.snapshot
	section := func(title string) string { return styleColHdr.Render(strings.ToUpper(title)) }
	n := len(snap.Nodes)
	up := reachable(snap.Nodes)
	lvl := levelOK
	if up < n {
		lvl = levelWarn
	}
	if up == 0 {
		lvl = levelBad
	}

	lines := []string{
		section("Cluster"),
		lvl.style().Render(fmt.Sprintf("%s %d/%d reachable", healthGlyph(lvl, g), up, n)),
		ageLevel(d.observedAt, d.now()).style().Render(formatAge(d.observedAt, d.now())) +
			styleFaint.Render(g.sep+sourceLabel(d.source)),
	}
	if s := snap.Summary; s.TotalRAMMB > 0 {
		frac := float64(s.TotalRAMMB-s.TotalFreeRAMMB) / float64(s.TotalRAMMB)
		lines = append(lines, styleMuted.Render("mem ")+bar(frac, max(inner-9, 4), g, lipgloss.NewStyle())+" "+
			usageLevel(frac).style().Render(fmt.Sprintf("%3d%%", int(frac*100+0.5))))
	}

	lines = append(lines, "", section("Nodes"))
	for _, node := range snap.Nodes {
		lines = append(lines, sidebarNode(node, inner, g))
	}

	if len(snap.Warnings) > 0 {
		lines = append(lines, "", section(fmt.Sprintf("Alerts %d", len(snap.Warnings))))
		for i, w := range snap.Warnings {
			if i == 4 {
				lines = append(lines, styleFaint.Render(fmt.Sprintf("+%d more in dashboard", len(snap.Warnings)-4)))
				break
			}
			lines = append(lines, styleWarn.Render(g.warning+" ")+styleText.Render(truncPlain(w.Node+" "+w.Kind, inner-2, g.ellipsis)))
		}
	}
	return lines
}

// sidebarNode is one compact row: status glyph, name, RAM use or state.
func sidebarNode(node models.NodeFacts, inner int, g glyphSet) string {
	glyph, lvl := statusGlyph(node.Status, g)
	var right string
	switch {
	case node.Status == models.StatusUnreachable || node.Status == models.StatusError:
		right = styleBad.Render("down")
	default:
		if frac, ok := ramUsedFrac(node.Resources); ok {
			right = usageLevel(frac).style().Render(fmt.Sprintf("%3d%%", int(frac*100+0.5)))
		} else {
			right = styleFaint.Render(" n/a")
		}
	}
	name := styleText.Render(padPlain(node.Name, max(inner-2-lipgloss.Width(right)-1, 1), g.ellipsis))
	return lvl.style().Render(glyph) + " " + name + " " + right
}

func sourceLabel(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// renderStatusBar is the bottom line: where you are on the left; on the
// right either the leader menu (after ctrl+x) or the main shortcuts.
func renderStatusBar(w Workspace) string {
	where := "agent"
	if w.view == viewDashboard {
		where = "dashboard"
	}
	left := " " + brand() + styleMuted.Render(" "+where)

	var hints []string
	if w.leader {
		hints = append(hints, styleAccent.Render("ctrl+x"))
		for _, a := range workspaceActions {
			hints = append(hints, styleKey.Render(a.key)+" "+styleMuted.Render(a.label))
		}
	} else {
		if w.view == viewDashboard {
			hints = append(hints, styleKey.Render("a")+" "+styleMuted.Render("ask about node"),
				styleKey.Render("esc")+" "+styleMuted.Render("back"))
		}
		hints = append(hints,
			styleKey.Render("ctrl+o")+" "+styleMuted.Render("commands"),
			styleKey.Render("ctrl+x")+" "+styleMuted.Render("leader"),
			styleKey.Render("f2")+" "+styleMuted.Render("dashboard"),
			styleKey.Render("ctrl+c")+" "+styleMuted.Render("quit"))
	}
	right := joinFit(hints, "  ", max(w.width-lipgloss.Width(left)-3, 0))
	gap := w.width - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right + " "
}

func healthGlyph(l level, g glyphSet) string {
	switch l {
	case levelOK:
		return g.ok
	case levelBad:
		return g.bad
	default:
		return g.warn
	}
}
