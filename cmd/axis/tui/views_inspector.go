package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/toasterbook88/axis/internal/models"
)

// inspectorTabs are the per-node detail views, selected with 1-4.
var inspectorTabs = []string{"Overview", "Models", "Storage", "Reservations"}

// renderInspector draws the bordered detail panel for the selected node in
// exactly width × height.
func renderInspector(m Model, width, height int) []string {
	g := m.glyphs
	if height < 3 || width < 8 {
		return fitBlock("", width, height)
	}
	innerW := max(width-4, 1) // border + one column of padding each side
	innerH := height - 2

	var body []string
	if m.snapshot == nil || m.cursor >= len(m.snapshot.Nodes) {
		body = []string{styleMuted.Render("Select a node to view details")}
	} else {
		node := m.snapshot.Nodes[m.cursor]
		body = append(body, inspectorTitle(node, m.activeTab, innerW, g))
		body = append(body, styleFaint.Render(strings.Repeat(g.rule, innerW)))
		body = append(body, inspectorContent(m, node, innerW)...)
	}

	if len(body) > innerH {
		hidden := len(body) - innerH + 1
		body = append(body[:innerH-1], styleFaint.Render(fmt.Sprintf("%s %d more line(s); enlarge the terminal", g.ellipsis, hidden)))
	}
	for len(body) < innerH {
		body = append(body, "")
	}
	for i := range body {
		body[i] = fitLine(body[i], innerW)
	}

	box := lipgloss.NewStyle().
		Border(g.border).
		BorderForeground(colFaint).
		Padding(0, 1).
		Render(strings.Join(body, "\n"))
	return fitBlock(box, width, height)
}

func inspectorTitle(node models.NodeFacts, active, width int, g glyphSet) string {
	glyph, lvl := statusGlyph(node.Status, g)
	name := lvl.style().Render(glyph) + " " + styleBold.Render(node.Name)

	tabs := func(numbered bool) string {
		parts := make([]string, len(inspectorTabs))
		for i, t := range inspectorTabs {
			label := t
			if numbered {
				label = fmt.Sprintf("%d %s", i+1, t)
			}
			if i == active {
				parts[i] = styleTabOn.Render(label)
			} else {
				parts[i] = styleTabOff.Render(label)
			}
		}
		return strings.Join(parts, "   ")
	}
	for _, numbered := range []bool{true, false} {
		t := tabs(numbered)
		if lipgloss.Width(name)+4+lipgloss.Width(t) <= width {
			return name + strings.Repeat(" ", width-lipgloss.Width(name)-lipgloss.Width(t)) + t
		}
	}
	// Too narrow for every tab: show only the active one.
	only := styleTabOn.Render(fmt.Sprintf("%d/%d %s", active+1, len(inspectorTabs), inspectorTabs[active]))
	return name + "  " + only
}

func inspectorContent(m Model, node models.NodeFacts, width int) []string {
	switch m.activeTab {
	case 1:
		return modelsTab(node, m.glyphs)
	case 2:
		return storageTab(node, width, m.glyphs)
	case 3:
		return reservationsTab(m, node)
	default:
		return overviewTab(m, node, width)
	}
}

// kv renders one "label  value" line with an aligned muted label.
func kv(label, value string) string {
	return styleMuted.Render(padPlain(label, 12, "")) + value
}

func notObserved() string { return styleFaint.Render("not observed") }

