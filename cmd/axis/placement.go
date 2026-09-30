package main

import (
	"context"
	"fmt"
	"io"
	"strings"
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
	Age         string                      `json:"age,omitempty" yaml:"age,omitempty"`
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
	var live bool
	var cacheAddr string

	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if err := validateOutputFormat(&format, "text", "json")(cmd, args); err != nil {
				return err
			}
			return rejectLiveAndCachedOnly(live, cachedOnly)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			desc := args[0]
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			// --cached matches the default cache-first read and does not override --live.
			_ = cached

			read, err := loadCommandSnapshot(
				ctx,
				live,
				cachedOnly,
				func(ctx context.Context) (*models.ClusterSnapshot, string, error) {
					return fetchTaskSnapshot(ctx, cacheAddr)
				},
				loadTaskLiveSnapshot,
			)
			var explanation models.PlacementExplanation
			var source, age string
			if err == nil {
				explanation, source, age, err = explainPlacementFromSnapshot(ctx, desc, read.snap, read.source, read.age)
			}
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}

			if format == "json" {
				return printOutput(cmd.OutOrStdout(), placementExplainOutput{
					Source:      source,
					Age:         age,
					Explanation: explanation,
				}, "json")
			}

			if err := printPlacementExplanationText(cmd.OutOrStdout(), explanation, source, age); err != nil {
				return err
			}
			if !explanation.Decision.OK {
				return ExitCodeError{Code: ExitErrNoNodesFit, Message: "no suitable node found"}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	cmd.Flags().BoolVar(&cached, "cached", false, "Read the daemon publication when it is inside the 5-minute stale threshold (this is the default)")
	cmd.Flags().BoolVar(&cachedOnly, "cached-only", false, "Require a fresh daemon publication; fail instead of falling back to a live sweep")
	cmd.Flags().BoolVar(&live, "live", false, "Perform a live cluster discovery sweep instead of reading the daemon publication")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS API daemon cache (Unix socket or TCP host:port)")
	return cmd
}

func explainPlacementFromSnapshot(ctx context.Context, desc string, snap *models.ClusterSnapshot, source, age string) (models.PlacementExplanation, string, string, error) {
	if snap == nil {
		return models.PlacementExplanation{}, "", "", fmt.Errorf("cluster snapshot is empty")
	}
	reqs := placementpkg.InferRequirements(desc)
	st, stateErr := loadPlacementState()
	if stateErr != nil && st == nil {
		return models.PlacementExplanation{}, "", "", stateErr
	}
	if stateErr != nil {
		appendWarningIfMissing(snap, models.Warning{
			Kind:    "state",
			Message: stateErr.Error(),
		})
	}

	explanation := placementpkg.ExplainPlacement(reqs, snap.Nodes, st)
	explanation.Decision.Reasoning = runtimectx.PrependWarningReasoning(explanation.Decision.Reasoning, snap.Warnings)
	// Advisory inference-route hints from ~/.axis/ai.yaml (does not change FitScore).
	// Offline (SkipProbe): respects ctx cancellation and --cached-only semantics.
	appendInferenceRouteHints(ctx, &explanation.Decision, desc)
	if strings.TrimSpace(age) == "" {
		age = "none"
	}
	return explanation, source, age, nil
}

func printPlacementExplanationText(destination io.Writer, explanation models.PlacementExplanation, source, age string) error {
	var rendered strings.Builder
	out := &rendered

	fmt.Fprintf(out, "%s %s\n", ui.Dim("Source:"), formatReadOrigin(source, age))
	// Cache fallback and other fact-plane warnings are prepended onto decision
	// reasoning. Surface each once so a live fallback is not silent.
	for _, reason := range explanation.Decision.Reasoning {
		const prefix = "warning: "
		if strings.HasPrefix(reason, prefix) {
			ui.FprintWarning(out, strings.TrimPrefix(reason, prefix))
		}
	}

	if explanation.Decision.OK {
		locality := ui.Dim("remote")
		if explanation.Decision.IsLocal {
			locality = ui.Green("local")
		}
		fmt.Fprintf(out, "%s %s (%s, %s %s)\n",
			ui.Green("✓"),
			ui.Bold(explanation.Decision.Node),
			locality,
			ui.Dim(rankingHeadlineLabel(explanation.Decision)),
			ui.Cyan(rankingHeadlineValue(explanation.Decision)))
	} else {
		fmt.Fprintf(out, "%s %s\n", ui.Red("✗"), "No suitable node found.")
	}

	if len(explanation.Eligible) > 0 {
		fmt.Fprintf(out, "\n%s\n", ui.Bold("Advisory Placement"))
		if explanation.Decision.Ranking != nil {
			ranking := explanation.Decision.Ranking
			fmt.Fprintf(out, "%s objective=%s (%s); metric provenance=%s; decisive=%s\n",
				ui.Dim("Ranked by"), ranking.Objective, ranking.Source, ranking.Metric.Provenance, ranking.DecisiveBy)
		} else {
			fmt.Fprintf(out, "%s unavailable\n", ui.Dim("Ranking"))
		}
		for i, candidate := range explanation.Eligible {
			locality := ui.Dim("remote")
			if candidate.IsLocal {
				locality = ui.Green("local")
			}
			fmt.Fprintf(out, "%d. %s (%s, %s %s, residual headroom %s)\n",
				i+1,
				ui.Bold(candidate.Node),
				locality,
				candidate.Metric.Name,
				ui.Cyan(fmt.Sprintf("%.0f%s", candidate.Metric.Value, candidate.Metric.Unit)),
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
	_, err := fmt.Fprint(destination, rendered.String())
	return err
}
