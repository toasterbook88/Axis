package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/events"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/ui"
)

func formatToolResultSummary(toolName, result string) string {
	switch toolName {
	case "axis_status":
		// Extract first line (cluster summary).
		if i := strings.Index(result, "\n"); i > 0 {
			return toolName + ": " + strings.TrimSpace(result[:i])
		}
	case "axis_summary":
		return toolName + ": " + strings.TrimSpace(result)
	case "axis_facts":
		if i := strings.Index(result, "\n"); i > 0 {
			return toolName + ": " + strings.TrimSpace(result[:i])
		}
	case "axis_place":
		return toolName + ": " + strings.TrimSpace(result)
	case "axis_reservations":
		if strings.Contains(result, "Active reservations") {
			lines := strings.Split(result, "\n")
			if len(lines) >= 2 {
				count := 0
				for _, l := range lines[1:] {
					if strings.HasPrefix(l, "-") {
						count++
					}
				}
				return fmt.Sprintf("%s: found %d nodes with active reservations", toolName, count)
			}
		}
		return toolName + ": no active reservations"
	case "read_file":
		lines := strings.Count(result, "\n")
		return fmt.Sprintf("%s: read %d lines (%d chars)", toolName, lines, len(result))
	case "write_file":
		return fmt.Sprintf("%s: wrote file", toolName)
	case "edit_file":
		return fmt.Sprintf("%s: edited file", toolName)
	case "grep_search":
		if strings.HasPrefix(result, "No matches") {
			return toolName + ": no matches found"
		}
		lines := strings.Count(result, "\n")
		if result != "" {
			lines++
		}
		return fmt.Sprintf("%s: found %d match(es)", toolName, lines)
	case "list_directory":
		if i := strings.Index(result, "\n"); i > 0 {
			line := result[:i]
			if idx := strings.Index(line, "Directory:"); idx >= 0 {
				return toolName + ": " + strings.TrimSpace(line[idx+len("Directory:"):])
			}
		}
		return toolName + ": listed directory"
	case "run_shell":
		return toolName + ": executed shell command"
	case "git_status":
		if strings.Contains(result, "Branch:") {
			lines := strings.Split(result, "\n")
			return toolName + ": " + lines[0]
		}
		return toolName + ": checked status"
	case "git_diff":
		lines := strings.Count(result, "\n")
		return fmt.Sprintf("%s: generated diff of %d lines", toolName, lines)
	case "git_log":
		lines := strings.Count(result, "\n")
		return fmt.Sprintf("%s: retrieved %d commits", toolName, lines)
	case "fleet_exec":
		return fmt.Sprintf("%s: executed across fleet", toolName)
	case "remote_write_file":
		return fmt.Sprintf("%s: wrote remote file", toolName)
	case "remote_tail_logs":
		return fmt.Sprintf("%s: retrieved remote logs", toolName)
	}
	if strings.HasPrefix(toolName, "mcp_") {
		return fmt.Sprintf("%s: executed successfully (%d chars)", toolName, len(result))
	}
	return fmt.Sprintf("%s returned %d chars", toolName, len(result))
}

