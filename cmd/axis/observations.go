package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
	"github.com/toasterbook88/axis/internal/ui"
)

var loadObservationsState = state.Load

func observationsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "observations",
		Short: "Show execution observations tracked by the cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObservationsLocal(cmd)
		},
	}
	cmd.AddCommand(observationsListCmd())
	cmd.AddCommand(observationsInspectCmd())
	return cmd
}

func runObservationsLocal(cmd *cobra.Command) error {
	st, err := loadObservationsState()
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}
	if st == nil || len(st.Observations) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No observations tracked")
		return nil
	}
	entries := make([]models.ExecutionObservation, 0, len(st.Observations))
	for _, obs := range st.Observations {
		entries = append(entries, obs)
	}
	fmt.Fprint(cmd.OutOrStdout(), renderObservationTable(entries))
	return nil
}

func renderObservationTable(entries []models.ExecutionObservation) string {
	var b strings.Builder
	sep := strings.Repeat("─", 90)
	b.WriteString("\n")
	ui.WhiteColor.Fprintf(&b, "  EXECUTION OBSERVATIONS\n")
	b.WriteString("  ")
	b.WriteString(sep)
	b.WriteString("\n")

	if len(entries) == 0 {
		ui.DimColor.Fprintf(&b, "  No observations tracked\n\n")
		return b.String()
	}

	ui.WhiteColor.Fprintf(&b, "  %-15s %-12s %-12s %-12s %10s %10s %8s %8s\n",
		"NODE", "WORKLOAD", "BACKEND", "TOOL", "WALL MS", "PEAK RAM", "PEAK VRAM", "SAMPLES")
	b.WriteString("  ")
	b.WriteString(sep)
	b.WriteString("\n")

	display := entries
	truncated := 0
	if len(entries) > 50 {
		display = entries[:50]
		truncated = len(entries) - 50
	}

	for _, obs := range display {
		peakVRAM := "-"
		if obs.PeakVRAMMB > 0 {
			peakVRAM = fmt.Sprintf("%d MB", obs.PeakVRAMMB)
		}
		success := ""
		if !obs.LastSuccess {
			success = ui.RedColor.Sprintf(" (last failed)")
		}
		fmt.Fprintf(&b, "  %-15s %-12s %-12s %-12s %10d %10d %8s %8d%s\n",
			obs.Scope.Node,
			obs.Scope.Workload,
			obs.Scope.Backend,
			obs.Scope.Tool,
			obs.WallTimeMS,
			obs.PeakRAMMB,
			peakVRAM,
			obs.SampleCount,
			success,
		)
	}

	if truncated > 0 {
		ui.DimColor.Fprintf(&b, "\n  ... and %d more observations.\n", truncated)
	}

	b.WriteString("\n")
	return b.String()
}

func observationsListCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List execution observations from the local state ledger",
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := loadObservationsState()
			if err != nil {
				return fmt.Errorf("loading state: %w", err)
			}

			entries := make([]models.ExecutionObservation, 0, len(st.Observations))
			for _, obs := range st.Observations {
				entries = append(entries, obs)
			}
			sort.Slice(entries, func(i, j int) bool {
				return entries[i].ObservedAt.After(entries[j].ObservedAt)
			})

			switch format {
			case "json":
				return json.NewEncoder(cmd.OutOrStdout()).Encode(entries)
			default:
				if len(entries) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No observations tracked")
					return nil
				}
				tbl := ui.NewTable("KEY", "NODE", "WORKLOAD", "BACKEND", "TOOL", "WALL MS", "PEAK RAM", "PEAK VRAM", "SAMPLES", "OBSERVED")
				for _, obs := range entries {
					key := state.ObservationKey(obs.Scope)
					peakVRAM := "-"
					if obs.PeakVRAMMB > 0 {
						peakVRAM = fmt.Sprintf("%d MB", obs.PeakVRAMMB)
					}
					tbl.AddRow(
						truncateID(key, 12),
						obs.Scope.Node,
						string(obs.Scope.Workload),
						obs.Scope.Backend,
						obs.Scope.Tool,
						fmt.Sprintf("%d", obs.WallTimeMS),
						fmt.Sprintf("%d MB", obs.PeakRAMMB),
						peakVRAM,
						fmt.Sprintf("%d", obs.SampleCount),
						obs.ObservedAt.Format(time.RFC3339),
					)
				}
				tbl.Render(cmd.OutOrStdout())
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	return cmd
}

func observationsInspectCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "inspect <key>",
		Short: "Show full details of an execution observation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			st, err := loadObservationsState()
			if err != nil {
				return fmt.Errorf("loading state: %w", err)
			}

			var found *models.ExecutionObservation
			for k, obs := range st.Observations {
				if k == key {
					obsCopy := obs
					found = &obsCopy
					break
				}
			}
			if found == nil {
				// Allow lookup by prefix for convenience.
				for k, obs := range st.Observations {
					if strings.HasPrefix(k, key) {
						obsCopy := obs
						found = &obsCopy
						break
					}
				}
			}

			if found == nil {
				return ExitCodeError{Code: ExitErrGeneric, Message: fmt.Sprintf("observation %q not found", key)}
			}

			switch format {
			case "json":
				return json.NewEncoder(cmd.OutOrStdout()).Encode(found)
			default:
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "Key:         %s\n", state.ObservationKey(found.Scope))
				fmt.Fprintf(out, "Node:        %s\n", found.Scope.Node)
				fmt.Fprintf(out, "Workload:    %s\n", found.Scope.Workload)
				fmt.Fprintf(out, "Backend:     %s\n", found.Scope.Backend)
				fmt.Fprintf(out, "Tool:        %s\n", found.Scope.Tool)
				if found.Scope.ModelName != "" {
					fmt.Fprintf(out, "Model:       %s\n", found.Scope.ModelName)
				}
				fmt.Fprintf(out, "Wall Time:   %d ms\n", found.WallTimeMS)
				fmt.Fprintf(out, "Peak RAM:    %d MB\n", found.PeakRAMMB)
				if found.PeakVRAMMB > 0 {
					fmt.Fprintf(out, "Peak VRAM:   %d MB\n", found.PeakVRAMMB)
				}
				fmt.Fprintf(out, "Samples:     %d\n", found.SampleCount)
				fmt.Fprintf(out, "Last Success:%v\n", found.LastSuccess)
				fmt.Fprintf(out, "Observed At: %s\n", found.ObservedAt.Format(time.RFC3339))
				isStale := ""
				if !state.ObservationIsFresh(*found, time.Now().UTC()) {
					isStale = " (stale)"
				}
				fmt.Fprintf(out, "Fresh:       %v%s\n", state.ObservationIsFresh(*found, time.Now().UTC()), isStale)
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	return cmd
}
