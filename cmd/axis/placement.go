package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/models"
	placementpkg "github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/ui"
)

type placementExplainOutput struct {
	Source      string                      `json:"source" yaml:"source"`
	Explanation models.PlacementExplanation `json:"explanation" yaml:"explanation"`
}

func placementCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "placement",
		Short: "Explain deterministic placement decisions",
	}
	cmd.AddCommand(placementExplainCmd())
	return cmd
}

func placementExplainCmd() *cobra.Command {
	return newPlacementExplainCommand(
		"explain [intent]",
		"Explain how the cluster would rank nodes for a task",
	)
}

func newPlacementExplainCommand(use, short string) *cobra.Command {
	var format string
	var cached bool
	var cachedOnly bool
	var cacheAddr string

	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			desc := args[0]
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cacheRequested := cached || cachedOnly

			explanation, source, err := planTaskExplanation(
				ctx,
				desc,
				cacheRequested,
				cachedOnly,
				func(ctx context.Context) (*models.ClusterSnapshot, string, error) {
					return fetchTaskSnapshot(ctx, cacheAddr)
				},
				loadTaskLiveSnapshot,
			)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return err
			}

			if format == "json" {
				var payload any = explanation
				if cacheRequested {
					payload = placementExplainOutput{
						Source:      source,
						Explanation: explanation,
					}
				}
				return printOutput(cmd.OutOrStdout(), payload, "json")
			}

			printPlacementExplanationText(cmd.OutOrStdout(), explanation, source, cacheRequested)
			if !explanation.Decision.OK {
				return ExitCodeError{Code: ExitErrNoNodesFit, Message: "no suitable node found"}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	cmd.Flags().BoolVar(&cached, "cached", false, "Use the local daemon snapshot cache when available")
	cmd.Flags().BoolVar(&cachedOnly, "cached-only", false, "Require daemon cache; fail instead of falling back to live discovery")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS API daemon cache (Unix socket or TCP host:port)")
	return cmd
}

func planTaskExplanation(
	ctx context.Context,
	desc string,
	cached bool,
	cachedOnly bool,
	cachedLoader func(context.Context) (*models.ClusterSnapshot, string, error),
	liveLoader func(context.Context) (*models.ClusterSnapshot, string, error),
) (models.PlacementExplanation, string, error) {
	snap, source, err := collectStatusSnapshot(ctx, cached, cachedOnly, cachedLoader, liveLoader)
	if err != nil {
		return models.PlacementExplanation{}, "", err
	}

	reqs := placementpkg.InferRequirements(desc)
	st, stateErr := loadPlacementState()
	if stateErr != nil && st == nil {
		return models.PlacementExplanation{}, "", stateErr
	}
	if stateErr != nil {
		appendWarningIfMissing(snap, models.Warning{
			Kind:    "state",
			Message: stateErr.Error(),
		})
	}

	explanation := placementpkg.ExplainPlacement(reqs, snap.Nodes, st)
	explanation.Decision.Reasoning = runtimectx.PrependWarningReasoning(explanation.Decision.Reasoning, snap.Warnings)
	return explanation, source, nil
}

func printPlacementExplanationText(out io.Writer, explanation models.PlacementExplanation, source string, showSource bool) {
	if showSource {
		fmt.Fprintf(out, "%s %s\n", ui.Dim("Source:"), source)
	}

	if explanation.Decision.OK {
		locality := ui.Dim("remote")
		if explanation.Decision.IsLocal {
			locality = ui.Green("local")
		}
		fmt.Fprintf(out, "%s %s (%s, fit %s)\n",
			ui.Green("✓"),
			ui.Bold(explanation.Decision.Node),
			locality,
			ui.Cyan(fmt.Sprintf("%d/100", explanation.Decision.FitScore)))
	} else {
		fmt.Fprintf(out, "%s %s\n", ui.Red("✗"), "No suitable node found.")
	}

	if len(explanation.Eligible) > 0 {
		fmt.Fprintf(out, "\n%s\n", ui.Bold("Advisory Placement"))
		for i, candidate := range explanation.Eligible {
			locality := ui.Dim("remote")
			if candidate.IsLocal {
				locality = ui.Green("local")
			}
			fmt.Fprintf(out, "%d. %s (%s, fit %s, headroom %s)\n",
				i+1,
				ui.Bold(candidate.Node),
				locality,
				ui.Cyan(fmt.Sprintf("%d/100", candidate.FitScore)),
				ui.Cyan(fmt.Sprintf("%dMB", candidate.HeadroomMB)))
			for _, reason := range candidate.Reasoning {
				fmt.Fprintf(out, "   %s %s\n", ui.Dim("-"), reason)
			}
		}
	}

	if len(explanation.Excluded) > 0 {
		fmt.Fprintf(out, "\n%s\n", ui.Bold("Filtered"))
		for _, excluded := range explanation.Excluded {
			fmt.Fprintf(out, "%s %s\n", ui.Dim("-"), ui.Bold(excluded.Node))
			for _, reason := range excluded.Reasons {
				fmt.Fprintf(out, "   %s %s\n", ui.Dim("-"), reason)
			}
		}
	}
}