// dispatchToolCall handles a single tool call with safety gating and confirmation.
func (a *Agent) dispatchToolCall(ctx context.Context, tc chat.ToolCall) (string, error) {
	name := tc.Function.Name
	args := tc.Function.Arguments

	// 1. Check if tool exists.
	if !a.tools.HasTool(name) {
		return "", fmt.Errorf("unknown tool %q — available tools: %s", name, a.ToolNames())
	}

	// 2. Validate JSON arguments.
	if len(args) > 0 && !json.Valid(args) {
		return "", fmt.Errorf("malformed JSON arguments for tool %q: %s", name, string(args))
	}

	clusterTools := map[string]bool{
		"axis_status":       true,
		"axis_facts":        true,
		"axis_place":        true,
		"axis_summary":      true,
		"axis_reservations": true,
		"axis_run_task":     true,
	}

	if clusterTools[name] && a.toolContext != nil && a.toolContext.Reload != nil {
		a.dispatchMu.Lock()
		err := a.toolContext.ReloadCurrent(ctx)
		a.dispatchMu.Unlock()
		if err != nil {
			return "", fmt.Errorf("failed to refresh cluster runtime context: %w", err)
		}
	}

	// 3. Special handling for shell commands.
	if name == "run_shell" {
		return a.dispatchShell(ctx, args)
	}
	if name == "run_on_node" {
		return a.dispatchRunOnNode(ctx, args)
	}
	if name == "fleet_exec" {
		return a.dispatchFleetExec(ctx, args)
	}
	if name == "axis_run_task" {
		return a.dispatchRunTask(ctx, args)
	}
	if name == "spawn_subagent" {
		return a.dispatchSubagent(ctx, args)
	}
	if name == "run_background" {
		return a.dispatchRunBackground(ctx, args)
	}
	if name == "check_task" {
		return a.dispatchCheckTask(ctx, args)
	}
	if name == "list_background_tasks" {
		return a.listBackgroundTasks(), nil
	}
	if name == "branch_session" {
		var ba struct {
			Label string `json:"label,omitempty"`
		}
		_ = json.Unmarshal(args, &ba)
		return a.branchSession(ba.Label)
	}
	if name == "rollback_session" {
		var ra struct {
			Label string `json:"label,omitempty"`
		}
		_ = json.Unmarshal(args, &ra)
		return a.rollbackSession(ra.Label)
	}

	// 3.5. Confirmation for mutating tools (e.g. write_file, edit_file, or mutating MCP tools).
	// Serialized across concurrent dispatches so operator prompts never interleave
	// and the autoApproveAll/blockAll toggles are race-free. The expensive tool
	// execution below runs unlocked so independent calls stay parallel.
	if !isReadOnlyTool(name) {
		a.dispatchMu.Lock()
		approved := a.autoApproveAll
		blocked := a.blockAll
		a.dispatchMu.Unlock()
		if !approved {
			a.dispatchMu.Lock()
			if blocked || a.blockAll {
				a.dispatchMu.Unlock()
				return "", fmt.Errorf("operator has blocked all tool execution for this session")
			}
			a.dispatchMu.Unlock()

			description := fmt.Sprintf("Execute tool %s with arguments: %s", name, string(args))
			if name == "write_file" {
				var wArgs struct {
					Path    string `json:"path"`
					Content string `json:"content"`
				}
				if err := json.Unmarshal(args, &wArgs); err == nil && wArgs.Path != "" {
					cleanPath, err := validateToolPathForWrite(wArgs.Path)
					if err == nil {
						if info, err := os.Stat(cleanPath); err == nil && !info.IsDir() {
							oldContent, err := os.ReadFile(cleanPath)
							if err == nil {
								diffText := ui.FormatDiff(string(oldContent), wArgs.Content)
								description = fmt.Sprintf("Overwrite file %s\n\nProposed Changes:\n%s", wArgs.Path, diffText)
							}
						} else {
							// New file
							var preview []string
							lines := strings.Split(wArgs.Content, "\n")
							for i, l := range lines {
								if i >= 10 {
									preview = append(preview, ui.DimColor.Sprint("... (truncated)"))
									break
								}
								preview = append(preview, ui.GreenColor.Sprint("+ "+l))
							}
							description = fmt.Sprintf("Create new file %s\n\nProposed Content:\n%s", wArgs.Path, strings.Join(preview, "\n"))
						}
					}
				}
			} else if name == "edit_file" {
				var eArgs struct {
					Path               string `json:"path"`
					TargetContent      string `json:"target_content"`
					ReplacementContent string `json:"replacement_content"`
				}
				if err := json.Unmarshal(args, &eArgs); err == nil && eArgs.Path != "" {
					diffText := ui.FormatDiff(eArgs.TargetContent, eArgs.ReplacementContent)
					description = fmt.Sprintf("Edit file %s\n\nProposed Changes:\n%s", eArgs.Path, diffText)
				}
			}

			decision := a.askConfirm(name, description, 0)
			a.dispatchMu.Lock()
			switch decision {
			case ConfirmNo:
				a.dispatchMu.Unlock()
				return "", fmt.Errorf("operator declined to execute tool: %s", name)
			case ConfirmAlways:
				a.autoApproveAll = true
				a.dispatchMu.Unlock()
			case ConfirmNever:
				a.blockAll = true
				a.dispatchMu.Unlock()
				return "", fmt.Errorf("operator has blocked all tool execution for this session")
			case ConfirmYes:
				a.dispatchMu.Unlock()
			}
		}
	}

	// 4. Execute tool.
	return a.tools.Execute(ctx, name, args)
}

