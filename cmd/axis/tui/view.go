package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/ui"
)

// frameLayout is the row budget for one frame.
type frameLayout struct {
	w, h       int
	summaryH   int
	bodyH      int
	tableH     int // includes the column header row
	inspectorH int
}

// size is the terminal size, with a conventional default before the first
// WindowSizeMsg arrives.
func (m Model) size() (int, int) {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	return w, h
}

func (m Model) layout() frameLayout {
	w, h := m.size()
	l := frameLayout{w: w, h: h}
	if h >= 14 {
		l.summaryH = 1
	}
	l.bodyH = max(h-2-l.summaryH, 0) // header + footer
	nodes := 1
	if m.snapshot != nil {
		nodes = max(len(m.snapshot.Nodes), 1)
	}
	want := nodes + 1
	l.tableH = min(want, max(l.bodyH*55/100, 4))
	l.inspectorH = l.bodyH - l.tableH
	if l.inspectorH < 6 {
		// Too short for a useful inspector: give the table everything.
		l.tableH, l.inspectorH = l.bodyH, 0
	}
	l.tableH = min(l.tableH, l.bodyH)
	return l
}

// ViewWithLogo renders one full frame: exactly the terminal's width and
// height, so nothing wraps or scrolls off.
func ViewWithLogo(m Model) string {
	if m.quitting {
		return ""
	}
	l := m.layout()
	if m.modalActive {
		placed := lipgloss.Place(l.w, l.h, lipgloss.Center, lipgloss.Center, m.modal.View())
		return strings.Join(fitBlock(placed, l.w, l.h), "\n")
	}

	lines := []string{fitLine(renderHeader(m, l.w), l.w)}
	if l.summaryH > 0 {
		lines = append(lines, fitLine(renderSummary(m, l.w), l.w))
	}

	switch {
	case m.showHelp:
		lines = append(lines, centered(renderHelp(m), l.w, l.bodyH)...)
	case m.loading && m.snapshot == nil:
		lines = append(lines, centered(renderLoading(m, l.w, l.bodyH), l.w, l.bodyH)...)
	case m.loadErr != nil && m.snapshot == nil:
		lines = append(lines, centered(renderSnapshotRecovery(m.loadErr, l.w, m.glyphs), l.w, l.bodyH)...)
	default:
		lines = append(lines, renderTable(m, l.w, l.tableH)...)
		if l.inspectorH > 0 {
			lines = append(lines, renderInspector(m, l.w, l.inspectorH)...)
		}
	}

	lines = append(lines, fitLine(renderFooter(m, l.w), l.w))
	return strings.Join(fitBlock(strings.Join(lines, "\n"), l.w, l.h), "\n")
}

func centered(block string, w, h int) []string {
	return fitBlock(lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, block), w, h)
}

// brand renders the AXIS wordmark with the logo's cyan → magenta ramp.
func brand() string {
	ramp := []lipgloss.Color{"51", "45", "141", "171"}
	var b strings.Builder
	for i, r := range "AXIS" {
		b.WriteString(lipgloss.NewStyle().Foreground(ramp[i]).Bold(true).Render(string(r)))
	}
	return b.String()
}

