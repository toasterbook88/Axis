package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/models"
)

// tickMsg triggers a snapshot refresh.
type tickMsg struct{}

// tickCmd returns a command that sends a tickMsg after the given duration.
func tickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return tickMsg{}
	})
}

// refreshInterval is the auto-refresh duration for the dashboard.
const refreshInterval = 30 * time.Second

// UpdateWithRefresh extends the base Update to handle auto-refresh ticks.
func UpdateWithRefresh(m Model, msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Viewport disabled - using direct rendering instead
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit

		case "j", "down":
			if m.snapshot != nil && m.cursor < len(m.snapshot.Nodes)-1 {
				m.cursor++
			}

		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}

		case "r":
			m.loading = true
			m.lastRefresh = "loading..."
			m.statusMsg = "Refreshing cluster snapshot..."
			return m, loadSnapshotCmd()

		case "1":
			m.activeTab = 0
			m.statusMsg = "Tab: Hardware Details"
		case "2":
			m.activeTab = 1
			m.statusMsg = "Tab: AI Backends"
		case "3":
			m.activeTab = 2
			m.statusMsg = "Tab: Storage"
		case "4":
			m.activeTab = 3
			m.statusMsg = "Tab: Reservations"

		case "h", "left":
			if m.activeTab > 0 {
				m.activeTab--
			}
		case "l", "right":
			if m.activeTab < 3 {
				m.activeTab++
			}

		case "?":
			m.statusMsg = "j/k: navigate | 1-4: tabs | r: refresh | h/l: switch tab | q: quit"
			return m, nil

		case "enter":
			if m.snapshot != nil && m.cursor < len(m.snapshot.Nodes) {
				node := m.snapshot.Nodes[m.cursor]
				m.statusMsg = fmt.Sprintf("Selected: %s (%s)", node.Name, node.Role)
			}
		}

	case snapshotLoadedMsg:
		m.snapshot = msg.Snapshot
		m.loading = false
		m.lastRefresh = msg.Timestamp
		m.loadErr = nil
		m.statusMsg = fmt.Sprintf("Snapshot loaded: %d nodes", len(m.snapshot.Nodes))
		// Schedule next refresh
		return m, tickCmd(refreshInterval)

	case loadErrMsg:
		m.loading = false
		m.loadErr = msg.Err
		m.statusMsg = "Error: " + msg.Err.Error()
		// Retry after delay
		return m, tickCmd(5 * time.Second)

	case tickMsg:
		// Auto-refresh trigger
		if !m.loading {
			m.loading = true
			m.lastRefresh = "refreshing..."
			return m, loadSnapshotCmd()
		}
		// Still loading, schedule next tick
		return m, tickCmd(refreshInterval)
	}

	// Update (no-op for direct rendering)
	return m, nil
}

// ViewWithLogo renders the TUI with the ASCII logo.
func ViewWithLogo(m Model) string {
	if m.quitting {
		return ""
	}

	var b strings.Builder

	// Header with logo
	b.WriteString(renderHeaderWithLogo(m))
	b.WriteString("\n")

	// Main content
	if m.loading && m.snapshot == nil {
		// Initial loading state with logo
		b.WriteString("\n\n")
		b.WriteString("  ")
		b.WriteString(RenderLogo())
		b.WriteString("\n\n")
		b.WriteString("  Loading cluster snapshot...")
	} else if m.loadErr != nil {
		b.WriteString("Error: ")
		b.WriteString(m.loadErr.Error())
	} else {
		b.WriteString(renderFleetTable(m))
		b.WriteString("\n")
		b.WriteString(renderInspector(m))
	}

	b.WriteString("\n")
	b.WriteString(renderFooter(m))

	return b.String()
}

// renderHeaderWithLogo renders the header with status bar.
func renderHeaderWithLogo(m Model) string {
	nodeCount := 0
	healthyCount := 0
	if m.snapshot != nil {
		nodeCount = len(m.snapshot.Nodes)
		for _, n := range m.snapshot.Nodes {
			if n.Status == models.StatusComplete {
				healthyCount++
			}
		}
	}

	version := buildinfo.Version
	status := fmt.Sprintf("Nodes: %d/%d  |  Last: %s  |  v%s",
		healthyCount, nodeCount, m.lastRefresh, version)

	header := lipgloss.JoinHorizontal(lipgloss.Center,
		RenderLogoSmall(),
		"  |  ",
		lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(status),
	)

	return headerStyle.Width(m.width).Render(header)
}

// renderInspectorEnhanced renders the tabbed inspector with real data.
func renderInspectorEnhanced(m Model) string {
	if m.snapshot == nil || m.cursor >= len(m.snapshot.Nodes) {
		return "Select a node to view details"
	}

	node := m.snapshot.Nodes[m.cursor]

	tabs := []string{"Details", "Backends", "Storage", "Reservations"}
	var tabContent string

	switch m.activeTab {
	case 0:
		tabContent = renderDetailsTabEnhanced(node)
	case 1:
		tabContent = renderBackendsTabEnhanced(node)
	case 2:
		tabContent = renderStorageTabEnhanced(node)
	case 3:
		tabContent = renderReservationsTabEnhanced()
	}

	// Tab bar with active highlight
	var tabParts []string
	for i, tab := range tabs {
		style := lipgloss.NewStyle().Padding(0, 1)
		if i == m.activeTab {
			style = style.Bold(true).
				Background(lipgloss.Color("63")).
				Foreground(lipgloss.Color("252"))
		}
		tabParts = append(tabParts, style.Render(fmt.Sprintf("[%d] %s", i+1, tab)))
	}
	tabBar := strings.Join(tabParts, " ")

	content := lipgloss.JoinVertical(lipgloss.Left,
		tabBar,
		"─"+strings.Repeat("─", m.width-2),
		tabContent,
	)

	return content
}