// dispatchShell handles the run_shell tool with safety gating and confirmation.
func (a *Agent) dispatchShell(ctx context.Context, args json.RawMessage) (string, error) {
	var sa shellArgs
	if err := json.Unmarshal(args, &sa); err != nil {
		return "", fmt.Errorf("invalid arguments for run_shell: expected {\"command\": \"...\"}, got: %s", string(args))
	}
	if sa.Command == "" {
		return "", fmt.Errorf("run_shell requires a non-empty \"command\" argument")
	}

	// Session-level block.
	a.dispatchMu.Lock()
	if a.blockAll {
		a.dispatchMu.Unlock()
		return "", fmt.Errorf("operator has blocked all shell commands for this session")
	}
	a.dispatchMu.Unlock()

	// Safety gate.
	allowed, reason, safetyScore := a.safety(sa.Command)
	forceConfirm := false
	if !allowed {
		forceConfirm = true
		if safetyScore < 80 {
			safetyScore = 80
		}
	}

	// Confirmation (unless auto-approved or session-level always, and not safety-blocked).
	// Serialized across concurrent dispatches.
	a.dispatchMu.Lock()
	needsConfirm := !a.autoApproveAll || forceConfirm
	a.dispatchMu.Unlock()
	if needsConfirm {
		promptDesc := sa.Command
		if sa.Cwd != "" {
			promptDesc = fmt.Sprintf("[in %s] %s", sa.Cwd, sa.Command)
		}
		if forceConfirm {
			promptDesc = fmt.Sprintf("[OVERRIDE SAFETY - BLOCKED REASON: %s] %s", reason, promptDesc)
		}
		decision := a.askConfirm("run_shell", promptDesc, safetyScore)
		a.dispatchMu.Lock()
		switch decision {
		case ConfirmNo:
			a.dispatchMu.Unlock()
			if forceConfirm {
				return "", fmt.Errorf("operator declined to execute safety-blocked command: %s (safety block: %s)", sa.Command, reason)
			}
			return "", fmt.Errorf("operator declined to execute: %s", sa.Command)
		case ConfirmAlways:
			if !forceConfirm {
				a.autoApproveAll = true
			}
			a.dispatchMu.Unlock()
		case ConfirmNever:
			a.blockAll = true
			a.dispatchMu.Unlock()
			return "", fmt.Errorf("operator has blocked all shell commands for this session")
		case ConfirmYes:
			a.dispatchMu.Unlock()
			// proceed
		}
	}

	cmdToRun := sa.Command
	if sa.Cwd != "" {
		cleanCwd, err := validateToolPath(sa.Cwd)
		if err != nil {
			return "", err
		}
		cmdToRun = fmt.Sprintf("cd %s && %s", shellQuote(cleanCwd), sa.Command)
	}

	if sa.Cwd != "" {
		a.emitShellExecuting("", "", sa.Cwd, sa.Command)
	} else {
		a.emitShellExecuting("", "", "", sa.Command)
	}
	runLocal := a.shellRunner()
	if runLocal == nil {
		return "", fmt.Errorf("runShell runner unavailable")
	}
	return runLocal(ctx, cmdToRun)
}