// renderHeader: wordmark on the left; health, snapshot age, provenance and
// version on the right. The operator always sees which authority produced
// the view and how old it is.
func renderHeader(m Model, width int) string {
	g := m.glyphs
	left := " " + brand() + styleMuted.Render("  cluster")

	var segs []string
	if m.loading && m.snapshot != nil {
		segs = append(segs, styleAccent.Render(g.refreshing+" refreshing"))
	}
	if m.snapshot != nil {
		n := len(m.snapshot.Nodes)
		up := reachable(m.snapshot.Nodes)
		lvl := levelOK
		switch {
		case n == 0 || up == 0:
			lvl = levelBad
		case up < n:
			lvl = levelWarn
		default:
			for _, node := range m.snapshot.Nodes {
				if node.Status != models.StatusComplete {
					lvl = levelWarn
				}
			}
		}
		glyph := g.ok
		if lvl != levelOK {
			glyph = g.warn
		}
		segs = append(segs,
			lvl.style().Render(fmt.Sprintf("%s %d/%d reachable", glyph, up, n)),
			ageLevel(m.observedAt, m.now()).style().Render(formatAge(m.observedAt, m.now())),
		)
	} else {
		segs = append(segs, styleFaint.Render(g.unknown+" no snapshot"))
	}
	source := m.source
	if source == "" {
		source = "unknown"
	}
	segs = append(segs, styleMuted.Render(source), styleFaint.Render("v"+buildinfo.Version))

	right := joinFit(segs, styleFaint.Render(g.sep), max(width-lipgloss.Width(left)-3, 0))
	gap := width - lipgloss.Width(left) - lipgloss.Width(right) - 1
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right + " "
}

// renderSummary is the cluster-wide strip: memory use from the snapshot's
// own aggregates, and the warning count.
func renderSummary(m Model, width int) string {
	g := m.glyphs
	if m.snapshot == nil {
		return ""
	}
	s := m.snapshot.Summary
	var segs []string
	if s.TotalRAMMB > 0 {
		used := s.TotalRAMMB - s.TotalFreeRAMMB
		frac := float64(used) / float64(s.TotalRAMMB)
		segs = append(segs, styleMuted.Render("memory ")+bar(frac, 12, g, lipgloss.NewStyle())+" "+
			usageLevel(frac).style().Render(fmt.Sprintf("%d%%", int(frac*100+0.5)))+
			styleText.Render(fmt.Sprintf("  %s / %s used", gb(used), gb(s.TotalRAMMB))))
		if s.TotalAllocatableMB > 0 {
			segs = append(segs, styleText.Render(gb(s.TotalAllocatableMB))+styleMuted.Render(" allocatable"))
		}
		if s.TotalReservedMB > 0 {
			segs = append(segs, styleText.Render(gb(s.TotalReservedMB))+styleMuted.Render(" reserved"))
		}
	} else {
		segs = append(segs, styleFaint.Render("cluster memory not observed"))
	}

	warn := styleFaint.Render("no warnings")
	if n := len(m.snapshot.Warnings); n > 0 {
		word := "warnings"
		if n == 1 {
			word = "warning"
		}
		warn = styleWarn.Render(fmt.Sprintf("%s %d %s", g.warning, n, word))
	}
	room := max(width-lipgloss.Width(warn)-4, 0)
	fitted := joinFit(segs, styleFaint.Render(g.sep), room)
	if fitted == "" && s.TotalRAMMB > 0 {
		// Narrow terminal: keep the headline number at least.
		frac := float64(s.TotalRAMMB-s.TotalFreeRAMMB) / float64(s.TotalRAMMB)
		fitted = joinFit([]string{styleMuted.Render("memory ") + bar(frac, 8, g, lipgloss.NewStyle()) + " " +
			usageLevel(frac).style().Render(fmt.Sprintf("%d%%", int(frac*100+0.5)))}, "", room)
	}
	left := " " + fitted
	gap := width - lipgloss.Width(left) - lipgloss.Width(warn) - 1
	if gap < 2 {
		return left
	}
	return left + strings.Repeat(" ", gap) + warn + " "
}

type keyHint struct{ key, desc string }

func footerHints(g glyphSet) []keyHint {
	return []keyHint{
		{g.navKeys, "select"},
		{g.tabKeys, "tab"},
		{"p", "place"},
		{"r", "refresh"},
		{"?", "help"},
		{"q", "quit"},
	}
}