// renderDetailsTabEnhanced shows detailed hardware specs.
func renderDetailsTabEnhanced(node models.NodeFacts) string {
	var lines []string

	// CPU info
	cpuCores := 0
	cpuModel := "Unknown"
	if node.Resources != nil {
		cpuCores = node.Resources.CPUCores
		cpuModel = node.Resources.CPUModel
	}
	lines = append(lines, fmt.Sprintf("CPU: %s (%d cores)", cpuModel, cpuCores))

	// OS info
	lines = append(lines, fmt.Sprintf("OS: %s %s (%s)", node.OS, node.OSVersion, node.Arch))

	// Memory
	if node.Resources != nil {
		ramTotal := node.Resources.RAMTotalMB / 1024
		ramFree := node.Resources.RAMFreeMB / 1024
		lines = append(lines, fmt.Sprintf("RAM: %d GB total, %d GB free", ramTotal, ramFree))

		// Pressure
		if node.Resources.Pressure != "none" && node.Resources.Pressure != "" {
			lines = append(lines, fmt.Sprintf("Pressure: %s", node.Resources.Pressure))
		}

		// Thermal
		if node.Resources.ThermalState != "" && node.Resources.ThermalState != "nominal" {
			lines = append(lines, fmt.Sprintf("Thermal: %s", node.Resources.ThermalState))
		}

		// Battery
		if node.Resources.BatteryPercent != nil {
			lines = append(lines, fmt.Sprintf("Battery: %d%% (%s)",
				*node.Resources.BatteryPercent, node.Resources.PowerSource))
		}
	}

	// GPU info
	if node.Resources != nil && len(node.Resources.GPUs) > 0 {
		lines = append(lines, "GPUs:")
		for _, gpu := range node.Resources.GPUs {
			vramStr := ""
			if gpu.VRAMMB > 0 {
				vramStr = fmt.Sprintf(" (%d MB)", gpu.VRAMMB)
			}
			caps := ""
			if len(gpu.Capabilities) > 0 {
				caps = fmt.Sprintf(" [%s]", strings.Join(gpu.Capabilities, ", "))
			}
			lines = append(lines, fmt.Sprintf("  • %s%s%s", gpu.Model, vramStr, caps))
		}
	}

	return strings.Join(lines, "\n")
}

// renderBackendsTabEnhanced shows AI backend status.
func renderBackendsTabEnhanced(node models.NodeFacts) string {
	var lines []string

	// Ollama
	if node.Ollama != nil {
		if node.Ollama.Installed {
			status := "● Running"
			if !node.Ollama.Running {
				status = "○ Stopped"
			}
			lines = append(lines, fmt.Sprintf("Ollama: %s %s (port %d)",
				node.Ollama.Version, status, node.Ollama.Port))

			if len(node.Ollama.Models) > 0 {
				lines = append(lines, fmt.Sprintf("  Models: %s", strings.Join(node.Ollama.Models, ", ")))
			}
		} else {
			lines = append(lines, "Ollama: Not installed")
		}
	}

	// Resident models
	if len(node.ResidentModels) > 0 {
		lines = append(lines, "Resident Models:")
		for _, model := range node.ResidentModels {
			vramStr := ""
			if model.SizeVRAMMB > 0 {
				vramStr = fmt.Sprintf(" (%d MB VRAM)", model.SizeVRAMMB)
			}
			lines = append(lines, fmt.Sprintf("  • %s on %s:%d%s",
				model.Name, model.Runtime, model.Port, vramStr))
		}
	}

	if len(lines) == 0 {
		return "No AI backends detected on this node."
	}

	return strings.Join(lines, "\n")
}

// renderStorageTabEnhanced shows storage mount information.
func renderStorageTabEnhanced(node models.NodeFacts) string {
	if node.Resources == nil || len(node.Resources.Volumes) == 0 {
		return "No storage mounts detected."
	}

	var lines []string
	lines = append(lines, "Storage Volumes:")

	for _, vol := range node.Resources.Volumes {
		pctFree := 0
		if vol.TotalGB > 0 {
			pctFree = int((vol.FreeGB * 100) / vol.TotalGB)
		}

		classStr := vol.Class
		if classStr == "" {
			classStr = "unknown"
		}

		roleStr := ""
		if vol.Role == "root" {
			roleStr = " [root]"
		}

		lines = append(lines, fmt.Sprintf("  • %s (%s, %d/%d GB, %d%% free)%s",
			vol.Mount, classStr, vol.FreeGB, vol.TotalGB, pctFree, roleStr))
	}

	return strings.Join(lines, "\n")
}

// renderReservationsTabEnhanced shows active reservations.
func renderReservationsTabEnhanced() string {
	// Placeholder - would integrate with reservation system
	return "No active reservations.\n\nReservations are managed via:\n  axis reservations list\n  axis reservations release <id>"
}
