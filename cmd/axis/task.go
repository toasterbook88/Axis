package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/snapshot"
)

func taskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Task placement commands",
	}
	cmd.AddCommand(taskPlaceCmd())
	return cmd
}

func taskPlaceCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "place [description]",
		Short: "Select the best node to run a task (advisory only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			desc := args[0]

			// Load config → discover → snapshot (reuse Phase 1 flow)
			cfgPath := config.DefaultConfigPath()
			cfg, err := config.Load(cfgPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			nodes := discovery.Discover(ctx, cfg)
			snap := snapshot.Build(nodes)

			// Infer requirements from description
			reqs := inferRequirements(desc)

			// Run placement
			decision := placement.SelectBestNode(reqs, snap.Nodes)

			if format == "json" {
				return printOutput(decision, "json")
			}

			// Human-readable output
			if !decision.OK {
				fmt.Println("No suitable node found.")
				for _, r := range decision.Reasoning {
					fmt.Printf("  - %s\n", r)
				}
				return nil
			}

			fmt.Printf("Selected node: %s\n", decision.Node)
			if decision.Tool != "" {
				fmt.Printf("Tool: %s\n", decision.Tool)
			}
			fmt.Println("Reason:")
			for _, r := range decision.Reasoning {
				fmt.Printf("  - %s\n", r)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&format, "format", "", "Output format: json")
	return cmd
}

// inferRequirements derives TaskRequirements from a task description string.
// Simple keyword matching — no ML, no parsing.
func inferRequirements(desc string) models.TaskRequirements {
	reqs := models.TaskRequirements{
		Description: desc,
	}

	lower := strings.ToLower(desc)

	// Tool inference
	switch {
	case containsAny(lower, "repo", "analyze", "code"):
		reqs.RequiredTool = "git"
	case containsAny(lower, "build"):
		reqs.RequiredTool = "go"
	}

	// RAM inference
	if containsAny(lower, "model", "large", "heavy") {
		reqs.MinFreeRAMMB = 4096
	}

	return reqs
}

func containsAny(s string, keywords ...string) bool {
	for _, kw := range keywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}
