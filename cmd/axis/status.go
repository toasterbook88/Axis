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
	var cacheAddr string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Collect cluster snapshot from all configured nodes",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			snap, source, err := collectStatusSnapshot(
				ctx,
				cached,
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
				if cached {
					payload = statusOutput{Source: source, Snapshot: snap}
				}
				return printOutput(payload, format)
			default:
				printStatusText(cmd, snap, source, cached)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, or yaml")
	cmd.Flags().BoolVar(&cached, "cached", false, "Use the local daemon snapshot cache when available")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS API daemon cache (Unix socket or TCP host:port)")
	return cmd
}

func printStatusText(cmd *cobra.Command, snap *models.ClusterSnapshot, source string, cached bool) {
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
	cachedLoader func(context.Context) (*models.ClusterSnapshot, string, error),
	liveLoader func(context.Context) (*models.ClusterSnapshot, string, error),
) (*models.ClusterSnapshot, string, error) {
	if cached && cachedLoader != nil {
		snap, source, err := cachedLoader(ctx)
		if err == nil {
			return snap, source, nil
		}
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
