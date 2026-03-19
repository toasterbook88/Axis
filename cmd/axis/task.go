package main

import (
	"context"
	"fmt"
	"os"
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
		Short: "Task placement and zero-risk context block emission",
	}
	cmd.AddCommand(taskPlaceCmd())
	cmd.AddCommand(taskContextCmd())
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
			reqs := placement.InferRequirements(desc)

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
				os.Exit(ExitErrNoNodesFit)
			}

			locality := "remote"
			if decision.IsLocal {
				locality = "local"
			}
			fmt.Printf("Selected node: %s (%s, fit %d/100)\n", decision.Node, locality, decision.FitScore)
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

// === NEW: axis task context <description> — zero-risk token saver ===
func taskContextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context [description]",
		Short: "Emit 200-token context block for Gemini/Codex/Copilot/OpenCode",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			desc := args[0]
			
			cfgPath := config.DefaultConfigPath()
			cfg, err := config.Load(cfgPath)
			if err != nil {
				Fatal(ExitErrConfigLoad, "Failed to load config: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			
			snap := snapshot.Build(discovery.Discover(ctx, cfg))
			reqs := placement.InferRequirements(desc)
			printContextBlock(snap, reqs, desc)
			return nil
		},
	}
	return cmd
}

func printContextBlock(snap *models.ClusterSnapshot, reqs models.TaskRequirements, task string) {
	if len(snap.Nodes) == 0 {
		fmt.Println("No nodes found in cluster.")
		return
	}
	
	reqs.RequiredTool = ""
	ranked := placement.RankCandidates(placement.FilterCandidates(reqs, snap.Nodes))
	if len(ranked) == 0 {
		ranked = snap.Nodes // fallback
	}
	best := ranked[0]
	
	fmt.Printf(`AXIS CLUSTER CONTEXT (paste as system prompt):

- Best node: %s (%dMB free, %s pressure)
- Tools: %v
- Summary: %d nodes, %dMB total free RAM
- Task: %s

Be precise. Use real node names and tools above.`, 
		best.Name, best.Resources.RAMFreeMB, best.Resources.Pressure,
		toolsList(best), len(snap.Nodes), snap.Summary.TotalFreeRAMMB, task)
	fmt.Println()
}

func toolsList(n models.NodeFacts) []string {
	var t []string
	for _, tool := range n.Tools { t = append(t, tool.Name) }
	return t
}