func overviewTab(m Model, node models.NodeFacts, width int) []string {
	g := m.glyphs
	var lines []string

	_, lvl := statusGlyph(node.Status, g)
	status := lvl.style().Render(statusWord(node.Status))
	var why []string
	for _, r := range node.PartialReasons {
		if r.Message != "" {
			why = append(why, r.Probe+": "+r.Message)
		} else {
			why = append(why, r.Probe)
		}
	}
	if node.Error != "" {
		why = append(why, node.Error)
	}
	if len(why) > 0 {
		status += styleMuted.Render(" " + g.dash + " " + strings.Join(why, "; "))
	}
	lines = append(lines, kv("Status", status))

	if node.OS != "" {
		sys := strings.TrimSpace(node.OS + " " + node.OSVersion)
		if node.Arch != "" {
			sys += g.sep + node.Arch
		}
		lines = append(lines, kv("System", styleText.Render(sys)))
	} else {
		lines = append(lines, kv("System", notObserved()))
	}

	r := node.Resources
	if r == nil {
		lines = append(lines, kv("Hardware", notObserved()))
	} else {
		var cpuParts []string
		if r.CPUModel != "" {
			cpuParts = append(cpuParts, r.CPUModel)
		}
		if r.CPUCores > 0 {
			cpuParts = append(cpuParts, fmt.Sprintf("%d cores", r.CPUCores))
		} else {
			cpuParts = append(cpuParts, "cores not observed")
		}
		cpu := strings.Join(cpuParts, g.sep)
		if frac, ok := cpuLoadFrac(r); ok {
			cpu += g.sep + fmt.Sprintf("load %.2f ", r.Load1M) + usageLevel(min(frac, 1.0)).style().Render(fmt.Sprintf("(%d%%)", int(frac*100+0.5)))
		}
		lines = append(lines, kv("CPU", styleText.Render(cpu)))

		if frac, ok := ramUsedFrac(r); ok {
			barW := min(16, max(width/5, 6))
			mem := bar(frac, barW, g, lipgloss.NewStyle()) + " " +
				styleText.Render(fmt.Sprintf("%d%%  %s / %s used", int(frac*100+0.5), gb(r.RAMTotalMB-r.RAMFreeMB), gb(r.RAMTotalMB)))
			if r.RAMAllocatableMB > 0 {
				mem += styleMuted.Render(g.sep + gb(r.RAMAllocatableMB) + " allocatable")
			}
			lines = append(lines, kv("Memory", mem))
		} else {
			lines = append(lines, kv("Memory", notObserved()))
		}

		if p := r.Pressure; p != "" && p != "none" {
			s := styleWarn
			if p == "high" {
				s = styleBad
			}
			lines = append(lines, kv("Pressure", s.Render(p)))
		}
		if t := r.ThermalState; t != "" && t != "nominal" {
			s := styleWarn
			if t == "serious" || t == "critical" {
				s = styleBad
			}
			lines = append(lines, kv("Thermal", s.Render(t)))
		}
		if r.BatteryPercent != nil {
			lines = append(lines, kv("Power", styleText.Render(fmt.Sprintf("battery %d%%%s%s", *r.BatteryPercent, g.sep, r.PowerSource))))
		}
		for i, gpu := range r.GPUs {
			label := ""
			if i == 0 {
				label = "GPU"
			}
			desc := gpu.Model
			if gpu.VRAMMB > 0 {
				desc += g.sep + gb(int64(gpu.VRAMMB)) + " VRAM"
			}
			if len(gpu.Capabilities) > 0 {
				desc += styleMuted.Render(" [" + strings.Join(gpu.Capabilities, ", ") + "]")
			}
			lines = append(lines, kv(label, styleText.Render(desc)))
		}
	}

	link := linkLabel(node.NetworkClass)
	if node.SSHHandshakeLatencyMs > 0 {
		link += fmt.Sprintf("%sssh handshake %d ms", g.sep, node.SSHHandshakeLatencyMs)
	}
	lines = append(lines, kv("Link", styleText.Render(link)))
	if !node.CollectedAt.IsZero() {
		lines = append(lines, kv("Collected", styleText.Render(formatAge(node.CollectedAt, m.now()))))
	}

	if m.snapshot != nil {
		for _, w := range m.snapshot.Warnings {
			if w.Node == node.Name {
				lines = append(lines, styleWarn.Render(g.warning+" "+w.Kind+": ")+styleText.Render(w.Message))
			}
		}
	}
	return lines
}