// dispatchRunOnNode handles the run_on_node tool with safety gating and confirmation,
// then delegates to the configured guarded node runner (no raw SSH bypass).
func (a *Agent) dispatchRunOnNode(ctx context.Context, args json.RawMessage) (string, error) {
	var a0 struct {
		Node    string `json:"node"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &a0); err != nil {
		return "", fmt.Errorf("invalid arguments for run_on_node: expected {\"node\": \"...\", \"command\": \"...\"}, got: %s", string(args))
	}
	if strings.TrimSpace(a0.Node) == "" {
		return "", fmt.Errorf("run_on_node requires a non-empty \"node\" argument")
	}
	if strings.TrimSpace(a0.Command) == "" {
		return "", fmt.Errorf("run_on_node requires a non-empty \"command\" argument")
	}
	runNode := a.nodeRunner()
	if runNode == nil {
		return "", fmt.Errorf("run_on_node runner not configured; node commands must use guarded execution")
	}

	a.dispatchMu.Lock()
	if a.blockAll {
		a.dispatchMu.Unlock()
		return "", fmt.Errorf("operator has blocked all tool execution for this session")
	}
	a.dispatchMu.Unlock()

	allowed, reason, safetyScore := a.safety(a0.Command)
	forceConfirm := false
	if !allowed {
		forceConfirm = true
		if safetyScore < 80 {
			safetyScore = 80
		}
	}

	a.dispatchMu.Lock()
	needsConfirm := !a.autoApproveAll || forceConfirm
	a.dispatchMu.Unlock()
	if needsConfirm {
		promptDesc := fmt.Sprintf("[on %s] %s", a0.Node, a0.Command)
		if forceConfirm {
			promptDesc = fmt.Sprintf("[OVERRIDE SAFETY - BLOCKED REASON: %s] %s", reason, promptDesc)
		}
		decision := a.askConfirm("run_on_node", promptDesc, safetyScore)
		a.dispatchMu.Lock()
		switch decision {
		case ConfirmNo:
			a.dispatchMu.Unlock()
			if forceConfirm {
				return "", fmt.Errorf("operator declined to execute safety-blocked command on %s: %s (safety block: %s)", a0.Node, a0.Command, reason)
			}
			return "", fmt.Errorf("operator declined to execute on %s: %s", a0.Node, a0.Command)
		case ConfirmAlways:
			if !forceConfirm {
				a.autoApproveAll = true
			}
			a.dispatchMu.Unlock()
		case ConfirmNever:
			a.blockAll = true
			a.dispatchMu.Unlock()
			return "", fmt.Errorf("operator has blocked all tool execution for this session")
		case ConfirmYes:
			a.dispatchMu.Unlock()
		}
	}

	a.emitShellExecuting("", a0.Node, "", a0.Command)
	return runNode(ctx, a0.Node, a0.Command)
}

// dispatchFleetExec executes a command across multiple cluster nodes in parallel with safety gating.
func (a *Agent) dispatchFleetExec(ctx context.Context, args json.RawMessage) (string, error) {
	var a0 fleetExecArgs
	if err := json.Unmarshal(args, &a0); err != nil {
		return "", fmt.Errorf("invalid arguments for fleet_exec: %w", err)
	}
	if strings.TrimSpace(a0.Command) == "" {
		return "", fmt.Errorf("fleet_exec requires a non-empty \"command\" argument")
	}

	a.dispatchMu.Lock()
	if a.blockAll {
		a.dispatchMu.Unlock()
		return "", fmt.Errorf("operator has blocked all tool execution for this session")
	}
	a.dispatchMu.Unlock()

	// Safety check on command
	allowed, reason, safetyScore := a.safety(a0.Command)
	forceConfirm := false
	if !allowed {
		forceConfirm = true
		if safetyScore < 80 {
			safetyScore = 80
		}
	}

	// Resolve target nodes
	var targetNodes []string
	var allNodes []string
	if a.toolContext != nil {
		view := a.toolContext.GetView()
		if view != nil && view.Snapshot != nil {
			for _, n := range view.Snapshot.Nodes {
				allNodes = append(allNodes, n.Name)
			}
		} else if view != nil && view.Config != nil {
			for _, n := range view.Config.Nodes {
				allNodes = append(allNodes, n.Name)
			}
		}
	}

	isAll := len(a0.Nodes) == 0
	for _, n := range a0.Nodes {
		if strings.EqualFold(strings.TrimSpace(n), "all") {
			isAll = true
			break
		}
	}

	if isAll {
		targetNodes = allNodes
	} else {
		for _, n := range a0.Nodes {
			n = strings.TrimSpace(n)
			if n != "" {
				targetNodes = append(targetNodes, n)
			}
		}
	}

	if len(targetNodes) == 0 {
		return "", fmt.Errorf("fleet_exec: no target nodes found in snapshot or config")
	}

	a.dispatchMu.Lock()
	needsConfirm := !a.autoApproveAll || forceConfirm
	a.dispatchMu.Unlock()
	if needsConfirm {
		promptDesc := fmt.Sprintf("[on %d nodes: %s] %s", len(targetNodes), strings.Join(targetNodes, ", "), a0.Command)
		if forceConfirm {
			promptDesc = fmt.Sprintf("[OVERRIDE SAFETY - BLOCKED REASON: %s] %s", reason, promptDesc)
		}
		decision := a.askConfirm("fleet_exec", promptDesc, safetyScore)
		a.dispatchMu.Lock()
		switch decision {
		case ConfirmNo:
			a.dispatchMu.Unlock()
			if forceConfirm {
				return "", fmt.Errorf("operator declined safety-blocked fleet command: %s (%s)", a0.Command, reason)
			}
			return "", fmt.Errorf("operator declined fleet execution: %s", a0.Command)
		case ConfirmAlways:
			if !forceConfirm {
				a.autoApproveAll = true
			}
			a.dispatchMu.Unlock()
		case ConfirmNever:
			a.blockAll = true
			a.dispatchMu.Unlock()
			return "", fmt.Errorf("operator has blocked all tool execution for this session")
		case ConfirmYes:
			a.dispatchMu.Unlock()
		}
	}

	timeout := 15 * time.Second
	if a0.TimeoutSec > 0 {
		timeout = time.Duration(a0.TimeoutSec) * time.Second
	}

	type nodeResult struct {
		node   string
		output string
		err    error
	}

	results := make([]nodeResult, len(targetNodes))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)

	for i, nodeName := range targetNodes {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			nodeCtx, nodeCancel := context.WithTimeout(ctx, timeout)
			defer nodeCancel()

			var out string
			var err error
			if runNode := a.nodeRunner(); runNode != nil {
				out, err = runNode(nodeCtx, name, a0.Command)
			} else {
				out, err = runRemote(nodeCtx, a.toolContext, name, a0.Command)
			}
			results[idx] = nodeResult{node: name, output: strings.TrimSpace(out), err: err}
		}(i, nodeName)
	}
	wg.Wait()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Fleet Execution Report (Command: `%s`, %d node(s)):\n\n", a0.Command, len(targetNodes)))
	for _, res := range results {
		status := "✓ OK"
		if res.err != nil {
			status = fmt.Sprintf("✗ FAIL (%s)", res.err.Error())
		}
		sb.WriteString(fmt.Sprintf("### Node: %s [%s]\n", res.node, status))
		if res.output != "" {
			sb.WriteString("```\n")
			sb.WriteString(res.output)
			sb.WriteString("\n```\n\n")
		} else {
			sb.WriteString("(no output)\n\n")
		}
	}

	return strings.TrimSpace(sb.String()), nil
}

