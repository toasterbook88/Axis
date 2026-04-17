package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/scripts"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
	"github.com/toasterbook88/axis/internal/turboexec"
	"github.com/toasterbook88/axis/internal/ui"
)

var loadPlacementState = state.Load
var fetchTaskSnapshot = daemon.FetchSnapshot
var loadTaskLiveSnapshot = discoverLiveSnapshot
var loadTaskRunRuntime = runtimectx.Load
var prepareTaskGuarded = execution.PrepareGuardedExecution
var runPreparedTaskGuarded = execution.RunPreparedExecution
var taskRunStdinIsTerminal = ui.StdinIsTerminal
var taskRunStdoutIsTerminal = ui.StdoutIsTerminal
var taskRunStderrIsTerminal = ui.StderrIsTerminal
var signalTaskRunDaemonRefresh = func(ctx context.Context, trigger string) error {
	return refreshDaemonCacheWithTrigger(ctx, api.DefaultAddr(), trigger)
}

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
	var cachedOnly bool
	var cacheAddr string

	cmd := &cobra.Command{
		Use:   "place [description]",
		Short: "Select the best node to run a task (advisory only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			desc := args[0]
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cacheRequested := cached || cachedOnly

			decision, source, err := planTaskPlacement(
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
				var payload any = decision
				if cacheRequested {
					payload = taskPlaceOutput{
						Source:   source,
						Decision: decision,
					}
				}
				return printOutput(payload, "json")
			}

			// Human-readable output
			if !decision.OK {
				if cacheRequested {
					fmt.Printf("%s %s\n", ui.Dim("Source:"), source)
				}
				fmt.Printf("%s %s\n", ui.Red("✗"), "No suitable node found.")
				for _, r := range decision.Reasoning {
					fmt.Printf("  %s %s\n", ui.Dim("-"), r)
				}
				os.Exit(ExitErrNoNodesFit)
			}

			locality := ui.Dim("remote")
			if decision.IsLocal {
				locality = ui.Green("local")
			}
			if cacheRequested {
				fmt.Printf("%s %s\n", ui.Dim("Source:"), source)
			}
			fmt.Printf("%s %s (%s, fit %s)\n",
				ui.Green("✓"),
				ui.Bold(decision.Node),
				locality,
				ui.Cyan(fmt.Sprintf("%d/100", decision.FitScore)))
			if decision.Tool != "" {
				fmt.Printf("  %s %s\n", ui.Dim("Tool:"), decision.Tool)
			}
			fmt.Printf("  %s\n", ui.Dim("Reason:"))
			for _, r := range decision.Reasoning {
				fmt.Printf("    %s %s\n", ui.Dim("-"), r)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&format, "format", "", "Output format: json")
	cmd.Flags().BoolVar(&cached, "cached", false, "Use the local daemon snapshot cache when available")
	cmd.Flags().BoolVar(&cachedOnly, "cached-only", false, "Require daemon cache; fail instead of falling back to live discovery")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS API daemon cache (Unix socket or TCP host:port)")
	return cmd
}

func planTaskPlacement(
	ctx context.Context,
	desc string,
	cached bool,
	cachedOnly bool,
	cachedLoader func(context.Context) (*models.ClusterSnapshot, string, error),
	liveLoader func(context.Context) (*models.ClusterSnapshot, string, error),
) (models.PlacementDecision, string, error) {
	explanation, source, err := planTaskExplanation(ctx, desc, cached, cachedOnly, cachedLoader, liveLoader)
	if err != nil {
		return models.PlacementDecision{}, "", err
	}
	return explanation.Decision, source, nil
}

func appendWarningIfMissing(snap *models.ClusterSnapshot, warning models.Warning) {
	models.AppendWarningIfMissing(snap, warning)
}

type taskRunIntent struct {
	command              string
	label                string
	matchedScript        *scripts.Script
	matchedSkill         *skills.LearnedSkill
	requiresConfirmation bool
}

func reservationMBForRequirements(reqs models.TaskRequirements) int64 {
	return execution.ReservationMBForRequirements(reqs)
}

