package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/safety"
	"github.com/toasterbook88/axis/internal/scripts"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
	"github.com/toasterbook88/axis/internal/transport"
	"github.com/toasterbook88/axis/internal/turboexec"
)

var loadPlacementState = state.Load

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

type taskPlaceOutput struct {
	Source   string                   `json:"source" yaml:"source"`
	Decision models.PlacementDecision `json:"decision" yaml:"decision"`
}

func taskPlaceCmd() *cobra.Command {
	var format string
	var cached bool
	var cacheAddr string

	cmd := &cobra.Command{
		Use:   "place [description]",
		Short: "Select the best node to run a task (advisory only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			desc := args[0]

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			decision, source, err := planTaskPlacement(
				ctx,
				desc,
				cached,
				func(ctx context.Context) (*models.ClusterSnapshot, string, error) {
					return daemon.FetchSnapshot(ctx, cacheAddr)
				},
				discoverLiveSnapshot,
			)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return err
			}

			if format == "json" {
				var payload any = decision
				if cached {
					payload = taskPlaceOutput{
						Source:   source,
						Decision: decision,
					}
				}
				return printOutput(payload, "json")
			}

			// Human-readable output
			if !decision.OK {
				if cached {
					fmt.Printf("Source: %s\n", source)
				}
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
			if cached {
				fmt.Printf("Source: %s\n", source)
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
	cmd.Flags().BoolVar(&cached, "cached", false, "Use the local daemon snapshot cache when available")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr, "Address of the local AXIS daemon cache")
	return cmd
}

func planTaskPlacement(
	ctx context.Context,
	desc string,
	cached bool,
	cachedLoader func(context.Context) (*models.ClusterSnapshot, string, error),
	liveLoader func(context.Context) (*models.ClusterSnapshot, string, error),
) (models.PlacementDecision, string, error) {
	snap, source, err := collectStatusSnapshot(ctx, cached, cachedLoader, liveLoader)
	if err != nil {
		return models.PlacementDecision{}, "", err
	}

	reqs := placement.InferRequirements(desc)
	st, stateErr := loadPlacementState()
	if stateErr != nil && st == nil {
		return models.PlacementDecision{}, "", stateErr
	}
	if stateErr != nil {
		appendWarningIfMissing(snap, models.Warning{
			Kind:    "state",
			Message: stateErr.Error(),
		})
	}
	decision := placement.SelectBestNode(reqs, snap.Nodes, st)
	decision.Reasoning = runtimectx.PrependWarningReasoning(decision.Reasoning, snap.Warnings)
	return decision, source, nil
}

func appendWarningIfMissing(snap *models.ClusterSnapshot, warning models.Warning) {
	if snap == nil {
		return
	}
	for _, existing := range snap.Warnings {
		if existing.Kind == warning.Kind && existing.Message == warning.Message && existing.Node == warning.Node {
			return
		}
	}
	snap.Warnings = append(snap.Warnings, warning)
}

type taskRunIntent struct {
	command              string
	label                string
	matchedScript        *scripts.Script
	matchedSkill         *skills.LearnedSkill
	requiresConfirmation bool
}

func reservationMBForRequirements(reqs models.TaskRequirements) int64 {
	return reqs.MinFreeRAMMB + 1024
}

func ensureReservationCapacity(snap *models.ClusterSnapshot, st *state.ClusterState, node string, reservationMB int64) error {
	if !daemon.CanReserve(snap, st, node, reservationMB) {
		return fmt.Errorf("node %s cannot reserve %d MB (current reservations exceed cap)", node, reservationMB)
	}
	return nil
}

func resolveTaskRunIntent(input string, execFlag, scriptFlag bool, skillStore *skills.Store) (taskRunIntent, error) {
	if execFlag && scriptFlag {
		return taskRunIntent{}, fmt.Errorf("use either --exec for a raw command or --script for a known script/skill, not both")
	}

	var intent taskRunIntent
	if skillStore != nil {
		if skill, ok := skillStore.BestMatch(input); ok {
			skillCopy := skill
			intent.matchedSkill = &skillCopy
		}
	}
	if script, ok := scripts.GetBestScript(input); ok {
		scriptCopy := script
		intent.matchedScript = &scriptCopy
	}

	if execFlag {
		intent.command = input
		intent.label = "raw command"
		return intent, nil
	}

	if scriptFlag {
		if intent.matchedScript != nil {
			intent.command = intent.matchedScript.Command
			intent.label = fmt.Sprintf("fallback script %q", intent.matchedScript.Name)
			return intent, nil
		}
		if intent.matchedSkill != nil {
			intent.command = intent.matchedSkill.Command
			intent.label = fmt.Sprintf("learned skill %q", intent.matchedSkill.ID)
			return intent, nil
		}
		return taskRunIntent{}, fmt.Errorf("no known script or learned skill matches %q", input)
	}

	if intent.matchedScript != nil {
		intent.command = intent.matchedScript.Command
		intent.label = fmt.Sprintf("fallback script %q", intent.matchedScript.Name)
		intent.requiresConfirmation = true
		return intent, nil
	}
	if intent.matchedSkill != nil {
		intent.command = intent.matchedSkill.Command
		intent.label = fmt.Sprintf("learned skill %q", intent.matchedSkill.ID)
		intent.requiresConfirmation = true
		return intent, nil
	}

	return taskRunIntent{}, fmt.Errorf("refusing to execute implicitly: use --exec for a raw command or --script for a known script/skill")
}

func taskRunCmd() *cobra.Command {
	var execFlag, scriptFlag bool
	cmd := &cobra.Command{
		Use:   "run [description-or-command]",
		Short: "Run task on best node (explicit only — advisory placement first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input := args[0]
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			rt, err := runtimectx.Load(ctx)
			if err != nil {
				Fatal(ExitErrConfigLoad, "Failed to load runtime context: %v", err)
			}

			skillStore := rt.Skills
			intent, err := resolveTaskRunIntent(input, execFlag, scriptFlag, skillStore)
			if err != nil {
				return err
			}

			cfg := rt.Config
			snap := rt.Snapshot
			st := rt.State
			reqs := placement.InferRequirements(input)

			if intent.matchedScript != nil {
				reqs.RequiredTools = append([]string(nil), intent.matchedScript.RequiredTools...)
				if intent.matchedScript.EstRAMMB > reqs.MinFreeRAMMB {
					reqs.MinFreeRAMMB = intent.matchedScript.EstRAMMB
				}
			}

			// Always bypass strict requirement for purely explicit runs if user just says "df -h"
			if execFlag && len(reqs.RequiredTools) > 0 {
				filtered := reqs.RequiredTools[:0]
				for _, tool := range reqs.RequiredTools {
					if !strings.EqualFold(tool, "ollama") {
						filtered = append(filtered, tool)
					}
				}
				reqs.RequiredTools = append([]string(nil), filtered...)
			}

			decision := placement.SelectBestNode(reqs, snap.Nodes, st)
			decision.Reasoning = runtimectx.PrependWarningReasoning(decision.Reasoning, snap.Warnings)

			if !decision.OK {
				for _, r := range decision.Reasoning {
					fmt.Printf("  - %s\n", r)
				}
				Fatal(ExitErrNoNodesFit, "no suitable node found")
			}

			fmt.Printf("Selected node: %s (fit %d/100)\n", decision.Node, decision.FitScore)
			for _, r := range decision.Reasoning {
				fmt.Printf("  - %s\n", r)
			}

			// 2. require an explicit execution opt-in for any script/skill suggestion.
			if intent.requiresConfirmation {
				fmt.Printf("\nSuggested %s for %q:\n%s\n", intent.label, input, intent.command)
				return fmt.Errorf("refusing to execute implicitly; re-run with --script to execute the suggestion or --exec to run a raw command")
			}

			commandToRun := intent.command
			if intent.matchedSkill != nil && scriptFlag {
				fmt.Printf("\n=== AXIS LEARNED SKILL: %s ===\n%s\n", intent.matchedSkill.ID, intent.matchedSkill.Description)
			} else if intent.matchedScript != nil && scriptFlag {
				fmt.Printf("\n=== MOLE FALLBACK SCRIPT: %s ===\n%s\n", intent.matchedScript.Name, intent.matchedScript.Description)
			}

			reservationMB := reservationMBForRequirements(reqs)
			if err := ensureReservationCapacity(snap, st, decision.Node, reservationMB); err != nil {
				return err
			}

			// 3. execute with stream
			// Match the node explicitly
			var targetNode models.NodeFacts
			for _, n := range snap.Nodes {
				if n.Name == decision.Node {
					targetNode = n
					break
				}
			}

			turboPlan := turboexec.Prepare(targetNode, reqs, commandToRun)
			commandToRun = turboPlan.Command

			fmt.Printf("\n=== EXECUTING ON %s ===\n%s\n", decision.Node, commandToRun)
			for _, note := range turboPlan.Notes {
				fmt.Printf("  - %s\n", note)
			}
			fmt.Println()

			// knowledge injection
			k := knowledge.Build(snap, st, decision.Node)

			// Safety blocker applies only once the user has explicitly opted into execution.
			if block := safety.Check(k, commandToRun, skillStore.IsKnownBad); block.Blocked {
				if out, err := exec.Command("toilet", "-f", "mono12", "-F", "metal", "BULLSHIT BLOCKED").Output(); err == nil {
					fmt.Print("\n", string(out), "\n")
				} else {
					fmt.Printf("\n=== BULLSHIT BLOCKED ===\n")
				}
				fmt.Printf("Reason: %s\n", block.Reason)
				fmt.Printf("Dumb score: %d/100\n", block.Score)
				fmt.Println("Nothing was executed. Fix your request.")
				return nil
			}

			contextJSON, err := knowledge.ExecutionContextJSON(snap, st, decision, input, intent.matchedScript, intent.matchedSkill)
			if err != nil {
				Fatal(ExitErrContextWrite, "failed to encode execution context: %v", err)
			}

			if models.IsLocalNode(targetNode) {
				contextFile, err := os.CreateTemp("", "axis-knows-*.json")
				if err != nil {
					Fatal(ExitErrContextWrite, "failed to create context file: %v", err)
				}
				defer os.Remove(contextFile.Name())
				if _, err := contextFile.Write(contextJSON); err != nil {
					contextFile.Close()
					Fatal(ExitErrContextWrite, "failed to write context: %v", err)
				}
				if err := contextFile.Close(); err != nil {
					Fatal(ExitErrContextWrite, "failed to finalize context file: %v", err)
				}

				c := exec.CommandContext(ctx, "bash", "-lc", commandToRun)
				c.Env = append(os.Environ(),
					"AXIS_CONTEXT_FILE="+contextFile.Name(),
					"BEST_NODE="+decision.Node,
				)
				c.Env = append(c.Env, turboPlan.Env...)
				if st != nil {
					execID, err := st.AcquireTask(decision.Node, input, reservationMB)
					if err != nil {
						return fmt.Errorf("failed to persist task reservation: %w", err)
					}
					defer func() {
						if err := st.ReleaseTask(decision.Node, execID, reservationMB); err != nil {
							fmt.Fprintf(os.Stderr, "warning: failed to release task reservation: %v\n", err)
						}
					}()
				}
				c.Stdout = os.Stdout
				c.Stderr = os.Stderr
				runErr := c.Run()
				if runErr == nil {
					skillStore.RecordSuccess(input, commandToRun, decision.Node)
					skillStore.Save()
				} else {
					exitCode := 1
					if err, ok := runErr.(*exec.ExitError); ok {
						exitCode = err.ExitCode()
					}
					skillStore.RecordFailure(input, "failed with code "+fmt.Sprint(exitCode))
					skillStore.Save()
				}
				return runErr
			} else {
				// Find config for SSH transport
				var targetConfig config.NodeConfig
				for _, nc := range cfg.Nodes {
					if nc.Name == decision.Node {
						targetConfig = nc
						break
					}
				}

				executor := transport.NewSSHExecutor(targetConfig.Hostname, targetConfig.EffectiveSSHPort(), targetConfig.SSHUser, targetConfig.EffectiveTimeout())
				defer executor.Close()

				remoteContextPath := fmt.Sprintf("/tmp/axis-knows-%d.json", time.Now().UTC().UnixNano())
				writeJSONCmd := fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF\n", shellescape.Quote(remoteContextPath), string(contextJSON))
				if _, err := executor.Run(ctx, writeJSONCmd); err != nil {
					Fatal(ExitErrContextWrite, "failed to upload context: %v", err)
				}

				if st != nil {
					execID, err := st.AcquireTask(decision.Node, input, reservationMB)
					if err != nil {
						return fmt.Errorf("failed to persist task reservation: %w", err)
					}
					defer func() {
						if err := st.ReleaseTask(decision.Node, execID, reservationMB); err != nil {
							fmt.Fprintf(os.Stderr, "warning: failed to release task reservation: %v\n", err)
						}
					}()
				}

				quotedCmd := fmt.Sprintf(
					"%s trap 'rm -f %s' EXIT; bash -lc %s",
					remoteExecPrefix(decision.Node, remoteContextPath, turboPlan.Env),
					shellescape.Quote(remoteContextPath),
					shellescape.Quote(commandToRun),
				)
				runErr := executor.Stream(ctx, quotedCmd, os.Stdout, os.Stderr)
				if runErr == nil {
					skillStore.RecordSuccess(input, commandToRun, decision.Node)
					skillStore.Save()
				}
				if runErr != nil {
					exitCode := 1
					if err, ok := runErr.(*exec.ExitError); ok {
						exitCode = err.ExitCode()
					}
					skillStore.RecordFailure(input, "failed with code "+fmt.Sprint(exitCode))
					skillStore.Save()
					return fmt.Errorf("remote execution failed: %w", runErr)
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
	var cached bool
	var cacheAddr string

	cmd := &cobra.Command{
		Use:   "context [description]",
		Short: "Emit 200-token context block for Gemini/Codex/Copilot/OpenCode",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			desc := args[0]
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			snap, source, err := collectStatusSnapshot(
				ctx,
				cached,
				func(ctx context.Context) (*models.ClusterSnapshot, string, error) {
					return daemon.FetchSnapshot(ctx, cacheAddr)
				},
				discoverLiveSnapshot,
			)
			if err != nil {
				Fatal(ExitErrConfigLoad, "Failed to load snapshot: %v", err)
			}

			reqs := placement.InferRequirements(desc)
			printContextBlock(snap, reqs, desc, source)
			return nil
		},
	}
	cmd.Flags().BoolVar(&cached, "cached", false, "Use the local daemon snapshot cache when available")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr, "Address of the local AXIS daemon cache")
	return cmd
}

func printContextBlock(snap *models.ClusterSnapshot, reqs models.TaskRequirements, task, source string) {
	fmt.Println(buildContextBlock(snap, reqs, task, source))
}

func buildContextBlock(snap *models.ClusterSnapshot, reqs models.TaskRequirements, task, source string) string {
	if snap == nil || len(snap.Nodes) == 0 {
		return "No nodes found in cluster."
	}

	best, ok := selectContextNode(snap.Nodes, reqs)
	if !ok {
		return "No nodes found in cluster."
	}

	ramSummary := "unknown"
	pressure := "unknown"
	extraLines := ""
	if best.Resources != nil {
		if best.Resources.RAMReservedMB > 0 || best.Resources.RAMAllocatableMB > 0 {
			ramSummary = fmt.Sprintf("%dMB allocatable (%dMB reserved)", best.Resources.RAMAllocatableMB, best.Resources.RAMReservedMB)
		} else {
			ramSummary = fmt.Sprintf("%dMB free", best.Resources.RAMFreeMB)
		}
		pressure = best.Resources.Pressure
	}
	if best.TurboQuant != nil && best.TurboQuant.Supported && len(best.TurboQuant.Backends) > 0 {
		status := "detected"
		if best.TurboQuant.Verified {
			status = "verified"
		}
		line := fmt.Sprintf("TurboQuant %s: %s", status, strings.Join(best.TurboQuant.Backends, ", "))
		if len(best.TurboQuant.Capabilities) > 0 {
			line += fmt.Sprintf(" (%s)", strings.Join(best.TurboQuant.Capabilities, ", "))
		}
		extraLines += "\n- " + line
	}
	if matrix := turboQuantCapabilityMatrix(snap.Nodes); matrix != "" {
		extraLines += "\n- TurboQuant matrix: " + matrix
	}

	return fmt.Sprintf(`AXIS CLUSTER CONTEXT (paste as system prompt):

- Source: %s
- Best node: %s (%s, %s pressure)
- Context hint: %s
- Tools: %v
- Summary: %s
- Task: %s
- Live tools: start read-only MCP with: axis mcp serve%s

Be precise. Use real node names and tools above.`,
		sourceOrLive(source), best.Name, ramSummary, pressure,
		contextHint(reqs), toolsList(best), clusterSummaryLine(snap), task, extraLines)
}

func clusterSummaryLine(snap *models.ClusterSnapshot) string {
	if snap == nil {
		return "unknown"
	}
	if snap.Summary.TotalAllocatableMB > 0 || snap.Summary.TotalReservedMB > 0 {
		return fmt.Sprintf("%d nodes, %dMB allocatable across cluster (%dMB reserved)",
			len(snap.Nodes), snap.Summary.TotalAllocatableMB, snap.Summary.TotalReservedMB)
	}
	return fmt.Sprintf("%d nodes, %dMB total free RAM", len(snap.Nodes), snap.Summary.TotalFreeRAMMB)
}

func sourceOrLive(source string) string {
	if strings.TrimSpace(source) == "" {
		return "live"
	}
	return source
}

func remoteExecPrefix(node, contextPath string, extraEnv []string) string {
	parts := []string{
		"export",
		"BEST_NODE=" + shellescape.Quote(node),
		"AXIS_CONTEXT_FILE=" + shellescape.Quote(contextPath),
	}
	for _, kv := range extraEnv {
		if strings.TrimSpace(kv) == "" {
			continue
		}
		if idx := strings.Index(kv, "="); idx > 0 {
			parts = append(parts, kv[:idx]+"="+shellescape.Quote(kv[idx+1:]))
		}
	}
	return strings.Join(parts, " ") + ";"
}

func contextHint(reqs models.TaskRequirements) string {
	if reqs.ContextWindowTokens > 0 {
		return fmt.Sprintf("long-context (~%d tokens)", reqs.ContextWindowTokens)
	}
	return "standard"
}

func selectContextNode(nodes []models.NodeFacts, reqs models.TaskRequirements) (models.NodeFacts, bool) {
	if len(nodes) == 0 {
		return models.NodeFacts{}, false
	}

	// Keep the context block broad: prefer a capable node even if the exact tool is absent.
	reqs.RequiredTools = nil
	ranked := placement.RankCandidates(placement.FilterCandidates(reqs, nodes, nil), reqs, nil)
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
	seen := make(map[string]struct{}, len(n.Tools))
	for _, tool := range n.Tools {
		if _, ok := seen[tool.Name]; ok {
			continue
		}
		seen[tool.Name] = struct{}{}
		t = append(t, tool.Name)
	}
	return t
}

func turboQuantCapabilityMatrix(nodes []models.NodeFacts) string {
	entries := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if node.TurboQuant == nil || !node.TurboQuant.Supported {
			continue
		}
		entry := fmt.Sprintf("%s=%s/%s", node.Name, turboQuantContextStatus(node), turboQuantExecutionMode(node))
		if len(node.TurboQuant.Backends) > 0 {
			entry += fmt.Sprintf(" (%s)", strings.Join(node.TurboQuant.Backends, ", "))
		}
		entries = append(entries, entry)
	}
	sort.Strings(entries)
	return strings.Join(entries, "; ")
}

func turboQuantContextStatus(node models.NodeFacts) string {
	if node.TurboQuant != nil && node.TurboQuant.Verified {
		return "verified"
	}
	return "detected"
}

func turboQuantExecutionMode(node models.NodeFacts) string {
	return turboexec.ExecutionMode(node)
}
