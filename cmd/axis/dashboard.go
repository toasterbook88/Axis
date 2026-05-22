// Package main extension: dashboard.go contains rendering helpers for proposed
// dashboard-style CLI views.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/ui"
)

// --- Summary Command ---

func summaryCmd() *cobra.Command {
	var cached bool
	var cacheAddr string

	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Display a visual dashboard of cluster health and resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			snap, source, err := collectStatusSnapshot(
				ctx,
				cached,
				false,
				func(ctx context.Context) (*models.ClusterSnapshot, string, error) {
					return daemon.FetchSnapshot(ctx, cacheAddr)
				},
				discoverLiveSnapshot,
			)
			if err != nil {
				ui.FprintError(os.Stderr, fmt.Sprintf("%v", err), "")
				return err
			}

			meta := daemon.Metadata{}
			if source != "live" {
				m, _ := daemon.FetchMeta(ctx, cacheAddr)
				meta = m
			}

			view := populateSummaryView(snap, meta)
			fmt.Fprint(cmd.OutOrStdout(), view.Render())
			return nil
		},
	}
	cmd.Flags().BoolVar(&cached, "cached", true, "Use the local daemon snapshot cache by default")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS API daemon cache")
	return cmd
}

func populateSummaryView(snap *models.ClusterSnapshot, meta daemon.Metadata) ClusterSummaryView {
	view := ClusterSummaryView{
		Version:          meta.Version,
		NodeCount:        len(snap.Nodes),
		TotalRAMMB:       snap.Summary.TotalRAMMB,
		FreeRAMMB:        snap.Summary.TotalFreeRAMMB,
		ReservedRAMMB:    snap.Summary.TotalReservedMB,
		AllocatableRAMMB: snap.Summary.TotalAllocatableMB,
		CacheAge:         time.Duration(meta.CacheAgeSec) * time.Second,
		IsStale:          meta.Stale,
		MeshPeers:        meta.MeshPeers,
	}
	if view.Version == "" {
		view.Version = Version
	}
	for _, n := range snap.Nodes {
		switch n.Status {
		case models.StatusComplete:
			view.Healthy++
		case models.StatusPartial:
			view.Degraded++
		case models.StatusUnreachable, models.StatusError:
			view.Unreachable++
		}
		if n.Resources != nil {
			view.GPUCount += len(n.Resources.GPUs)
		}
	}
	for _, w := range snap.Warnings {
		view.Warnings = append(view.Warnings, fmt.Sprintf("%s: %s", w.Node, w.Message))
	}
	return view
}

// ClusterSummaryView renders a human-friendly cluster overview.
type ClusterSummaryView struct {
	Version          string
	NodeCount        int
	Healthy          int
	Degraded         int
	Unreachable      int
	TotalRAMMB       int64
	FreeRAMMB        int64
	ReservedRAMMB    int64
	AllocatableRAMMB int64
	GPUCount         int
	CacheAge         time.Duration
	IsStale          bool
	MeshPeers        int
	Warnings         []string
}

