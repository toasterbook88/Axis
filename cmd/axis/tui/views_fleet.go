package tui

import (
	"fmt"
	"strings"

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
	headerRow := renderRow(headers, headerStyle, m.width)

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

		// RAM gauge
		ramPct := 0.0
		if node.Resources != nil && node.Resources.RAMTotalMB > 0 {
			ramUsed := node.Resources.RAMTotalMB - node.Resources.RAMAllocatableMB
			ramPct = float64(ramUsed) / float64(node.Resources.RAMTotalMB)
		}
		ramBar := renderProgressBar(ramPct, 10)

		// GPU info
		gpuInfo := "--"
		if node.Resources != nil && len(node.Resources.GPUs) > 0 {
			gpu := node.Resources.GPUs[0]
			gpuInfo = fmt.Sprintf("%s %dMB", gpu.Model, gpu.VRAMMB)
		}

		// CPU load
		cpuPct := 0.0
		if node.Resources != nil {
			cpuPct = node.Resources.Load1M / float64(node.Resources.CPUCores)
			if cpuPct > 1.0 {
				cpuPct = 1.0
			}
		}
		cpuBar := renderProgressBar(cpuPct, 8)

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

		rows = append(rows, renderRow(rowData, rowStyle, m.width))
	}

	table := lipgloss.JoinVertical(lipgloss.Left,
		headerRow,
		strings.Join(rows, "\n"),
	)

	return table
}

// renderRow formats a single table row.
func renderRow(cells []string, style lipgloss.Style, _ int) string {
	// Simple fixed-width column layout
	colWidths := []int{15, 12, 8, 12, 15, 25, 10}
	var parts []string

	for i, cell := range cells {
		width := colWidths[i]
		if i < len(colWidths) {
			cell = fmt.Sprintf("%-*s", width, truncate(cell, width))
		}
		parts = append(parts, style.Render(cell))
	}

	return strings.Join(parts, " ")
}

// renderProgressBar creates a text-based progress bar.
func renderProgressBar(pct float64, width int) string {
	filled := int(pct * float64(width))
	if filled > width {
		filled = width
	}

	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return fmt.Sprintf("[%s] %3.0f%%", bar, pct*100)
}

// truncate shortens a string to maxLen.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "…"
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
