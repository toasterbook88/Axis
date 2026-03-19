package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/snapshot"
	"github.com/toasterbook88/axis/internal/state"
	"github.com/toasterbook88/axis/internal/transport"
)

func taskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Task placement, context emission, and execution",
	}
	cmd.AddCommand(taskPlaceCmd())
	cmd.AddCommand(taskContextCmd())
	cmd.AddCommand(taskRunCmd())
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

			st, _ := state.Load()

			// Infer requirements from description
			reqs := placement.InferRequirements(desc)
			if st != nil && len(st.Decisions) > 0 {
				reqs.Description += " | context: " + st.Decisions[len(st.Decisions)-1]
			}

			// Run placement
			decision := placement.SelectBestNode(reqs, snap.Nodes, st)

			if decision.OK && st != nil {
				st.RecordPlacement(decision.Node, reqs.MinFreeRAMMB+1024, desc)
				st.Save()
			}

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

func taskRunCmd() *cobra.Command {
	var execFlag, scriptFlag bool
	cmd := &cobra.Command{
		Use:   "run [description-or-command]",
		Short: "Run task on best node (explicit only — advisory placement first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input := args[0]

			// 1. placement (reuse existing)
			cfgPath := config.DefaultConfigPath()
			cfg, err := config.Load(cfgPath)
			if err != nil {
				Fatal(ExitErrConfigLoad, "Failed to load config: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			
			nodes := discovery.Discover(ctx, cfg)
			snap := snapshot.Build(nodes)
			
			st, _ := state.Load()

			reqs := placement.InferRequirements(input)
			if st != nil && len(st.Decisions) > 0 {
				reqs.Description += " | context: " + st.Decisions[len(st.Decisions)-1]
			}
			
			// Always bypass strict requirement for purely explicit runs if user just says "df -h"
			if execFlag && reqs.RequiredTool == "ollama" {
				reqs.RequiredTool = ""
			}

			decision := placement.SelectBestNode(reqs, snap.Nodes, st)

			if decision.OK && st != nil {
				st.RecordPlacement(decision.Node, reqs.MinFreeRAMMB+1024, input)
				st.Save()
			}

			fmt.Printf("Selected node: %s (fit %d/100)\n", decision.Node, decision.FitScore)
			for _, r := range decision.Reasoning {
				fmt.Printf("  - %s\n", r)
			}

			if !decision.OK {
				Fatal(ExitErrNoNodesFit, "no suitable node found")
			}

			// 2. explicit command only
			commandToRun := input
			if scriptFlag {
				// future multi-line support
			}
			if !execFlag && !scriptFlag {
				return fmt.Errorf("use --exec \"command\" or --script (safety gate enforces conscious execution)")
			}

			fmt.Printf("\n=== EXECUTING ON %s ===\n%s\n\n", decision.Node, commandToRun)

			// 3. execute with stream
			// Match the node explicitly
			var targetNode models.NodeFacts
			for _, n := range snap.Nodes {
				if n.Name == decision.Node {
					targetNode = n
					break
				}
			}

			if models.IsLocalNode(targetNode) {
				c := exec.CommandContext(ctx, "bash", "-c", commandToRun)
				c.Stdout = os.Stdout
				c.Stderr = os.Stderr
				return c.Run()
			} else {
				// Find config for SSH transport
				var targetConfig config.NodeConfig
				for _, nc := range cfg.Nodes {
					if nc.Name == decision.Node {
						targetConfig = nc
						break
					}
				}
				
				executor := transport.NewSSHExecutor(targetConfig.Hostname, targetConfig.SSHPort, targetConfig.SSHUser, targetConfig.TimeoutSec)
				defer executor.Close()
				
				quotedCmd := "bash -c " + shellescape.Quote(commandToRun)
				if err := executor.Stream(ctx, quotedCmd, os.Stdout, os.Stderr); err != nil {
					return fmt.Errorf("remote execution failed: %w", err)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&execFlag, "exec", false, "run raw command (required for safety)")
	cmd.Flags().BoolVar(&scriptFlag, "script", false, "run multi-line script")
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
	fmt.Println(buildContextBlock(snap, reqs, task))
}

func buildContextBlock(snap *models.ClusterSnapshot, reqs models.TaskRequirements, task string) string {
	if snap == nil || len(snap.Nodes) == 0 {
		return "No nodes found in cluster."
	}

	best, ok := selectContextNode(snap.Nodes, reqs)
	if !ok {
		return "No nodes found in cluster."
	}

	freeRAM := "unknown"
	pressure := "unknown"
	if best.Resources != nil {
		freeRAM = fmt.Sprintf("%dMB", best.Resources.RAMFreeMB)
		pressure = best.Resources.Pressure
	}

	return fmt.Sprintf(`AXIS CLUSTER CONTEXT (paste as system prompt):

- Best node: %s (%s free, %s pressure)
- Tools: %v
- Summary: %d nodes, %dMB total free RAM
- Task: %s
- Live tools: start read-only MCP with: axis mcp serve

Be precise. Use real node names and tools above.`, best.Name, freeRAM, pressure,
		toolsList(best), len(snap.Nodes), snap.Summary.TotalFreeRAMMB, task)
}

func selectContextNode(nodes []models.NodeFacts, reqs models.TaskRequirements) (models.NodeFacts, bool) {
	if len(nodes) == 0 {
		return models.NodeFacts{}, false
	}

	// Keep the context block broad: prefer a capable node even if the exact tool is absent.
	reqs.RequiredTool = ""
	st, _ := state.Load()
	ranked := placement.RankCandidates(placement.FilterCandidates(reqs, nodes, st), reqs, st)
	if len(ranked) > 0 {
		return ranked[0], true
	}

	for _, n := range nodes {
		if n.Resources != nil {
			return n, true
		}
	}

	return nodes[0], true
}

func toolsList(n models.NodeFacts) []string {
	var t []string
	for _, tool := range n.Tools {
		t = append(t, tool.Name)
	}
	return t
}
