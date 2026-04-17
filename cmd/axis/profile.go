package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/ui"
	"github.com/toasterbook88/axis/internal/workload"
)

type profileMatchOutput struct {
	Match        models.WorkloadProfileMatch `json:"match" yaml:"match"`
	Requirements models.TaskRequirements     `json:"requirements" yaml:"requirements"`
}

func profileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Inspect deterministic workload classification",
	}
	cmd.AddCommand(profileMatchCmd())
	return cmd
}

func profileMatchCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "match [intent]",
		Short: "See which workload class and requirements match an intent string",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			intent := args[0]
			match := workload.Match(intent)
			reqs := placement.InferRequirements(intent)

			output := profileMatchOutput{
				Match:        match,
				Requirements: reqs,
			}

			switch format {
			case "json", "yaml":
				return printOutput(output, format)
			default:
				printProfileMatchText(cmd, output)
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&format, "format", "text", "Output format: text, json, or yaml")
	return cmd
}

func printProfileMatchText(cmd *cobra.Command, output profileMatchOutput) {
	out := cmd.OutOrStdout()
	reqs := output.Requirements

	fmt.Fprintf(out, "%s %s\n\n", ui.Bold("MATCHED CLASS:"), ui.Cyan(string(output.Match.Class)))
	if len(output.Match.Notes) > 0 {
		fmt.Fprintf(out, "%s\n", ui.Bold("Notes"))
		for _, note := range output.Match.Notes {
			fmt.Fprintf(out, "  %s %s\n", ui.Dim("-"), note)
		}
		fmt.Fprintln(out)
	}

	fmt.Fprintf(out, "%s\n", ui.Bold("Inferred Requirements"))
	fmt.Fprintf(out, "  %s %d MB\n", ui.Dim("Min RAM:"), reqs.MinFreeRAMMB)
	if len(reqs.RequiredTools) > 0 {
		fmt.Fprintf(out, "  %s %s\n", ui.Dim("Required tools:"), strings.Join(reqs.RequiredTools, ", "))
	} else {
		fmt.Fprintf(out, "  %s none\n", ui.Dim("Required tools:"))
	}
	if len(reqs.PreferredBackends) > 0 {
		fmt.Fprintf(out, "  %s %s\n", ui.Dim("Preferred backends:"), strings.Join(reqs.PreferredBackends, ", "))
	} else {
		fmt.Fprintf(out, "  %s none\n", ui.Dim("Preferred backends:"))
	}
	if reqs.ContextWindowTokens > 0 {
		fmt.Fprintf(out, "  %s %d\n", ui.Dim("Context window tokens:"), reqs.ContextWindowTokens)
	} else {
		fmt.Fprintf(out, "  %s none\n", ui.Dim("Context window tokens:"))
	}
	fmt.Fprintf(out, "  %s %t\n", ui.Dim("Prefers turboquant:"), reqs.PrefersTurboQuant)
}