// dispatchRunTask handles the axis_run_task tool with safety gating and confirmation.
func (a *Agent) dispatchRunTask(ctx context.Context, args json.RawMessage) (string, error) {
	// 1. JSON unmarshal and validation first.
	var rArgs TaskRequest
	if err := json.Unmarshal(args, &rArgs); err != nil {
		return "", fmt.Errorf("invalid arguments for axis_run_task: expected {\"description\": \"...\"}, got: %s", string(args))
	}
	if rArgs.Description == "" {
		return "", fmt.Errorf("axis_run_task requires a non-empty \"description\" argument")
	}

	// 2. Validate runner delegate configuration before safety or confirmation gates.
	if a.runTask == nil {
		return "", fmt.Errorf("runTask runner delegate not configured")
	}

	// 3. Check session-level block.
	a.dispatchMu.Lock()
	if a.blockAll {
		a.dispatchMu.Unlock()
		return "", fmt.Errorf("operator has blocked all tool execution for this session")
	}
	a.dispatchMu.Unlock()

	// 4. Construct context and request for PrepareGuardedExecution.
	view := a.toolContext.GetView()
	if view == nil {
		return "", fmt.Errorf("runtime view is not available")
	}
	rt := &runtimectx.Context{
		Config:   view.Config,
		Snapshot: view.Snapshot,
		State:    view.State,
		Ledger:   view.Ledger,
		Skills:   view.Skills,
	}

	mode := execution.ModeExec
	if strings.ToLower(rArgs.Mode) == "script" {
		mode = execution.ModeScript
	}

	req := execution.GuardedExecutionRequest{
		Description:      rArgs.Description,
		Mode:             mode,
		Confirm:          execution.ConfirmWord,
		RequestedNode:    rArgs.TargetNode,
		MemoryRequestMB:  rArgs.MemoryRequestMB,
		MemoryMaxMB:      rArgs.MemoryMaxMB,
		ExposePorts:      rArgs.ExposePorts,
		OwnerSurface:     execution.OwnerSurfaceAgentRunTask,
		OwnerLabel:       strings.TrimSpace(a.model),
		Events:           events.GuardedExecutionSink{},
		BuildContextJSON: knowledge.ExecutionContextJSON,
	}

	prepared, err := execution.PrepareGuardedExecution(ctx, rt, req)
	if err != nil {
		return "", err
	}

	// 5. Check safety verdict (Authoritative from PrepareGuardedExecution).
	// If denied, terminate immediately before confirmation.
	if prepared.Result.Blocked || prepared.Result.DumbScore >= 80 {
		return "", fmt.Errorf("safety block: task execution blocked (score: %d): %s", prepared.Result.DumbScore, prepared.Result.BlockReason)
	}

	// 6. Confirmation gate (Always required, no automatic/model bypasses).
	descParts := []string{fmt.Sprintf("Run command: %s", prepared.Command)}
	if prepared.Result.Node != "" {
		descParts = append(descParts, fmt.Sprintf("Target Node: %s", prepared.Result.Node))
	}
	if rArgs.MemoryRequestMB > 0 {
		descParts = append(descParts, fmt.Sprintf("Memory Request: %d MB", rArgs.MemoryRequestMB))
	}
	if rArgs.ExposePorts != "" {
		descParts = append(descParts, fmt.Sprintf("Expose Ports: %s", rArgs.ExposePorts))
	}
	promptDesc := strings.Join(descParts, "\n")

	// Risk scores (0 < score < 80) add stronger warning text.
	if prepared.Result.DumbScore > 0 {
		promptDesc = fmt.Sprintf("[WARNING: TASK RISKS DETECTED (Score: %d) - REASON: %s]\n%s", prepared.Result.DumbScore, prepared.Result.BlockReason, promptDesc)
	}

	// Confirm without holding dispatchMu: the operator overlay is the only thing
	// this turn must wait on, and Autonomy() needs the lock to paint/mutate
	// itself while the prompt is open. Same pattern as dispatchShell.
	decision := a.askConfirm("axis_run_task", promptDesc, prepared.Result.DumbScore)
	a.dispatchMu.Lock()
	switch decision {
	case ConfirmNo:
		a.dispatchMu.Unlock()
		return "", fmt.Errorf("operator declined to execute: %s", prepared.Command)
	case ConfirmNever:
		a.blockAll = true
		a.dispatchMu.Unlock()
		return "", fmt.Errorf("operator has blocked all tool execution for this session")
	case ConfirmAlways, ConfirmYes:
		a.dispatchMu.Unlock()
		// Proceed. For axis_run_task, ConfirmAlways does not bypass future run_task prompts.
	}

	// 7. Execute the prepared execution.
	return a.runTask(ctx, prepared)
}
