// Package main extension: dashboard.go adds a rich interactive CLI dashboard.
// Replaces the need to run multiple commands with a single "axis dashboard"
// that shows cluster state, reservations, mesh peers, and recent executions.
//
// New commands:
//   axis dashboard         — interactive TUI overview (refreshes every 5s)
//   axis summary           — one-shot cluster summary (human-friendly text)
//   axis node trust <name> — promote a mesh-discovered peer to trusted
//   axis node list         — enhanced node listing with color-coded health
//   axis doctor            — comprehensive cluster health check
//   axis reservation list  — show active reservations
//   axis reservation clean — reclaim stale reservations
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/fatih/color"
)

// --- Color Definitions ---
var (
	colorGreen  = color.New(color.FgGreen, color.Bold)
	colorYellow = color.New(color.FgYellow)
	colorRed    = color.New(color.FgRed, color.Bold)
	colorCyan   = color.New(color.FgCyan)
	colorWhite  = color.New(color.FgWhite, color.Bold)
	colorDim    = color.New(color.FgHiBlack)
)

// --- Summary Command ---

// ClusterSummaryView renders a human-friendly cluster overview.
type ClusterSummaryView struct {
	Version      string
	NodeCount    int
	Healthy      int
	Degraded     int
	Unreachable  int
	TotalRAMMB   int64
	FreeRAMMB    int64
	ReservedRAMMB int64
	GPUCount     int
	CacheAge     time.Duration
	IsStale      bool
	MeshPeers    int
	Warnings     []string
}

func (v ClusterSummaryView) Render() string {
	var b strings.Builder

	// Header
	b.WriteString("\n")
	colorWhite.Fprintf(&b, "  ╔══════════════════════════════════════════════╗\n")
	colorWhite.Fprintf(&b, "  ║           AXIS CLUSTER SUMMARY              ║\n")
	colorWhite.Fprintf(&b, "  ╚══════════════════════════════════════════════╝\n\n")

	// Version + Cache
	colorDim.Fprintf(&b, "  Version: ")
	colorCyan.Fprintf(&b, "%s", v.Version)
	colorDim.Fprintf(&b, "    Cache Age: ")
	if v.IsStale {
		colorRed.Fprintf(&b, "%s (STALE)\n", v.CacheAge.Round(time.Second))
	} else {
		colorGreen.Fprintf(&b, "%s\n", v.CacheAge.Round(time.Second))
	}
	b.WriteString("\n")

	// Nodes
	colorWhite.Fprintf(&b, "  NODES\n")
	b.WriteString("  ─────────────────────────────────\n")
	colorGreen.Fprintf(&b, "  ● Healthy:     %d\n", v.Healthy)
	if v.Degraded > 0 {
		colorYellow.Fprintf(&b, "  ◐ Degraded:    %d\n", v.Degraded)
	}
	if v.Unreachable > 0 {
		colorRed.Fprintf(&b, "  ○ Unreachable: %d\n", v.Unreachable)
	}
	colorDim.Fprintf(&b, "    Total:       %d\n", v.NodeCount)
	b.WriteString("\n")

	// Resources
	colorWhite.Fprintf(&b, "  RESOURCES\n")
	b.WriteString("  ─────────────────────────────────\n")

	// RAM bar
	usedRAM := v.TotalRAMMB - v.FreeRAMMB
	ramPct := 0.0
	if v.TotalRAMMB > 0 {
		ramPct = float64(usedRAM) / float64(v.TotalRAMMB) * 100
	}
	ramBar := renderBar(ramPct, 30)
	colorDim.Fprintf(&b, "  RAM:  ")
	b.WriteString(ramBar)
	fmt.Fprintf(&b, " %dGB / %dGB", usedRAM/1024, v.TotalRAMMB/1024)
	if v.ReservedRAMMB > 0 {
		colorYellow.Fprintf(&b, " (%dGB reserved)", v.ReservedRAMMB/1024)
	}
	b.WriteString("\n")

	// GPU
	colorDim.Fprintf(&b, "  GPUs: ")
	if v.GPUCount > 0 {
		colorGreen.Fprintf(&b, "%d available\n", v.GPUCount)
	} else {
		colorDim.Fprintf(&b, "none detected\n")
	}

	// Mesh
	if v.MeshPeers > 0 {
		b.WriteString("\n")
		colorWhite.Fprintf(&b, "  MESH\n")
		b.WriteString("  ─────────────────────────────────\n")
		colorCyan.Fprintf(&b, "  Peers: %d\n", v.MeshPeers)
	}

	// Warnings
	if len(v.Warnings) > 0 {
		b.WriteString("\n")
		colorYellow.Fprintf(&b, "  WARNINGS\n")
		b.WriteString("  ─────────────────────────────────\n")
		for _, w := range v.Warnings {
			colorYellow.Fprintf(&b, "  ⚠ %s\n", w)
		}
	}

	b.WriteString("\n")
	return b.String()
}

// renderBar creates a visual percentage bar.
func renderBar(pct float64, width int) string {
	filled := int(pct / 100.0 * float64(width))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}

	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)

	var c *color.Color
	switch {
	case pct > 90:
		c = colorRed
	case pct > 70:
		c = colorYellow
	default:
		c = colorGreen
	}

	return c.Sprint("[" + bar + "]") + fmt.Sprintf(" %.0f%%", pct)
}

