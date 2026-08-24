package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/toasterbook88/axis/internal/models"
)

// Style definitions for the fleet table.
var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Background(lipgloss.Color("63")).
			Foreground(lipgloss.Color("252")).
			Padding(0, 1)

	selectedRowStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("57")).
				Foreground(lipgloss.Color("252")).
				Padding(0, 1)

	normalRowStyle = lipgloss.NewStyle().
			Padding(0, 1)

	statusGreen  = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("●")
	statusYellow = lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Render("▲")
	statusRed    = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("✖")

	rolePrimary = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("235")).
			Background(lipgloss.Color("166")).
			Padding(0, 1).
			Render("PRIMARY")

	roleWorker = lipgloss.NewStyle().
			Foreground(lipgloss.Color("235")).
			Background(lipgloss.Color("248")).
			Padding(0, 1).
			Render("WORKER")

	transportLAN  = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render("[LAN]")
	transportTail = lipgloss.NewStyle().Foreground(lipgloss.Color("93")).Render("[TAIL]")
)

// renderFleetTable returns the node fleet overview table.
func renderFleetTable(m Model) string {
	if m.snapshot == nil || len(m.snapshot.Nodes) == 0 {
		return "No nodes discovered."
	}

	// Table header
	headers := []string{"NODE", "ROLE", "STATUS", "CPU", "RAM", "GPU", "TRANSPORT"}
	headerRow := renderRow(headers, headerStyle)

	// Node rows
	var rows []string
	for i, node := range m.snapshot.Nodes {
		statusIcon := statusGreen
		switch node.Status {
		case models.StatusPartial:
			statusIcon = statusYellow
		case models.StatusUnreachable, models.StatusError:
			statusIcon = statusRed
		}

		roleBadge := roleWorker
		if node.Role == "primary" {
			roleBadge = rolePrimary
		}

		transport := transportLAN
		if node.NetworkClass == "tailscale-direct" {
			transport = transportTail
		}

		// RAM gauge (constrain to 10 chars)
		ramPct := 0.0
		if node.Resources != nil && node.Resources.RAMTotalMB > 0 {
			ramUsed := node.Resources.RAMTotalMB - node.Resources.RAMAllocatableMB
			ramPct = float64(ramUsed) / float64(node.Resources.RAMTotalMB)
		}
		ramBar := renderProgressBar(ramPct, 8) // Reduced width to fit column

		// GPU info
		gpuInfo := "--"
		if node.Resources != nil && len(node.Resources.GPUs) > 0 {
			gpu := node.Resources.GPUs[0]
			gpuInfo = fmt.Sprintf("%s %dMB", gpu.Model, gpu.VRAMMB)
		}

		// CPU load (constrain to 8 chars)
		cpuPct := 0.0
		if node.Resources != nil && node.Resources.CPUCores > 0 {
			cpuPct = node.Resources.Load1M / float64(node.Resources.CPUCores)
			if cpuPct > 1.0 {
				cpuPct = 1.0
			}
		}
		cpuBar := renderProgressBar(cpuPct, 6) // Reduced width

		rowData := []string{
			node.Name,
			roleBadge,
			statusIcon,
			cpuBar,
			ramBar,
			gpuInfo,
			transport,
		}

		rowStyle := normalRowStyle
		if i == m.cursor {
			rowStyle = selectedRowStyle
		}

		rows = append(rows, renderRow(rowData, rowStyle))
	}

	table := lipgloss.JoinVertical(lipgloss.Left,
		headerRow,
		strings.Join(rows, "\n"),
	)

	return table
}

// renderRow formats a single table row with ANSI-aware padding.
func renderRow(cells []string, style lipgloss.Style) string {
	// Column widths (visual, excluding ANSI codes)
	colWidths := []int{15, 12, 8, 10, 12, 20, 10}
	var parts []string

	for i, cell := range cells {
		if i >= len(colWidths) {
			parts = append(parts, style.Render(cell))
			continue
		}
		width := colWidths[i]
		cell = padVisualWidth(cell, width)
		parts = append(parts, style.Render(cell))
	}

	return strings.Join(parts, " ")
}

// padVisualWidth pads a string to the specified visual width,
// accounting for ANSI escape codes and Unicode graphemes.
func padVisualWidth(s string, width int) string {
	visualLen := visualWidth(s)
	if visualLen >= width {
		return truncateVisual(s, width)
	}
	return s + strings.Repeat(" ", width-visualLen)
}

// visualWidth returns the display width of a string,
// excluding ANSI escape codes and counting Unicode graphemes.
func visualWidth(s string) int {
	// Strip ANSI codes for width calculation
	stripped := stripANSI(s)
	return utf8.RuneCountInString(stripped)
}

// truncateVisual truncates a string to the specified visual width.
func truncateVisual(s string, maxLen int) string {
	if visualWidth(s) <= maxLen {
		return s
	}
	// Simple truncation (could be enhanced for grapheme clusters)
	stripped := stripANSI(s)
	runes := []rune(stripped)
	if len(runes) > maxLen-1 {
		runes = runes[:maxLen-1]
	}
	// Re-apply ANSI prefix if present (simplified)
	return string(runes) + "…"
}

// stripANSI removes ANSI escape codes from a string.
func stripANSI(s string) string {
	var result strings.Builder
	inEscape := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if (s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z') || s[i] == '@' {
				inEscape = false
			}
			continue
		}
		result.WriteByte(s[i])
	}
	return result.String()
}

// renderProgressBar creates a text-based progress bar of specified width.
func renderProgressBar(pct float64, barWidth int) string {
	if barWidth <= 0 {
		barWidth = 8
	}
	filled := int(pct * float64(barWidth))
	if filled > barWidth {
		filled = barWidth
	}

	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
	return fmt.Sprintf("[%s]", bar)
}

// renderInspector returns the tabbed inspector panel.
func renderInspector(m Model) string {
	return renderInspectorEnhanced(m)
}

// renderFooter returns the status bar and keybindings.
func renderFooter(m Model) string {
	if m.statusMsg != "" {
		return lipgloss.NewStyle().
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("252")).
			Padding(0, 1).
			Width(m.width).
			Render(m.statusMsg)
	}

	keys := "[j/k] Navigate  [1-4] Tabs  [r] Refresh  [h/l] Switch Tab  [q] Quit"
	return lipgloss.NewStyle().
		Background(lipgloss.Color("63")).
		Foreground(lipgloss.Color("252")).
		Padding(0, 1).
		Width(m.width).
		Render(keys)
}