func ensureReservationCapacity(snap *models.ClusterSnapshot, st *state.ClusterState, node string, reservationMB int64) error {
	if !execution.CanReserve(snap, st, node, reservationMB) {
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
	var execFlag, scriptFlag, dryRunFlag bool
	cmd := &cobra.Command{
		Use:   "run [description-or-command]",
		Short: "Run task on best node (explicit only — advisory placement first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input := args[0]
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()

			rt, err := loadTaskRunRuntime(ctx)
			if err != nil {
				Fatal(ExitErrConfigLoad, "Failed to load runtime context: %v", err)
			}

			skillStore := rt.Skills
			intent, err := resolveTaskRunIntent(input, execFlag, scriptFlag, skillStore)
			if err != nil {
				return err
			}

			if intent.requiresConfirmation {
				fmt.Printf("\nSuggested %s for %q:\n%s\n", intent.label, input, intent.command)
				return fmt.Errorf("refusing to execute implicitly; re-run with --script to execute the suggestion or --exec to run a raw command")
			}

			mode := execution.ModeExec
			if scriptFlag {
				mode = execution.ModeScript
			}

			req := execution.GuardedExecutionRequest{
				Description:  input,
				Mode:         mode,
				Confirm:      execution.ConfirmWord,
				OwnerSurface: execution.OwnerSurfaceTaskRun,
				Stdout:       os.Stdout,
				Stderr:       os.Stderr,
				OnStateChange: func(_ context.Context, trigger string, _ execution.GuardedExecutionResult) {
					scheduleTaskRunDaemonRefresh(trigger)
				},
				OnReady: func(resp execution.GuardedExecutionResult) {
					fmt.Printf("Selected node: %s (fit %d/100)\n", resp.Node, resp.FitScore)
					for _, reason := range resp.Reasoning {
						fmt.Printf("  - %s\n", reason)
					}
					if intent.matchedSkill != nil && scriptFlag {
						fmt.Printf("\n=== AXIS LEARNED SKILL: %s ===\n%s\n", intent.matchedSkill.ID, intent.matchedSkill.Description)
					} else if intent.matchedScript != nil && scriptFlag {
						fmt.Printf("\n=== MOLE FALLBACK SCRIPT: %s ===\n%s\n", intent.matchedScript.Name, intent.matchedScript.Description)
					}
					fmt.Printf("\n=== EXECUTING ON %s ===\n%s\n\n", resp.Node, resp.Command)
				},
			}

			prepared, err := prepareTaskGuarded(ctx, rt, req)
			if prepared.Result.Blocked {
				printBlockedResult(prepared.Result)
				return nil
			}
			if err != nil && prepared.Result.Error == "no suitable node found" {
				for _, reason := range prepared.Result.Reasoning {
					fmt.Printf("  - %s\n", reason)
				}
				Fatal(ExitErrNoNodesFit, "no suitable node found")
			}
			if err != nil {
				return err
			}

			if dryRunFlag {
				return printDryRunPlan(prepared)
			}

			if taskRunUsesTTYPrompt() {
				proceed, err := confirmTaskRunExecution(cmd, prepared)
				if err != nil {
					return err
				}
				if !proceed {
					fmt.Fprintln(cmd.ErrOrStderr(), "aborted; nothing executed")
					return nil
				}
			}

			resp, err := runTaskRunRequest(ctx, prepared)
			if err != nil && resp.Error == "no suitable node found" {
				for _, reason := range resp.Reasoning {
					fmt.Printf("  - %s\n", reason)
				}
				Fatal(ExitErrNoNodesFit, "no suitable node found")
			}
			if err != nil {
				return err
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&execFlag, "exec", false, "run raw command (required for safety)")
	cmd.Flags().BoolVar(&scriptFlag, "script", false, "run multi-line script")
	cmd.Flags().BoolVar(&dryRunFlag, "dry-run", false, "show the execution plan without running anything")
	return cmd
}

func scheduleTaskRunDaemonRefresh(trigger string) {
	scheduleBestEffortDaemonRefresh("task-run", trigger, signalTaskRunDaemonRefresh)
}

func runTaskRunRequest(ctx context.Context, prepared execution.PreparedExecution) (execution.GuardedExecutionResult, error) {
	return runPreparedTaskGuarded(ctx, prepared)
}

func taskRunUsesTTYPrompt() bool {
	return taskRunStdinIsTerminal() && taskRunStdoutIsTerminal() && taskRunStderrIsTerminal()
}

func confirmTaskRunExecution(cmd *cobra.Command, prepared execution.PreparedExecution) (bool, error) {
	errW := cmd.ErrOrStderr()
	locality := "remote"
	if prepared.Result.IsLocal {
		locality = "local"
	}
	workloadClass := string(prepared.Requirements.Workload.Class)
	if workloadClass == "" {
		workloadClass = "unknown"
	}

	fmt.Fprintln(errW, "About to run guarded execution:")
	fmt.Fprintf(errW, "  Node: %s\n", prepared.Result.Node)
	fmt.Fprintf(errW, "  Workload: %s\n", workloadClass)
	fmt.Fprintf(errW, "  Fit score: %d/100\n", prepared.Result.FitScore)
	fmt.Fprintf(errW, "  Reservation headroom: %dMB\n", prepared.ReservationMB)
	fmt.Fprintf(errW, "  Locality: %s\n", locality)
	fmt.Fprintf(errW, "  Command: %s\n", prepared.Command)
	fmt.Fprint(errW, "Proceed? [y/N]: ")

	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func printDryRunPlan(prepared execution.PreparedExecution) error {
	fmt.Println(ui.Bold("=== DRY RUN - Execution Plan ==="))
	fmt.Println()
	fmt.Printf("  Node: %s\n", prepared.Result.Node)
	fmt.Printf("  Mode: %s\n", prepared.Result.Mode)
	fmt.Printf("  Intent: %s\n", prepared.Result.Intent)
	fmt.Printf("  Command: %s\n", prepared.Command)
	fmt.Printf("  Fit score: %d/100\n", prepared.Result.FitScore)
	fmt.Printf("  Locality: %s\n", localityLabel(prepared.Result.IsLocal))
	fmt.Printf("  Reservation: %dMB\n", prepared.ReservationMB)
	if prepared.Result.Tool != "" {
		fmt.Printf("  Tool: %s\n", prepared.Result.Tool)
	}
	if prepared.Requirements.Workload.Class != "" {
		fmt.Printf("  Workload: %s\n", prepared.Requirements.Workload.Class)
	}
	if len(prepared.Result.Reasoning) > 0 {
		fmt.Println()
		fmt.Printf("  %s\n", ui.Dim("Reasoning:"))
		for _, r := range prepared.Result.Reasoning {
			fmt.Printf("    %s %s\n", ui.Dim("-"), r)
		}
	}
	if len(prepared.ExtraEnv) > 0 {
		fmt.Println()
		fmt.Printf("  %s\n", ui.Dim("Extra environment:"))
		for _, env := range prepared.ExtraEnv {
			fmt.Printf("    %s\n", env)
		}
	}
	fmt.Println()
	fmt.Println(ui.Dim("Nothing was executed. Remove --dry-run to run."))
	return nil
}

func localityLabel(isLocal bool) string {
	if isLocal {
		return "local"
	}
	return "remote"
}

func printBlockedResult(resp execution.GuardedExecutionResult) {
	fmt.Printf("\n=== SAFETY BLOCKED ===\n")
	fmt.Printf("Reason: %s\n", resp.BlockReason)
	fmt.Printf("Safety score: %d/100\n", resp.DumbScore)
	fmt.Println("Nothing was executed. Fix your request.")
}

// === NEW: axis task context <description> — zero-risk token saver ===
func taskContextCmd() *cobra.Command {
	var cached bool
	var cachedOnly bool
	var cacheAddr string
	var format string

	cmd := &cobra.Command{
		Use:   "context [description]",
		Short: "Emit 200-token context block for Gemini/Codex/Copilot/OpenCode",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			desc := args[0]
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cacheRequested := cached || cachedOnly

			snap, source, err := collectStatusSnapshot(
				ctx,
				cacheRequested,
				cachedOnly,
				func(ctx context.Context) (*models.ClusterSnapshot, string, error) {
					return fetchTaskSnapshot(ctx, cacheAddr)
				},
				loadTaskLiveSnapshot,
			)
			if err != nil {
				Fatal(ExitErrConfigLoad, "Failed to load snapshot: %v", err)
			}

			reqs := placement.InferRequirements(desc)

			st, _ := state.Load()
			skillStore, _ := skills.Load()

			if format == "json" {
				out := buildContextJSON(snap, reqs, desc, source, st, skillStore)
				return printOutput(out, "json")
			}
			printContextBlock(snap, reqs, desc, source, st, skillStore)
			return nil
		},
	}
	cmd.Flags().BoolVar(&cached, "cached", false, "Use the local daemon snapshot cache when available")
	cmd.Flags().BoolVar(&cachedOnly, "cached-only", false, "Require daemon cache; fail instead of falling back to live discovery")
	cmd.Flags().StringVar(&cacheAddr, "cache-addr", api.DefaultAddr(), "Address of the local AXIS API daemon cache (Unix socket or TCP host:port)")
	cmd.Flags().StringVar(&format, "format", "text", "Output format: text or json")
	return cmd
}

func printContextBlock(snap *models.ClusterSnapshot, reqs models.TaskRequirements, task, source string, st *state.ClusterState, skillStore *skills.Store) {
	fmt.Println(buildContextBlock(snap, reqs, task, source, st, skillStore))
}

// ContextOutput is the structured JSON form of the context block — suitable
// for programmatic injection into LLM system prompts.
type ContextOutput struct {
	Node             string    `json:"node"`
	FitScore         int       `json:"fit_score"`
	RAMFreeMB        int64     `json:"ram_free_mb"`
	RAMReservableMB  int64     `json:"ram_reservable_mb,omitempty"`
	RAMAllocatableMB int64     `json:"ram_allocatable_mb,omitempty"`
	Pressure         string    `json:"pressure"`
	Tools            []string  `json:"tools"`
	RecentDecisions  []string  `json:"recent_decisions,omitempty"`
	Skills           []string  `json:"skills,omitempty"`
	Source           string    `json:"source"`
	Task             string    `json:"task"`
	GeneratedAt      time.Time `json:"generated_at"`
}

func buildContextJSON(snap *models.ClusterSnapshot, reqs models.TaskRequirements, task, source string, st *state.ClusterState, skillStore *skills.Store) ContextOutput {
	out := ContextOutput{
		Source:      sourceOrLive(source),
		Task:        task,
		GeneratedAt: time.Now().UTC(),
	}
	if snap == nil || len(snap.Nodes) == 0 {
		return out
	}
	best, ok := selectContextNode(snap.Nodes, reqs)
	if !ok {
		return out
	}
	out.Node = best.Name
	if best.Resources != nil {
		out.RAMFreeMB = best.Resources.RAMFreeMB
		out.RAMReservableMB = best.Resources.ReservableRAM()
		out.RAMAllocatableMB = best.Resources.RAMAllocatableMB
		out.Pressure = best.Resources.Pressure
	}
	out.Tools = toolsList(best)
	var clusterState *state.ClusterState
	if st != nil {
		clusterState = st
	}
	out.FitScore = placement.ComputeFitScore(best, models.IsLocalNode(best), clusterState)

	if st != nil {
		last := st.Decisions
		if len(last) > 5 {
			last = last[len(last)-5:]
		}
		out.RecentDecisions = last
	}
	if skillStore != nil {
		for _, s := range skillStore.Skills {
			out.Skills = append(out.Skills, s.Description)
		}
	}
	return out
}

func buildContextBlock(snap *models.ClusterSnapshot, reqs models.TaskRequirements, task, source string, st *state.ClusterState, skillStore *skills.Store) string {
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
		reservable := best.Resources.ReservableRAM()
		if best.Resources.RAMReservedMB > 0 || best.Resources.RAMAllocatableMB > 0 {
			ramSummary = fmt.Sprintf("%dMB allocatable (%dMB reserved of %dMB reservable)", best.Resources.RAMAllocatableMB, best.Resources.RAMReservedMB, reservable)
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

	// Recent placement decisions give the LLM cluster history context.
	if st != nil && len(st.Decisions) > 0 {
		last := st.Decisions
		if len(last) > 5 {
			last = last[len(last)-5:]
		}
		extraLines += "\n- Recent placements: " + strings.Join(last, " | ")
	}

	// Learned skills tell the LLM what tasks have been validated on this cluster.
	if skillStore != nil && len(skillStore.Skills) > 0 {
		names := make([]string, 0, len(skillStore.Skills))
		for _, s := range skillStore.Skills {
			names = append(names, s.Description)
		}
		if len(names) > 5 {
			names = names[:5]
		}
		extraLines += "\n- Known skills: " + strings.Join(names, ", ")
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
	totalReservable := snap.Summary.TotalReservableMB
	if totalReservable <= 0 {
		for _, node := range snap.Nodes {
			totalReservable += node.Resources.ReservableRAM()
		}
	}
	if snap.Summary.TotalAllocatableMB > 0 || snap.Summary.TotalReservedMB > 0 || totalReservable > 0 {
		return fmt.Sprintf("%d nodes, %dMB allocatable across cluster (%dMB reserved of %dMB reservable)",
			len(snap.Nodes), snap.Summary.TotalAllocatableMB, snap.Summary.TotalReservedMB, totalReservable)
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
	return execution.RemoteExecPrefix(node, contextPath, extraEnv)
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
