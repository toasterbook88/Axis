// Package main extension: dashboard.go contains rendering helpers for proposed
// dashboard-style CLI views.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/ui"
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
			fmt.Print(view.Render())
			return nil
		},
	}
	cmd.Flags().BoolVar(&cached, "cached", true, "Use the local daemon snapshot cache by default")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS API daemon cache")
	return cmd
}

func populateSummaryView(snap *models.ClusterSnapshot, meta daemon.Metadata) ClusterSummaryView {
	view := ClusterSummaryView{
		Version:       meta.Version,
		NodeCount:     len(snap.Nodes),
		TotalRAMMB:    snap.Summary.TotalRAMMB,
		FreeRAMMB:     snap.Summary.TotalFreeRAMMB,
		ReservedRAMMB: snap.Summary.TotalReservedMB,
		CacheAge:      time.Duration(meta.CacheAgeSec) * time.Second,
		IsStale:       meta.Stale,
		MeshPeers:     meta.MeshPeers,
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
	Version       string
	NodeCount     int
	Healthy       int
	Degraded      int
	Unreachable   int
	TotalRAMMB    int64
	FreeRAMMB     int64
	ReservedRAMMB int64
	GPUCount      int
	CacheAge      time.Duration
	IsStale       bool
	MeshPeers     int
	Warnings      []string
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
	if v.CacheAge > 0 || v.IsStale {
		colorDim.Fprintf(&b, "    Cache Age: ")
		if v.IsStale {
			colorRed.Fprintf(&b, "%s (STALE)\n", v.CacheAge.Round(time.Second))
		} else {
			colorGreen.Fprintf(&b, "%s\n", v.CacheAge.Round(time.Second))
		}
	} else {
		b.WriteString("\n")
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

func reservationsCmd() *cobra.Command {
	var cacheAddr string

	cmd := &cobra.Command{
		Use:   "reservations",
		Short: "Show active resource reservations in the cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			// Fetch from /v2/reservations
			client, baseURLAddr := daemon.HttpClientForAddr(cacheAddr)
			baseURL := daemon.NormalizeAddr(baseURLAddr)

			token, err := auth.LoadOrGenerateToken()
			if err != nil {
				return err
			}

			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v2/reservations", nil)
			req.Header.Set("Authorization", "Bearer "+token)

			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("daemon not reachable: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != 200 {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("api error (%d): %s", resp.StatusCode, string(body))
			}

			var result struct {
				Entries []struct {
					ID        string    `json:"id"`
					Node      string    `json:"node"`
					RAMMB     int64     `json:"ram_mb"`
					Owner     string    `json:"owner_surface"`
					CreatedAt time.Time `json:"created_at"`
				} `json:"reservations"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return err
			}

			items := make([]ReservationListItem, 0, len(result.Entries))
			for _, e := range result.Entries {
				items = append(items, ReservationListItem{
					ID:      e.ID,
					Node:    e.Node,
					RAMMB:   e.RAMMB,
					Owner:   e.Owner,
					Age:     time.Since(e.CreatedAt),
					IsStale: time.Since(e.CreatedAt) > 5*time.Minute,
				})
			}

			fmt.Print(RenderReservationTable(items))
			return nil
		},
	}
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS API daemon cache")
	return cmd
}

type ReservationListItem struct {
	ID      string
	Node    string
	RAMMB   int64
	Owner   string
	Age     time.Duration
	IsStale bool
}

func RenderReservationTable(items []ReservationListItem) string {
	var b strings.Builder
	b.WriteString("\n")
	colorWhite.Fprintf(&b, "  ACTIVE RESERVATIONS\n")
	b.WriteString("  " + strings.Repeat("─", 75) + "\n")

	if len(items) == 0 {
		colorDim.Fprintf(&b, "  No active reservations\n\n")
		return b.String()
	}

	colorWhite.Fprintf(&b, "  %-20s %-15s %10s %-15s %10s\n",
		"ID", "NODE", "RAM (MB)", "OWNER", "AGE")
	b.WriteString("  " + strings.Repeat("─", 75) + "\n")

	displayItems := items
	truncated := 0
	if len(items) > 50 {
		displayItems = items[:50]
		truncated = len(items) - 50
	}

	for _, r := range displayItems {
		ageStr := formatDuration(r.Age)
		if r.IsStale {
			ageStr = colorRed.Sprintf("%s (STALE)", ageStr)
		}
		fmt.Fprintf(&b, "  %-20s %-15s %10d %-15s %s\n",
			truncateID(r.ID, 20),
			r.Node,
			r.RAMMB,
			truncateID(r.Owner, 15),
			ageStr,
		)
	}

	if truncated > 0 {
		colorDim.Fprintf(&b, "\n  ... and %d more reservations.\n", truncated)
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

func truncateID(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
