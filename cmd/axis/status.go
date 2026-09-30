package main

import (
	"context"
	"fmt"
	"io"
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
	Age      string                  `json:"age,omitempty" yaml:"age,omitempty"`
	Snapshot *models.ClusterSnapshot `json:"snapshot" yaml:"snapshot"`
}

func statusCmd() *cobra.Command {
	var format string
	var cached bool
	var cachedOnly bool
	var live bool
	var cacheAddr string
	var watch bool
	var watchInterval time.Duration
	validateFormat := validateOutputFormat(&format, "text", "json", "yaml")
	validateWatchInterval := validatePositiveDuration("watch-interval", &watchInterval)

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Collect cluster snapshot from all configured nodes",
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if err := validateFormat(cmd, args); err != nil {
				return err
			}
			if err := validateWatchInterval(cmd, args); err != nil {
				return err
			}
			if err := rejectLiveAndCachedOnly(live, cachedOnly); err != nil {
				return err
			}
			if watch && format != "text" {
				return fmt.Errorf("--watch only supports --format text")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// --cached is accepted and matches the default cache-first read.
			// It does not widen the 5-minute window or override --live.
			_ = cached
			load := func(ctx context.Context) (commandSnapshot, error) {
				return loadCommandSnapshot(
					ctx,
					live,
					cachedOnly,
					func(ctx context.Context) (*models.ClusterSnapshot, string, error) {
						return fetchStatusSnapshot(ctx, cacheAddr)
					},
					loadStatusLiveSnapshot,
				)
			}

			if watch {
				ticker := time.NewTicker(watchInterval)
				defer ticker.Stop()
				for {
					select {
					case <-cmd.Context().Done():
						return nil
					default:
					}

					fetchCtx, fetchCancel := context.WithTimeout(cmd.Context(), 10*time.Second)
					read, err := load(fetchCtx)
					fetchCancel()
					snap, source, age := read.snap, read.source, read.age

					// Clear terminal screen and move cursor to home
					if _, writeErr := fmt.Fprint(cmd.OutOrStdout(), "\033[H\033[2J"); writeErr != nil {
						return writeErr
					}

					if err != nil {
						ui.FprintError(cmd.ErrOrStderr(), fmt.Sprintf("%v", err), "")
						if cachedOnly {
							// --cached-only fails closed: report once and
							// stop the watch loop instead of rendering a
							// missing or stale publication forever.
							return err
						}
					} else if writeErr := printStatusText(cmd, snap, source, age); writeErr != nil {
						return writeErr
					}

					select {
					case <-cmd.Context().Done():
						return nil
					case <-ticker.C:
					}
				}
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()

			read, err := load(ctx)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				ui.FprintError(cmd.ErrOrStderr(), fmt.Sprintf("%v", err), "")
				return err
			}

			switch format {
			case "json", "yaml":
				return printOutput(cmd.OutOrStdout(), statusOutput{
					Source:   read.source,
					Age:      read.age,
					Snapshot: read.snap,
				}, format)
			default:
				return printStatusText(cmd, read.snap, read.source, read.age)
			}
		},
	}

	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, or yaml")
	cmd.Flags().BoolVar(&cached, "cached", false, "Read the daemon publication when it is inside the 5-minute stale threshold (this is the default)")
	cmd.Flags().BoolVar(&cachedOnly, "cached-only", false, "Require a fresh daemon publication; fail instead of falling back to a live sweep")
	cmd.Flags().BoolVar(&live, "live", false, "Perform a live cluster discovery sweep instead of reading the daemon publication")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS API daemon cache (Unix socket or TCP host:port)")
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "Watch status in real-time")
	cmd.Flags().DurationVarP(&watchInterval, "watch-interval", "i", 3*time.Second, "Watch refresh interval")
	return cmd
}

