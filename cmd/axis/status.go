package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
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
				return printOutput(cmd.OutOrStdout(), payload, format)
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

	tbl := ui.NewTable("NAME", "STATUS", "RAM", "STORAGE", "PRESSURE", "GPU", "TOOLS")
	for _, n := range snap.Nodes {
		status := formatNodeStatus(n.Status)
		ram := "—"
		storage := "—"
		pressure := "—"
		gpu := "—"
		tools := "—"

		if n.Resources != nil {
			if n.RAMAllocatableMB > 0 {
				ram = fmt.Sprintf("%d MB", n.RAMAllocatableMB)
				if n.RAMReservedMB > 0 {
					ram = fmt.Sprintf("%d MB (%d reserved)", n.RAMAllocatableMB, n.RAMReservedMB)
				}
			} else {
				ram = fmt.Sprintf("%d MB", n.Resources.RAMFreeMB)
			}
			storage = n.Resources.StorageClass
			if storage == "" {
				storage = "—"
			}
			pressure = formatPressure(n.Resources.Pressure)
			if len(n.Resources.GPUs) > 0 {
				gpu = formatGPUStatus(n.Resources.GPUs)
			}
		}
		if len(n.Tools) > 0 {
			names := make([]string, 0, len(n.Tools))
			for _, t := range n.Tools {
				names = append(names, t.Name)
			}
			tools = strings.Join(names, ", ")
		}

		tbl.AddRow(ui.Cyan(n.Name), status, ram, storage, pressure, gpu, tools)
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

// canonicalRuntimeOrder defines the display order for known resident model
// runtimes. Unknown runtimes are appended in sorted order after these.
var canonicalRuntimeOrder = []string{"ollama", "llama.cpp", "mlx", "apple-foundation-models"}

// printResidentModelsSection renders a RESIDENT MODELS table when at least one
// node has live resident models. Rows are ordered by node then by runtime
// (ollama → llama.cpp → mlx → apple-fm → others alphabetically). Model names
// are truncated at 3 per row with a "+N more" suffix to keep lines readable on
// narrow terminals. A VRAM column is always shown for resident models; rows
// without a truth-backed SizeVRAMMB value render "—" instead of inventing a
// number. Today that value is only populated by the Ollama probe.
func printResidentModelsSection(out io.Writer, nodes []models.NodeFacts) {
	type runtimeRow struct {
		node    string
		runtime string
		models  []models.ResidentModel
	}

	// Collect rows in display order: iterate nodes, then group by runtime.
	var rows []runtimeRow
	for _, n := range nodes {
		if len(n.ResidentModels) == 0 {
			continue
		}
		// Group resident models by runtime for this node.
		byRuntime := make(map[string][]models.ResidentModel, 4)
		for _, rm := range n.ResidentModels {
			rt := strings.ToLower(strings.TrimSpace(rm.Runtime))
			if rt == "" {
				rt = "unknown"
			}
			byRuntime[rt] = append(byRuntime[rt], rm)
		}
		// Emit canonical runtimes first, then any extras in sorted order to
		// guarantee deterministic output (map iteration order is undefined).
		seen := make(map[string]bool, len(canonicalRuntimeOrder))
		for _, rt := range canonicalRuntimeOrder {
			if rms, ok := byRuntime[rt]; ok {
				rows = append(rows, runtimeRow{n.Name, rt, rms})
				seen[rt] = true
			}
		}
		extras := make([]string, 0, len(byRuntime))
		for rt := range byRuntime {
			if !seen[rt] {
				extras = append(extras, rt)
			}
		}
		sort.Strings(extras)
		for _, rt := range extras {
			rows = append(rows, runtimeRow{n.Name, rt, byRuntime[rt]})
		}
	}

	if len(rows) == 0 {
		return
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "%s\n", ui.Bold("RESIDENT MODELS"))
	tbl := ui.NewTable("NODE", "RUNTIME", "MODELS", "VRAM")
	for _, row := range rows {
		names := make([]string, len(row.models))
		for i, rm := range row.models {
			names[i] = rm.Name
		}
		tbl.AddRow(
			ui.Cyan(row.node),
			formatResidentRuntime(row.runtime),
			truncateModelList(names, 3),
			formatResidentVRAM(residentRowVRAMTotal(row.models)),
		)
	}
	tbl.Render(out)
}

// residentRowVRAMTotal returns the sum of SizeVRAMMB across all resident models
// in a runtime row. Returns 0 when no VRAM data is available.
func residentRowVRAMTotal(rms []models.ResidentModel) int64 {
	var total int64
	for _, rm := range rms {
		total += rm.SizeVRAMMB
	}
	return total
}

// formatResidentVRAM formats a VRAM total for display in the status table.
// Returns "—" when total is 0 (unknown or not applicable to this runtime).
func formatResidentVRAM(mb int64) string {
	if mb <= 0 {
		return "—"
	}
	if mb < 1024 {
		return fmt.Sprintf("%d MB", mb)
	}
	return fmt.Sprintf("%.1f GB", float64(mb)/1024)
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

func formatGPUBaseName(g models.GPUInfo) string {
	s := g.Model
	if g.Vendor != "" && g.Vendor != "unknown" && !strings.Contains(strings.ToLower(s), strings.ToLower(g.Vendor)) {
		s = fmt.Sprintf("%s %s", g.Vendor, s)
	}
	return s
}

func formatGPUStatus(gpus []models.GPUInfo) string {
	if len(gpus) == 0 {
		return "—"
	}
	if len(gpus) == 1 {
		g := gpus[0]
		s := formatGPUBaseName(g)
		if g.VRAMMB > 0 {
			s = fmt.Sprintf("%s (%d MB)", s, g.VRAMMB)
		}
		return s
	}
	first := formatGPUBaseName(gpus[0])
	return fmt.Sprintf("%s +%d more", first, len(gpus)-1)
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