// renderFooter keeps the key legend visible and puts transient status on
// the right, so a status message never hides the interaction path.
func renderFooter(m Model, width int) string {
	var hints []string
	for _, h := range footerHints(m.glyphs) {
		hints = append(hints, styleKey.Render(h.key)+" "+styleMuted.Render(h.desc))
	}
	isErr := strings.HasPrefix(m.statusMsg, "Error") || strings.HasPrefix(m.statusMsg, "Refresh failed")
	hintRoom := max(width-2, 0)
	if isErr {
		// A failure outranks the key legend: give it the space first and
		// let the hints take what is left.
		hintRoom = max(width-lipgloss.Width(m.statusMsg)-5, 0)
	}
	left := " " + joinFit(hints, "  ", hintRoom)
	if m.statusMsg == "" {
		return left
	}
	room := width - lipgloss.Width(left) - 4
	if room < 12 && !isErr {
		return left
	}
	s := styleMuted
	if isErr {
		s = styleBad
	}
	status := s.Render(truncPlain(m.statusMsg, max(room, 1), m.glyphs.ellipsis))
	return left + strings.Repeat(" ", width-lipgloss.Width(left)-lipgloss.Width(status)-1) + status + " "
}

func renderHelp(m Model) string {
	g := m.glyphs
	row := func(k, d string) string { return styleKey.Render(padPlain(k, 14, "")) + styleText.Render(d) }
	source := m.source
	if source == "" {
		source = "unknown"
	}
	lines := []string{
		styleBold.Render("Keys"),
		row(g.helpNav, "select node"),
		row("g  G", "first / last node"),
		row(g.helpTab, "previous / next tab"),
		row("tab  1-4", "cycle / jump to tab"),
		row("p", "placement preview"),
		row("r", "refresh now"),
		row("?  esc", "close this help"),
		row("q", "quit"),
		"",
		styleBold.Render("Status"),
		styleOK.Render(g.ok) + styleText.Render(" ok   ") +
			styleWarn.Render(g.warn) + styleText.Render(" partial   ") +
			styleBad.Render(g.bad) + styleText.Render(" unreachable / error"),
		"",
		styleBold.Render("Data"),
		styleMuted.Render("Read-only view of the " + source + " snapshot."),
		styleMuted.Render("Placement is advisory; nothing here runs tasks."),
	}
	return lipgloss.NewStyle().
		Border(g.border).BorderForeground(colAccent).
		Padding(1, 3).
		Render(strings.Join(lines, "\n"))
}

func renderLoading(m Model, w, h int) string {
	msg := styleMuted.Render("Loading cluster snapshot" + m.glyphs.ellipsis)
	var logo []string
	if m.glyphs.unicode {
		if w >= ui.LogoWidth()+4 {
			logo = ui.LogoLines(m.colorDepth)
		}
	} else if w >= ui.ASCIILogoWidth()+4 {
		logo = ui.ASCIILogoLines()
	}
	if len(logo) == 0 || h < len(logo)+3 {
		return msg
	}
	return lipgloss.JoinVertical(lipgloss.Center, strings.Join(logo, "\n"), "", msg)
}

func renderSnapshotRecovery(err error, width int, g glyphSet) string {
	command := lipgloss.NewStyle().Foreground(lipgloss.Color("229"))
	detail := "unknown error"
	if err != nil {
		detail = err.Error()
	}
	inner := max(min(width-8, 76), 20)
	body := lipgloss.JoinVertical(lipgloss.Left,
		styleBad.Bold(true).Render("Snapshot unavailable"),
		styleMuted.Render(truncPlain(detail, inner, g.ellipsis)),
		"",
		styleText.Render("AXIS needs observed cluster state before it can show or place work."),
		"",
		styleBold.Render("First-time setup"),
		"  1. "+command.Render("axis init"),
		"  2. "+command.Render("axis daemon service install"),
		"",
		styleBold.Render("Already configured"),
		"  "+g.ok+" "+command.Render("axis daemon start")+" in another terminal",
		"  "+g.ok+" Press "+command.Render("r")+" here to retry immediately",
		"",
		styleFaint.Render("The TUI starts nothing until you run one of these commands."),
	)
	return lipgloss.NewStyle().
		Border(g.border).BorderForeground(colBad).
		Padding(1, 2).
		Render(body)
}