func (v ClusterSummaryView) Render() string {
	var b strings.Builder

	// Header
	b.WriteString("\n")
	ui.WhiteColor.Fprintf(&b, "  ╔══════════════════════════════════════════════╗\n")
	ui.WhiteColor.Fprintf(&b, "  ║           AXIS CLUSTER SUMMARY              ║\n")
	ui.WhiteColor.Fprintf(&b, "  ╚══════════════════════════════════════════════╝\n\n")

	// Version + Cache
	ui.DimColor.Fprintf(&b, "  Version: ")
	ui.CyanColor.Fprintf(&b, "%s", v.Version)
	if v.CacheAge > 0 || v.IsStale {
		ui.DimColor.Fprintf(&b, "    Cache Age: ")
		if v.IsStale {
			ui.RedColor.Fprintf(&b, "%s (STALE)\n", v.CacheAge.Round(time.Second))
		} else {
			ui.GreenColor.Fprintf(&b, "%s\n", v.CacheAge.Round(time.Second))
		}
	} else {
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Nodes
	ui.WhiteColor.Fprintf(&b, "  NODES\n")
	b.WriteString("  ─────────────────────────────────\n")
	ui.GreenColor.Fprintf(&b, "  ● Healthy:     %d\n", v.Healthy)
	if v.Degraded > 0 {
		ui.YellowColor.Fprintf(&b, "  ◐ Degraded:    %d\n", v.Degraded)
	}
	if v.Unreachable > 0 {
		ui.RedColor.Fprintf(&b, "  ○ Unreachable: %d\n", v.Unreachable)
	}
	ui.DimColor.Fprintf(&b, "    Total:       %d\n", v.NodeCount)
	b.WriteString("\n")

	// Resources
	ui.WhiteColor.Fprintf(&b, "  RESOURCES\n")
	b.WriteString("  ─────────────────────────────────\n")

	// RAM bar
	usedRAM := v.TotalRAMMB - v.FreeRAMMB
	ramPct := 0.0
	if v.TotalRAMMB > 0 {
		ramPct = float64(usedRAM) / float64(v.TotalRAMMB) * 100
	}
	ramBar := renderBar(ramPct, 30)
	ui.DimColor.Fprintf(&b, "  RAM:  ")
	b.WriteString(ramBar)
	fmt.Fprintf(&b, " %dGB / %dGB", usedRAM/1024, v.TotalRAMMB/1024)
	if v.ReservedRAMMB > 0 {
		ui.YellowColor.Fprintf(&b, " (%dGB reserved)", v.ReservedRAMMB/1024)
	}
	if v.AllocatableRAMMB > 0 {
		ui.GreenColor.Fprintf(&b, " (%dGB allocatable)", v.AllocatableRAMMB/1024)
	}
	b.WriteString("\n")

	// GPU
	ui.DimColor.Fprintf(&b, "  GPUs: ")
	if v.GPUCount > 0 {
		ui.GreenColor.Fprintf(&b, "%d available\n", v.GPUCount)
	} else {
		ui.DimColor.Fprintf(&b, "none detected\n")
	}

	// Mesh
	if v.MeshPeers > 0 {
		b.WriteString("\n")
		ui.WhiteColor.Fprintf(&b, "  MESH\n")
		b.WriteString("  ─────────────────────────────────\n")
		ui.CyanColor.Fprintf(&b, "  Peers: %d\n", v.MeshPeers)
	}

	// Warnings
	if len(v.Warnings) > 0 {
		b.WriteString("\n")
		ui.YellowColor.Fprintf(&b, "  WARNINGS\n")
		b.WriteString("  ─────────────────────────────────\n")
		for _, w := range v.Warnings {
			ui.YellowColor.Fprintf(&b, "  ⚠ %s\n", w)
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

	var c = ui.GreenColor
	switch {
	case pct > 90:
		c = ui.RedColor
	case pct > 70:
		c = ui.YellowColor
	}

	return c.Sprint("["+bar+"]") + fmt.Sprintf(" %.0f%%", pct)
}

// --- Node List ---

// NodeListItem represents a node in the enhanced listing.
type NodeListItem struct {
	Name     string
	Status   string
	OS       string
	Arch     string
	RAMTotal int
	RAMFree  int
	Pressure string
	GPUs     []string
	IsLocal  bool
	Reserved int64
}

func (n NodeListItem) StatusIcon() string {
	switch n.Status {
	case "complete":
		return ui.GreenColor.Sprint("●")
	case "partial":
		return ui.YellowColor.Sprint("◐")
	case "unreachable":
		return ui.RedColor.Sprint("○")
	default:
		return ui.DimColor.Sprint("?")
	}
}

func (n NodeListItem) PressureColor() string {
	switch strings.ToLower(n.Pressure) {
	case "none":
		return ui.GreenColor.Sprint(n.Pressure)
	case "low":
		return ui.GreenColor.Sprint(n.Pressure)
	case "medium":
		return ui.YellowColor.Sprint(n.Pressure)
	case "high":
		return ui.RedColor.Sprint(n.Pressure)
	default:
		return n.Pressure
	}
}

func RenderNodeTable(nodes []NodeListItem) string {
	var b strings.Builder
	sep := strings.Repeat("─", 85)
	b.WriteString("\n")

	// Header
	ui.WhiteColor.Fprintf(&b, "  %-3s %-20s %-10s %-8s %10s %10s %8s  %s\n",
		"", "NAME", "STATUS", "ARCH", "RAM TOTAL", "RAM FREE", "PRESSURE", "GPUS")
	b.WriteString("  ")
	b.WriteString(sep)
	b.WriteString("\n")

	for _, n := range nodes {
		name := n.Name
		if n.IsLocal {
			name = name + " " + ui.CyanColor.Sprint("(local)")
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
		return ui.GreenColor.Sprint("✓")
	case "warn":
		return ui.YellowColor.Sprint("!")
	case "fail":
		return ui.RedColor.Sprint("✗")
	default:
		return "?"
	}
}

func RenderDoctorReport(checks []DoctorCheck) string {
	var b strings.Builder
	sep := strings.Repeat("─", 50)
	b.WriteString("\n")
	ui.WhiteColor.Fprintf(&b, "  AXIS DOCTOR — Cluster Health Report\n")
	b.WriteString("  ")
	b.WriteString(strings.Repeat("═", 50))
	b.WriteString("\n\n")

	pass, warn, fail := 0, 0, 0
	for _, c := range checks {
		fmt.Fprintf(&b, "  %s %-35s %s\n", c.Icon(), c.Name, c.Message)
		if c.Fix != "" && c.Status != "pass" {
			ui.DimColor.Fprintf(&b, "    → %s\n", c.Fix)
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

	b.WriteString("\n  ")
	b.WriteString(sep)
	b.WriteString("\n")
	overall := ui.GreenColor.Sprint("HEALTHY")
	if fail > 0 {
		overall = ui.RedColor.Sprint("UNHEALTHY")
	} else if warn > 0 {
		overall = ui.YellowColor.Sprint("DEGRADED")
	}
	fmt.Fprintf(&b, "  Overall: %s  (%d pass, %d warn, %d fail)\n\n", overall, pass, warn, fail)
	return b.String()
}
