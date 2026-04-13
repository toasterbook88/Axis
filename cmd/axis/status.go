package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/ui"
)

var fetchStatusSnapshot = daemon.FetchSnapshot
var loadStatusLiveSnapshot = discoverLiveSnapshot
var loadStatusRuntime = runtimectx.Load

type statusOutput struct {
	Source   string                  `json:"source" yaml:"source"`
	Snapshot *models.ClusterSnapshot `json:"snapshot" yaml:"snapshot"`
}

func statusCmd() *cobra.Command {
	var format string
	var cached bool
	var cachedOnly bool
	var cacheAddr string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Collect cluster snapshot from all configured nodes",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cacheRequested := cached || cachedOnly

			snap, source, err := collectStatusSnapshot(
				ctx,
				cacheRequested,
				cachedOnly,
				func(ctx context.Context) (*models.ClusterSnapshot, string, error) {
					return fetchStatusSnapshot(ctx, cacheAddr)
				},
				loadStatusLiveSnapshot,
			)
			if err != nil {
				ui.FprintError(os.Stderr, fmt.Sprintf("%v", err), "")
				return err
			}

			switch format {
			case "json", "yaml":
				var payload any = snap
				if cacheRequested {
					payload = statusOutput{Source: source, Snapshot: snap}
				}
				return printOutput(payload, format)
			default:
				printStatusText(cmd, snap, source)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, or yaml")
	cmd.Flags().BoolVar(&cached, "cached", false, "Use the local daemon snapshot cache when available")
	cmd.Flags().BoolVar(&cachedOnly, "cached-only", false, "Require daemon cache; fail instead of falling back to live discovery")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS API daemon cache (Unix socket or TCP host:port)")
	return cmd
}

func printStatusText(cmd *cobra.Command, snap *models.ClusterSnapshot, source string) {
	out := cmd.OutOrStdout()

	healthy := 0
	for _, n := range snap.Nodes {
		if n.Status == models.StatusComplete {
			healthy++
		}
	}

	fmt.Fprintf(out, "%s (%d nodes, %d healthy)\n\n",
		ui.Bold("CLUSTER STATUS"), len(snap.Nodes), healthy)

	tbl := ui.NewTable("NAME", "STATUS", "RAM FREE", "PRESSURE", "TOOLS")
	for _, n := range snap.Nodes {
		status := formatNodeStatus(n.Status)
		ram := "—"
		pressure := "—"
		tools := "—"

		if n.Resources != nil {
			ram = fmt.Sprintf("%d MB", n.Resources.RAMFreeMB)
			pressure = formatPressure(n.Resources.Pressure)
		}
		if len(n.Tools) > 0 {
			names := make([]string, 0, len(n.Tools))
			for _, t := range n.Tools {
				names = append(names, t.Name)
			}
			tools = strings.Join(names, ", ")
		}

		tbl.AddRow(ui.Cyan(n.Name), status, ram, pressure, tools)
	}
	tbl.Render(out)

	printResidentModelsSection(out, snap.Nodes)

	if len(snap.Warnings) > 0 {
		fmt.Fprintln(out)
		for _, w := range snap.Warnings {
			ui.FprintWarning(out, fmt.Sprintf("%s: %s", w.Node, w.Message))
		}
	}

	fmt.Fprintln(out)
	sourceLabel := source
	if sourceLabel == "" {
		sourceLabel = "live"
	}
	fmt.Fprintf(out, "%s %s | %s\n",
		ui.Dim("Snapshot:"), sourceLabel,
		ui.Dim(snap.Timestamp.Format(time.RFC3339)))
}

// printResidentModelsSection renders a RESIDENT MODELS table when at least one
// node has live resident models. Rows are ordered by node then by runtime
// (ollama → llama.cpp → mlx → others). Model names are truncated at 3 per row
// with a "+N more" suffix to keep lines readable on narrow terminals.
func printResidentModelsSection(out io.Writer, nodes []models.NodeFacts) {
	type runtimeRow struct {
		node    string
		runtime string
		models  []string
	}

	// Collect rows in display order: iterate nodes, then group by runtime.
	var rows []runtimeRow
	for _, n := range nodes {
		if len(n.ResidentModels) == 0 {
			continue
		}
		// Group model names by runtime for this node.
		order := []string{"ollama", "llama.cpp", "mlx", "apple-foundation-models"}
		byRuntime := make(map[string][]string, 4)
		for _, rm := range n.ResidentModels {
			rt := strings.ToLower(strings.TrimSpace(rm.Runtime))
			if rt == "" {
				rt = "unknown"
			}
			byRuntime[rt] = append(byRuntime[rt], rm.Name)
		}
		// Emit in canonical order, then any extras alphabetically.
		seen := make(map[string]bool)
		for _, rt := range order {
			if names, ok := byRuntime[rt]; ok {
				rows = append(rows, runtimeRow{n.Name, rt, names})
				seen[rt] = true
			}
		}
		for rt, names := range byRuntime {
			if !seen[rt] {
				rows = append(rows, runtimeRow{n.Name, rt, names})
			}
		}
	}

	if len(rows) == 0 {
		return
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "%s\n", ui.Bold("RESIDENT MODELS"))
	tbl := ui.NewTable("NODE", "RUNTIME", "MODELS")
	for _, row := range rows {
		tbl.AddRow(
			ui.Cyan(row.node),
			formatResidentRuntime(row.runtime),
			truncateModelList(row.models, 3),
		)
	}
	tbl.Render(out)
}

// formatResidentRuntime returns a human-readable, optionally coloured label for
// a resident model runtime string.
func formatResidentRuntime(rt string) string {
	switch rt {
	case "ollama":
		return ui.Green("ollama")
	case "llama.cpp":
		return ui.Yellow("llama.cpp")
	case "mlx":
		return ui.Cyan("mlx")
	case "apple-foundation-models":
		return ui.Green("apple-fm")
	default:
		return ui.Dim(rt)
	}
}

// truncateModelList joins model names with ", " and appends "+N more" when the
// list exceeds max visible entries.
func truncateModelList(names []string, max int) string {
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	visible := strings.Join(names[:max], ", ")
	return fmt.Sprintf("%s, +%d more", visible, len(names)-max)
}

func formatNodeStatus(s models.NodeStatus) string {
	switch s {
	case models.StatusComplete:
		return ui.Green("✓ complete")
	case models.StatusPartial:
		return ui.Yellow("~ partial")
	case models.StatusUnreachable:
		return ui.Red("✗ unreachable")
	case models.StatusError:
		return ui.Red("✗ error")
	default:
		return string(s)
	}
}

func formatPressure(p string) string {
	switch p {
	case "none", "":
		return ui.Green("none")
	case "low":
		return ui.Green("low")
	case "medium":
		return ui.Yellow("medium")
	case "high":
		return ui.Red("high")
	default:
		return p
	}
}

func collectStatusSnapshot(
	ctx context.Context,
	cached bool,
	cachedOnly bool,
	cachedLoader func(context.Context) (*models.ClusterSnapshot, string, error),
	liveLoader func(context.Context) (*models.ClusterSnapshot, string, error),
) (*models.ClusterSnapshot, string, error) {
	if cachedOnly {
		cached = true
	}

	if cached && cachedLoader != nil {
		snap, source, err := cachedLoader(ctx)
		if err == nil {
			return snap, source, nil
		}
		if cachedOnly {
			return nil, "", fmt.Errorf("daemon cache unavailable: %w", err)
		}

		liveSnap, liveSource, liveErr := liveLoader(ctx)
		if liveErr != nil {
			return nil, "", liveErr
		}
		if liveSnap != nil {
			appendWarningIfMissing(liveSnap, models.Warning{
				Kind:    "cache",
				Message: fmt.Sprintf("daemon cache unavailable; fell back to live snapshot: %v", err),
			})
		}
		return liveSnap, fallbackSource(liveSource), nil
	}

	return liveLoader(ctx)
}

func discoverLiveSnapshot(ctx context.Context) (*models.ClusterSnapshot, string, error) {
	rt, err := loadStatusRuntime(ctx)
	if err != nil {
		return nil, "", err
	}
	return rt.Snapshot, "live", nil
}

func fallbackSource(source string) string {
	switch normalized := sourceOrLive(source); normalized {
	case "live":
		return "live-fallback"
	default:
		return normalized + "-fallback"
	}
}