func printStatusText(cmd *cobra.Command, snap *models.ClusterSnapshot, source, age string) error {
	var rendered strings.Builder
	out := &rendered

	healthy := 0
	for _, n := range snap.Nodes {
		if n.Status == models.StatusComplete {
			healthy++
		}
	}

	fmt.Fprintf(out, "%s (%d nodes, %d healthy)\n\n",
		ui.Bold("CLUSTER STATUS"), len(snap.Nodes), healthy)

	var listItems []NodeListItem
	for _, n := range snap.Nodes {
		var ramTotal, ramFree int
		var pressure string
		var gpus []string
		var osName, arch string

		if n.Resources != nil {
			ramTotal = int(n.Resources.RAMTotalMB)
			ramFree = int(n.Resources.RAMFreeMB)
			pressure = string(n.Resources.Pressure)
			for _, g := range n.Resources.GPUs {
				gpus = append(gpus, g.Model)
			}
		}
		osName = n.OS
		arch = n.Arch

		listItems = append(listItems, NodeListItem{
			Name:     n.Name,
			Status:   string(n.Status),
			OS:       osName,
			Arch:     arch,
			RAMTotal: ramTotal,
			RAMFree:  ramFree,
			Pressure: pressure,
			GPUs:     gpus,
			IsLocal:  models.IsLocalNode(n),
			Reserved: n.RAMReservedMB,
		})
	}

	fmt.Fprint(out, RenderNodeTable(listItems))

	printResidentModelsSection(out, snap.Nodes)

	if len(snap.Warnings) > 0 {
		fmt.Fprintln(out)
		for _, w := range snap.Warnings {
			ui.FprintWarning(out, fmt.Sprintf("%s: %s", w.Node, w.Message))
		}
	}

	fmt.Fprintln(out)
	fmt.Fprintf(out, "%s %s | %s\n",
		ui.Dim("Snapshot:"), formatReadOrigin(source, age),
		ui.Dim(snap.Timestamp.Format(time.RFC3339)))
	_, err := fmt.Fprint(cmd.OutOrStdout(), rendered.String())
	return err
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
				Message: "using live snapshot (daemon cache unavailable)",
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

// commandSnapshot is one read of the cluster publication, with the age that
// the command must print. Age is "none" when no publication clock is available
// (including a live sweep taken because the daemon cache was missing). On a
// stale-cache fallback the age is the rejected publication's age, matching the
// stale warning.
type commandSnapshot struct {
	snap   *models.ClusterSnapshot
	source string
	age    string
}

func rejectLiveAndCachedOnly(live, cachedOnly bool) error {
	if live && cachedOnly {
		return fmt.Errorf("--live and --cached-only cannot be combined")
	}
	return nil
}

// loadCommandSnapshot is the cache-first read used by axis status, axis task
// place, and axis placement explain. It uses the daemon publication already
// fetched by FetchSnapshot. A publication is fresh only inside
// daemon.DefaultStaleThreshold (the same 5-minute gate as Daemon.Meta). A
// missing or older publication is not returned as a fresh hit.
func loadCommandSnapshot(
	ctx context.Context,
	live bool,
	cachedOnly bool,
	cachedLoader func(context.Context) (*models.ClusterSnapshot, string, error),
	liveLoader func(context.Context) (*models.ClusterSnapshot, string, error),
) (commandSnapshot, error) {
	if live {
		snap, source, err := liveLoader(ctx)
		if err != nil {
			return commandSnapshot{}, err
		}
		return commandSnapshot{snap: snap, source: sourceOrLive(source), age: publicationAgeLabel(snap)}, nil
	}

	var (
		cacheErr error
		stale    bool
		staleAge string
	)
	if cachedLoader == nil {
		cacheErr = fmt.Errorf("no cache loader")
	} else {
		snap, source, err := cachedLoader(ctx)
		if err != nil {
			cacheErr = err
		} else if fresh, label := publicationIsFresh(snap); fresh {
			return commandSnapshot{snap: snap, source: sourceOrLive(source), age: label}, nil
		} else {
			stale = true
			staleAge = label
		}
	}

	if cachedOnly {
		if stale {
			return commandSnapshot{}, fmt.Errorf("daemon cache stale: publication age %s exceeds %s", staleAge, daemon.DefaultStaleThreshold)
		}
		if cacheErr == nil {
			cacheErr = fmt.Errorf("empty cache")
		}
		return commandSnapshot{}, fmt.Errorf("daemon cache unavailable: %w", cacheErr)
	}

	liveSnap, liveSource, liveErr := liveLoader(ctx)
	if liveErr != nil {
		return commandSnapshot{}, liveErr
	}
	if stale {
		message := "using live snapshot (daemon cache stale)"
		if staleAge != "" && staleAge != "none" {
			message = fmt.Sprintf("using live snapshot (daemon cache stale, age %s)", staleAge)
		}
		appendWarningIfMissing(liveSnap, models.Warning{Kind: "cache", Message: message})
		// The age field reports the daemon publication. Here the rejected
		// stale publication is the relevant one, so keep staleAge rather
		// than the fresh live snapshot's age (normally 0s).
		return commandSnapshot{snap: liveSnap, source: fallbackSource(liveSource), age: staleAge}, nil
	}
	appendWarningIfMissing(liveSnap, models.Warning{
		Kind:    "cache",
		Message: "using live snapshot (daemon cache unavailable)",
	})
	// Live-because-missing has no daemon publication age to report.
	return commandSnapshot{snap: liveSnap, source: fallbackSource(liveSource), age: "none"}, nil
}

func publicationIsFresh(snap *models.ClusterSnapshot) (bool, string) {
	if snap == nil {
		return false, "none"
	}
	age, known := publicationAge(snap)
	label := "none"
	if known {
		label = age.Round(time.Second).String()
	}
	if daemonMarkedPublicationStale(snap) {
		return false, label
	}
	if known && age > daemon.DefaultStaleThreshold {
		return false, label
	}
	if !known {
		// A successful cache read with no clock is not evidence the publication
		// is older than the threshold. Real daemon publications always carry
		// AssembledAt; this keeps clockless test doubles on the cache path.
		return true, "none"
	}
	return true, label
}

func publicationAge(snap *models.ClusterSnapshot) (time.Duration, bool) {
	if snap == nil {
		return 0, false
	}
	now := time.Now()
	if snap.Publication != nil && !snap.Publication.AssembledAt.IsZero() {
		age := now.Sub(snap.Publication.AssembledAt)
		if age < 0 {
			age = 0
		}
		return age, true
	}
	if snap.Publication != nil && snap.Publication.CacheAgeSec > 0 {
		return time.Duration(snap.Publication.CacheAgeSec) * time.Second, true
	}
	if !snap.Timestamp.IsZero() {
		age := now.Sub(snap.Timestamp)
		if age < 0 {
			age = 0
		}
		return age, true
	}
	return 0, false
}

func publicationAgeLabel(snap *models.ClusterSnapshot) string {
	age, known := publicationAge(snap)
	if !known {
		return "none"
	}
	return age.Round(time.Second).String()
}

func daemonMarkedPublicationStale(snap *models.ClusterSnapshot) bool {
	if snap == nil {
		return false
	}
	for _, warning := range snap.Warnings {
		if strings.Contains(warning.Message, "daemon cache is stale") {
			return true
		}
	}
	return false
}

func formatReadOrigin(source, age string) string {
	if strings.TrimSpace(age) == "" {
		age = "none"
	}
	return fmt.Sprintf("%s | age %s", humanReadSource(source), age)
}

func humanReadSource(source string) string {
	switch sourceOrLive(source) {
	case "live", "live-fallback", "live-runtime":
		return "live sweep"
	case "daemon-cache", "disk-cache":
		return "daemon cache"
	default:
		normalized := sourceOrLive(source)
		if strings.Contains(normalized, "live") {
			return "live sweep"
		}
		if strings.Contains(normalized, "cache") {
			return "daemon cache"
		}
		return normalized
	}
}
