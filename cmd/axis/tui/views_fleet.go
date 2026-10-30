package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/toasterbook88/axis/internal/models"
)

// Column keys, in display order.
const (
	colName  = "NODE"
	colRole  = "ROLE"
	colState = "STATE"
	colCPU   = "LOAD"
	colRAM   = "RAM"
	colGPU   = "GPU"
	colLink  = "LINK"
)

type column struct {
	title string
	width int
}

const (
	rowPrefix = 4 // cursor, space, status glyph, space
	colGap    = 2
	gaugeW    = 14
)

// dropOrder lists the columns removed first when the terminal is narrow.
var dropOrder = []string{colGPU, colLink, colRole, colState, colCPU, colRAM}

// tableColumns picks the columns that fit avail columns and sizes them.
// Low-priority columns drop out whole rather than squeezing every cell.
func tableColumns(nodes []models.NodeFacts, avail int) []column {
	cols := []column{
		{colName, 10}, {colRole, 7}, {colState, 11}, {colCPU, gaugeW},
		{colRAM, gaugeW}, {colGPU, 12}, {colLink, 13},
	}
	total := func() int {
		t := rowPrefix
		for i, c := range cols {
			t += c.width
			if i > 0 {
				t += colGap
			}
		}
		return t
	}
	for _, drop := range dropOrder {
		if total() <= avail {
			break
		}
		for i, c := range cols {
			if c.title == drop {
				cols = append(cols[:i], cols[i+1:]...)
				break
			}
		}
	}

	// Spend spare width on names first (up to the longest), then GPU.
	longest := 10
	for _, n := range nodes {
		longest = max(longest, lipgloss.Width(n.Name))
	}
	grow := func(title string, limit int) {
		for i := range cols {
			if cols[i].title == title {
				add := min(avail-total(), limit-cols[i].width)
				if add > 0 {
					cols[i].width += add
				}
			}
		}
	}
	grow(colName, min(longest, 28))
	grow(colGPU, 30)
	return cols
}

// tableRows is how many node rows fit on screen.
func (m Model) tableRows() int {
	return m.layout().tableH - 1
}

// renderTable renders the column header plus the visible node rows in
// exactly height lines.
func renderTable(m Model, width, height int) []string {
	g := m.glyphs
	if m.snapshot == nil || len(m.snapshot.Nodes) == 0 {
		return fitBlock(styleMuted.Render("  No nodes in this snapshot."), width, height)
	}
	nodes := m.snapshot.Nodes
	cols := tableColumns(nodes, width)

	var hdr strings.Builder
	hdr.WriteString(strings.Repeat(" ", rowPrefix))
	for i, c := range cols {
		if i > 0 {
			hdr.WriteString(strings.Repeat(" ", colGap))
		}
		if i == len(cols)-1 {
			// Unpadded, so the scroll position can use the slack.
			hdr.WriteString(styleColHdr.Render(truncPlain(c.title, c.width, g.ellipsis)))
			continue
		}
		hdr.WriteString(styleColHdr.Render(padPlain(c.title, c.width, g.ellipsis)))
	}
	header := hdr.String()
	rows := height - 1
	if len(nodes) > rows && rows > 0 {
		pos := fmt.Sprintf("%d-%d of %d", m.tableOffset+1, min(m.tableOffset+rows, len(nodes)), len(nodes))
		if lipgloss.Width(header)+2+len(pos) <= width {
			header += strings.Repeat(" ", width-lipgloss.Width(header)-len(pos)) + styleFaint.Render(pos)
		}
	}

	lines := []string{header}
	for i := m.tableOffset; i < len(nodes) && len(lines) < height; i++ {
		lines = append(lines, renderNodeRow(nodes[i], cols, i == m.cursor, width, g))
	}
	return fitBlock(strings.Join(lines, "\n"), width, height)
}

func renderNodeRow(node models.NodeFacts, cols []column, selected bool, width int, g glyphSet) string {
	bg := lipgloss.NewStyle()
	if selected {
		bg = bg.Background(colSelBg)
	}
	on := func(s lipgloss.Style) lipgloss.Style { return s.Inherit(bg) }
	cell := func(s lipgloss.Style, text string, w int) string {
		return on(s).Render(padPlain(text, w, g.ellipsis))
	}

	var b strings.Builder
	if selected {
		b.WriteString(on(styleAccent).Render(g.cursor))
	} else {
		b.WriteString(" ")
	}
	glyph, lvl := statusGlyph(node.Status, g)
	b.WriteString(bg.Render(" ") + on(lvl.style()).Render(glyph) + bg.Render(" "))

	down := node.Status == models.StatusUnreachable || node.Status == models.StatusError
	for i, c := range cols {
		if i > 0 {
			b.WriteString(bg.Render(strings.Repeat(" ", colGap)))
		}
		if down && (c.title == colCPU || c.title == colRAM || c.title == colGPU || c.title == colLink) {
			// No metrics exist for a node that did not answer: spend the
			// rest of the row on why, instead of empty gauges.
			msg := node.Error
			if msg == "" {
				msg = "no response"
			}
			rest := width - lipgloss.Width(b.String())
			b.WriteString(cell(styleBad, msg, max(rest, 0)))
			break
		}
		b.WriteString(renderCell(node, c, selected, g, bg, cell))
	}
	if gap := width - lipgloss.Width(b.String()); gap > 0 {
		b.WriteString(bg.Render(strings.Repeat(" ", gap)))
	}
	return b.String()
}

func renderCell(node models.NodeFacts, c column, selected bool, g glyphSet, bg lipgloss.Style,
	cell func(lipgloss.Style, string, int) string) string {
	switch c.title {
	case colName:
		s := styleText
		if selected {
			s = styleBold
		}
		return cell(s, node.Name, c.width)
	case colRole:
		if node.Role == "primary" {
			return cell(lipgloss.NewStyle().Foreground(colAccent2), "primary", c.width)
		}
		role := node.Role
		if role == "" {
			role = g.dash
		}
		return cell(styleMuted, role, c.width)
	case colState:
		_, lvl := statusGlyph(node.Status, g)
		return cell(lvl.style(), statusWord(node.Status), c.width)
	case colCPU:
		frac, ok := cpuLoadFrac(node.Resources)
		if !ok {
			return cell(styleFaint, "not observed", c.width)
		}
		return gauge(frac, c.width, g, bg)
	case colRAM:
		frac, ok := ramUsedFrac(node.Resources)
		if !ok {
			return cell(styleFaint, "not observed", c.width)
		}
		return gauge(frac, c.width, g, bg)
	case colGPU:
		return cell(styleText, gpuSummary(node, g), c.width)
	case colLink:
		link := linkLabel(node.NetworkClass)
		if node.SSHHandshakeLatencyMs > 0 {
			link += fmt.Sprintf(" %dms", node.SSHHandshakeLatencyMs)
		}
		s := styleMuted
		if node.NetworkClass == models.NetworkClassRelayed {
			s = styleWarn
		}
		return cell(s, link, c.width)
	}
	return cell(styleText, "", c.width)
}

// gpuSummary is the first GPU's model and VRAM, plus a count of the rest.
func gpuSummary(node models.NodeFacts, g glyphSet) string {
	if node.Resources == nil || len(node.Resources.GPUs) == 0 {
		return g.dash
	}
	gpu := node.Resources.GPUs[0]
	s := gpu.Model
	if gpu.VRAMMB > 0 {
		s += " " + gb(int64(gpu.VRAMMB))
	}
	if extra := len(node.Resources.GPUs) - 1; extra > 0 {
		s += fmt.Sprintf(" +%d", extra)
	}
	return s
}