func modelsTab(node models.NodeFacts, g glyphSet) []string {
	var lines []string
	if o := node.Ollama; o != nil {
		switch {
		case !o.Installed:
			lines = append(lines, kv("Ollama", styleMuted.Render("not installed")))
		case o.Running:
			s := styleOK.Render(g.ok+" running") + styleText.Render(" "+o.Version)
			if o.Port > 0 {
				s += styleMuted.Render(fmt.Sprintf("%sport %d", g.sep, o.Port))
			}
			lines = append(lines, kv("Ollama", s))
		default:
			lines = append(lines, kv("Ollama", styleWarn.Render(g.unknown+" installed, not running")+styleText.Render(" "+o.Version)))
		}
		for i, name := range o.Models {
			label := ""
			if i == 0 {
				label = "Available"
			}
			lines = append(lines, kv(label, styleText.Render(name)))
		}
	}
	if len(node.ResidentModels) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, styleColHdr.Render("Loaded in memory"))
		for _, rm := range node.ResidentModels {
			s := styleOK.Render(g.ok) + " " + styleBold.Render(rm.Name)
			var meta []string
			if rm.Runtime != "" {
				meta = append(meta, rm.Runtime)
			}
			if rm.Port > 0 {
				meta = append(meta, fmt.Sprintf("port %d", rm.Port))
			}
			if rm.SizeVRAMMB > 0 {
				meta = append(meta, gb(rm.SizeVRAMMB)+" VRAM")
			}
			if rm.SizeRAMMB > 0 {
				meta = append(meta, gb(rm.SizeRAMMB)+" RAM")
			}
			if len(meta) > 0 {
				s += styleMuted.Render("  " + strings.Join(meta, g.sep))
			}
			lines = append(lines, s)
		}
	}
	if len(lines) == 0 {
		return []string{styleMuted.Render("No inference backends or loaded models observed on this node.")}
	}
	return lines
}

func storageTab(node models.NodeFacts, width int, g glyphSet) []string {
	if node.Resources == nil || len(node.Resources.Volumes) == 0 {
		return []string{styleMuted.Render("No storage volumes observed on this node.")}
	}
	mountW := 6
	for _, v := range node.Resources.Volumes {
		mountW = max(mountW, min(lipgloss.Width(v.Mount), 24))
	}
	barW := min(16, max(width/6, 6))
	var lines []string
	for _, v := range node.Resources.Volumes {
		line := styleText.Render(padPlain(v.Mount, mountW, g.ellipsis)) + "  "
		if v.TotalGB > 0 {
			used := float64(v.TotalGB-v.FreeGB) / float64(v.TotalGB)
			line += bar(used, barW, g, lipgloss.NewStyle()) + " " +
				usageLevel(used).style().Render(fmt.Sprintf("%3d%%", int(used*100+0.5))) +
				styleText.Render(fmt.Sprintf("  %d GB free of %d GB", v.FreeGB, v.TotalGB))
		} else {
			line += notObserved()
		}
		class := v.Class
		if class == "" {
			class = "class unknown"
		}
		line += styleMuted.Render(g.sep + class)
		if v.Role == "root" {
			line += styleAccent.Render(" root")
		}
		lines = append(lines, line)
	}
	return lines
}

// reservationsTab shows the reservation overlay the snapshot carries for
// this node. Per-reservation detail lives in the ledger, which this
// read-only view does not open.
func reservationsTab(m Model, node models.NodeFacts) []string {
	var lines []string
	if node.RAMReservedMB > 0 {
		lines = append(lines, kv("Reserved", styleWarn.Render(gb(node.RAMReservedMB))+styleMuted.Render(" RAM held by active reservations")))
	} else {
		lines = append(lines, kv("Reserved", styleMuted.Render("no RAM reservations recorded for this node in the snapshot")))
	}
	alloc := node.RAMAllocatableMB
	if alloc == 0 && node.Resources != nil {
		alloc = node.Resources.RAMAllocatableMB
	}
	if alloc > 0 {
		lines = append(lines, kv("Allocatable", styleText.Render(gb(alloc))))
	}
	if m.snapshot != nil && m.snapshot.Summary.TotalReservedMB > 0 {
		s := m.snapshot.Summary
		cluster := gb(s.TotalReservedMB) + " reserved"
		if s.TotalAllocatableMB > 0 {
			cluster += " of " + gb(s.TotalAllocatableMB) + " allocatable"
		}
		lines = append(lines, kv("Cluster", styleText.Render(cluster)))
	}
	lines = append(lines, "", styleFaint.Render("Per-reservation detail: axis reservations list"))
	return lines
}