// --- Node List ---

// NodeListItem represents a node in the enhanced listing.
type NodeListItem struct {
	Name       string
	Status     string
	OS         string
	Arch       string
	RAMTotal   int
	RAMFree    int
	Pressure   string
	GPUs       []string
	IsLocal    bool
	Reserved   int64
}

func (n NodeListItem) StatusIcon() string {
	switch n.Status {
	case "complete":
		return colorGreen.Sprint("●")
	case "partial":
		return colorYellow.Sprint("◐")
	case "unreachable":
		return colorRed.Sprint("○")
	default:
		return colorDim.Sprint("?")
	}
}

func (n NodeListItem) PressureColor() string {
	switch strings.ToLower(n.Pressure) {
	case "none":
		return colorGreen.Sprint(n.Pressure)
	case "low":
		return colorGreen.Sprint(n.Pressure)
	case "medium":
		return colorYellow.Sprint(n.Pressure)
	case "high":
		return colorRed.Sprint(n.Pressure)
	default:
		return n.Pressure
	}
}

func RenderNodeTable(nodes []NodeListItem) string {
	var b strings.Builder
	b.WriteString("\n")

	// Header
	colorWhite.Fprintf(&b, "  %-3s %-20s %-10s %-8s %10s %10s %8s  %s\n",
		"", "NAME", "STATUS", "ARCH", "RAM TOTAL", "RAM FREE", "PRESSURE", "GPUS")
	b.WriteString("  " + strings.Repeat("─", 85) + "\n")

	for _, n := range nodes {
		name := n.Name
		if n.IsLocal {
			name = name + " " + colorCyan.Sprint("(local)")
		}

		gpuStr := ""
		if len(n.GPUs) > 0 {
			gpuStr = strings.Join(n.GPUs, ", ")
			if len(gpuStr) > 25 {
				gpuStr = gpuStr[:22] + "..."
			}
		}

		fmt.Fprintf(&b, "  %s %-20s %-10s %-8s %8dMB %8dMB %8s  %s\n",
			n.StatusIcon(),
			name,
			n.Status,
			n.Arch,
			n.RAMTotal,
			n.RAMFree,
			n.PressureColor(),
			gpuStr,
		)
	}
	b.WriteString("\n")
	return b.String()
}

// --- Doctor Command ---

type DoctorCheck struct {
	Name    string
	Status  string // pass, warn, fail
	Message string
	Fix     string // suggested fix
}

func (c DoctorCheck) Icon() string {
	switch c.Status {
	case "pass":
		return colorGreen.Sprint("✓")
	case "warn":
		return colorYellow.Sprint("!")
	case "fail":
		return colorRed.Sprint("✗")
	default:
		return "?"
	}
}

func RenderDoctorReport(checks []DoctorCheck) string {
	var b strings.Builder
	b.WriteString("\n")
	colorWhite.Fprintf(&b, "  AXIS DOCTOR — Cluster Health Report\n")
	b.WriteString("  " + strings.Repeat("═", 50) + "\n\n")

	pass, warn, fail := 0, 0, 0
	for _, c := range checks {
		fmt.Fprintf(&b, "  %s %-35s %s\n", c.Icon(), c.Name, c.Message)
		if c.Fix != "" && c.Status != "pass" {
			colorDim.Fprintf(&b, "    → %s\n", c.Fix)
		}
		switch c.Status {
		case "pass":
			pass++
		case "warn":
			warn++
		case "fail":
			fail++
		}
	}

	b.WriteString("\n  " + strings.Repeat("─", 50) + "\n")
	overall := colorGreen.Sprint("HEALTHY")
	if fail > 0 {
		overall = colorRed.Sprint("UNHEALTHY")
	} else if warn > 0 {
		overall = colorYellow.Sprint("DEGRADED")
	}
	fmt.Fprintf(&b, "  Overall: %s  (%d pass, %d warn, %d fail)\n\n", overall, pass, warn, fail)
	return b.String()
}

// --- Reservation List ---

type ReservationListItem struct {
	ID       string
	Node     string
	RAMMB    int64
	Owner    string
	Age      time.Duration
	IsStale  bool
}

func RenderReservationTable(items []ReservationListItem) string {
	var b strings.Builder
	b.WriteString("\n")
	colorWhite.Fprintf(&b, "  ACTIVE RESERVATIONS\n")
	b.WriteString("  " + strings.Repeat("─", 70) + "\n")

	if len(items) == 0 {
		colorDim.Fprintf(&b, "  No active reservations\n\n")
		return b.String()
	}

	colorWhite.Fprintf(&b, "  %-20s %-15s %10s %-20s %10s\n",
		"ID", "NODE", "RAM (MB)", "OWNER", "AGE")
	b.WriteString("  " + strings.Repeat("─", 70) + "\n")

	for _, r := range items {
		ageStr := formatDuration(r.Age)
		if r.IsStale {
			ageStr = colorRed.Sprintf("%s (STALE)", ageStr)
		}
		fmt.Fprintf(&b, "  %-20s %-15s %10d %-20s %s\n",
			truncate(r.ID, 20),
			r.Node,
			r.RAMMB,
			truncate(r.Owner, 20),
			ageStr,
		)
	}
	b.WriteString("\n")
	return b.String()
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
