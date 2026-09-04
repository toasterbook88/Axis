package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/events"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/git"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
	"github.com/toasterbook88/axis/internal/ui"
)

// maxParallelTools caps the number of tool calls dispatched concurrently
// within a single agent turn. Prevents fan-out from overwhelming local
// resources (file handles, SSH connections, subprocesses).
const maxParallelTools = 6

// TaskRequest represents the arguments for a cluster task execution.
type TaskRequest struct {
	Description     string `json:"description"`
	Mode            string `json:"mode,omitempty"`
	TargetNode      string `json:"target_node,omitempty"`
	MemoryRequestMB int64  `json:"memory_request_mb,omitempty"`
	MemoryMaxMB     int64  `json:"memory_max_mb,omitempty"`
	ExposePorts     string `json:"expose_ports,omitempty"`
}

// TaskRunner executes an approved cluster task.
type TaskRunner func(ctx context.Context, prepared execution.PreparedExecution) (string, error)

// Agent drives a multi-turn tool-calling loop on top of the chat client.
// It is strictly a consumer of the fact plane — its output is never cluster truth.
type Agent struct {
	client  ChatBackend
	conv    *chat.Conversation
	tools   *ToolRegistry
	confirm ConfirmFunc
	// baseConfirm is the surface-installed confirm without autonomy
	// wrapping. SetAutonomy re-wraps this instead of StdinConfirm so a
	// raw-mode surface's confirm survives mode changes.
	baseConfirm             ConfirmFunc
	runShell                ShellRunner
	runOnNode               NodeShellRunner
	runTask                 TaskRunner
	safety                  ShellSafetyGate
	output                  io.Writer
	observer                Observer
	runMu                   sync.Mutex
	maxTurns                int
	maxTokens               int
	verbose                 bool
	dryRun                  bool
	toolContext             *ToolContext
	model                   string
	allowRawCommandEvidence bool
	securityClass           BackendSecurityClass
	// autoApproveAll is toggled when the operator selects "always" in confirmation.
	autoApproveAll bool
	// blockAll is toggled when the operator selects "never" in confirmation.
	blockAll    bool
	mcpRegistry *mcpclient.Registry
	// dispatchMu serializes operator confirmation prompts and the shared
	// autoApproveAll/blockAll state across concurrent tool dispatches. The
	dispatchMu sync.Mutex
	// runnerMu protects runShell/runOnNode against concurrent /model refresh
	// while tool dispatch and background launches read them.
	runnerMu sync.RWMutex
	// subAgentDepth tracks nesting of spawn_subagent calls to prevent runaway
	// recursion. The root agent is depth 0; children inherit depth+1.
	subAgentDepth int
	// backgroundTasks tracks async commands started by run_background so the
	// agent can poll them with check_task without blocking the loop.
	backgroundTasks *backgroundTaskStore
	// branchStack holds session branch snapshots for branch_session /
	// rollback_session, letting the model try risky approaches and rewind.
	branchStack []branchSnapshot
	// autonomy tracks the active autonomy policy for the session.
	autonomy AutonomyMode
}

// Config configures an Agent.
type Config struct {
	Endpoint                string               // Ollama endpoint (default: chat.DefaultEndpoint)
	Model                   string               // Ollama model name
	Backend                 ChatBackend          // Optional custom backend override
	MaxTurns                int                  // Maximum agent loop iterations (default: 10)
	MaxTokens               int                  // Conversation token budget (default: 4096)
	AutoApprove             bool                 // Auto-approve safe commands (score < 70)
	Autonomy                AutonomyMode         // Autonomy mode: default, edit, or full (controls auto-approval breadth)
	SystemExtra             string               // Extra text appended to system prompt
	Verbose                 bool                 // Emit trace output for tool calls and turns
	DryRun                  bool                 // Plan tool calls without executing them
	ShellTimeout            time.Duration        // Timeout for run_shell commands (default: 5m)
	AllowRawCommandEvidence bool                 // Include raw command text in local evidence
	BackendSecurityClass    BackendSecurityClass // Local or remote backend trust classification
	// Cluster is optional. If non-nil, the agent injects a cluster summary
	// into the system prompt and uses it for safety checks.
	Cluster *chat.ClusterSummaryForPrompt

	// Knowledge is optional. Used for safety gating shell commands.
	Knowledge *knowledge.ClusterKnowledge

	// Snapshot and State are used to initialize tools.
	ToolContext *ToolContext

	// Output is where the agent writes assistant text and traces.
	Output io.Writer

	// Observer, when non-nil, receives structured loop events (tool calls,
	// results, turns, shell execs) instead of having them formatted to
	// Output. Surfaces that render their own transcript set this; the plain
	// CLI leaves it nil. See observer.go.
	Observer Observer

	// Confirm is the confirmation function. If nil, StdinConfirm() is used.
	Confirm ConfirmFunc

	// RunShell executes an approved shell command. If nil, the agent falls back
	// to a direct local shell helper.
	RunShell ShellRunner

	// RunOnNode executes an approved shell command on a named cluster node.
	// If nil, run_on_node fails with a configuration error (no raw SSH bypass).
	RunOnNode NodeShellRunner

	// RunTask executes an approved remote/cluster task.
	RunTask TaskRunner

	// MCPRegistry is a connected registry of external MCP servers (optional).
	MCPRegistry *mcpclient.Registry
}

// NodeShellRunner runs an approved command on a named cluster node.
type NodeShellRunner func(ctx context.Context, node, command string) (string, error)

// New creates an Agent from the given configuration.
func New(cfg Config) *Agent {
	if cfg.Endpoint == "" {
		cfg.Endpoint = chat.DefaultEndpoint
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 25
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 32768
	}
	if cfg.Output == nil {
		cfg.Output = io.Discard
	}

	client := cfg.Backend
	if client == nil {
		client = chat.NewClient(cfg.Endpoint, cfg.Model)
	}
	conv := chat.NewConversation(cfg.MaxTokens)

	// Build system prompt.
	sysPrompt := chat.BuildSystemPrompt(cfg.Cluster, cfg.SystemExtra)

	// Inject Git context if available
	if repoState, err := git.GetRepoState("."); err == nil && repoState.IsRepo {
		sysPrompt += fmt.Sprintf("\n\nGit Repository Context:\n- Branch: %s\n- HEAD Commit: %s (Subject: %s)\n", repoState.Branch, repoState.Commit, repoState.Subject)
		if repoState.IsDirty {
			sysPrompt += fmt.Sprintf("- Status: Dirty (%d files changed)\n", repoState.DirtyCount)
		} else {
			sysPrompt += "- Status: Clean\n"
		}
	}

	// Inject nearest AGENTS.md (project rules) when present — truthful workspace
	// context for the operator's repo, subordinate to the Truth Rule.
	if path, content, ok := loadRepoInstructions("."); ok {
		sysPrompt += formatRepoInstructionsBlock(path, content)
	}

	sysPrompt += "\n\nYou have access to tools. When you need cluster data or file operations, use the tools rather than guessing. " +
		"If a tool call fails, read the error and try a corrected call. " +
		"Never fabricate tool results.\n" +
		"\nYou have first-class tools to inspect and modify the workspace directly:\n" +
		"- `read_file` to read the contents of a file.\n" +
		"- `write_file` to create or overwrite a file with new content.\n" +
		"- `edit_file` to replace a specific block of text inside a file (unique by default; set replace_all=true to replace every occurrence).\n" +
		"- `multi_edit` to apply several text replacements to one file in a single call — prefer this over repeated edit_file calls.\n" +
		"- `list_directory` to list directory entries.\n" +
		"- `grep_search` to find a pattern or query recursively within text files.\n" +
		"- `symbol_search` to find symbol definitions (functions/types/consts) by name — Go-aware via AST, generic for other languages.\n" +
		"- `run_shell` to execute a shell command (Layer-4 guarded execution on the local node; returns a guarded JSON result).\n" +
		"- `axis_run_task` to execute a command on the best/targeted cluster node under placement control (Layer-4 guarded).\n" +
		"- `run_on_node` to run a shell command on a specific named cluster node through Layer-4 guarded execution with a requested-node pin (returns a guarded JSON result).\n" +
		"- `remote_read_file` / `remote_grep` / `remote_list` to read files, grep, and list directories on remote cluster nodes (read-only, no confirmation).\n" +
		"- `remote_write_file` to create or update a file on a remote cluster node via SSH (requires confirmation).\n" +
		"- `remote_tail_logs` to tail systemd unit logs or file logs on a remote cluster node (read-only).\n" +
		"- `fleet_exec` to execute a shell command across multiple cluster nodes in parallel (or all nodes) and aggregate results.\n" +
		"- `spawn_subagent` to delegate a focused sub-task to a child agent that runs its own tool loop on a target node (sync or async) — use this to parallelize work across nodes (e.g. tests on worker-1 while a build runs on builder).\n" +
		"- `git_status` to view repository status.\n" +
		"- `git_diff` to view git differences.\n" +
		"- `git_log` to view git commit history.\n" +
		"- `undo_last` to undo the most recent file edit (restores prior content from the session checkpoint).\n" +
		"- `review_changes` to review uncommitted changes the session has made.\n" +
		"- `web_fetch` to fetch a URL and return readable text (docs, issues, articles, endpoints).\n" +
		"- `web_search` to search the web (DuckDuckGo, no API key needed) and return top results.\n" +
		"- `run_background` to start a long-running command (build, tests, training) in the background and get a task id; use `check_task` to poll its output and `list_background_tasks` to see all running tasks. This lets you keep working while long jobs run.\n" +
		"- `branch_session` / `rollback_session` to snapshot the conversation and rewind to it if a risky approach fails (file changes are not auto-reverted — use undo_last for those).\n" +
		"\nFor multi-step work, use the `todo` tool to break the task into a tracked plan and mark progress as you go (ops: init, append, start, done, drop, view). This keeps long tasks organized.\n" +
		"\nExternal capabilities and MCP services are auto-registered. You can invoke any external tool prefixed with `mcp_` (e.g. `mcp_cortex_recall` or `mcp_cortex_remember` to interact with the Cortex shared vector memory).\n"
	conv.Append(chat.Message{Role: chat.RoleSystem, Content: sysPrompt})

	// Build tool registry.
	tc := cfg.ToolContext
	if tc == nil {
		tc = NewToolContext(&RuntimeView{}, nil)
	}
	tools := NewToolRegistry(tc)
	if cfg.MCPRegistry != nil {
		tools.RegisterMCPTools(cfg.MCPRegistry)
	}
	// When Cortex cluster-memory tools are connected, make them first-class:
	// instruct the model to recall before non-trivial work, remember discoveries,
	// lock shared files before mutation, and publish significant events — so
	// multi-agent cluster work stays coherent across sessions and nodes.
	if tools.HasTool("mcp_cortex_recall") || tools.HasTool("mcp_cortex_remember") {
		conv.Append(chat.Message{Role: chat.RoleSystem, Content: cortexMemoryGuidance()})
	}
	// Build confirmation function.
	confirm := cfg.Confirm
	if confirm == nil {
		confirm = StdinConfirm()
	}
	if cfg.AutoApprove {
		confirm = AutoApproveConfirm(70, confirm)
	}
	// Autonomy mode takes precedence over the legacy AutoApprove flag,
	// wrapping the (possibly already-auto-approving) confirm with the
	// mode-specific policy.
	baseConfirm := confirm
	if cfg.Autonomy != "" && cfg.Autonomy != AutonomyDefault {
		confirm = autonomyConfirm(cfg.Autonomy, confirm)
	}

	// Build safety gate.
	safetyGate := DefaultSafetyGate(tc)
	if cfg.ShellTimeout <= 0 {
		cfg.ShellTimeout = 5 * time.Minute
	}
	runShell := cfg.RunShell
	if runShell == nil {
		runShell = ExecuteShellWithTimeout(cfg.ShellTimeout)
	}

	return &Agent{
		client:                  client,
		conv:                    conv,
		tools:                   tools,
		confirm:                 confirm,
		baseConfirm:             baseConfirm,
		runShell:                runShell,
		runOnNode:               cfg.RunOnNode,
		runTask:                 cfg.RunTask,
		safety:                  safetyGate,
		output:                  cfg.Output,
		maxTurns:                cfg.MaxTurns,
		maxTokens:               cfg.MaxTokens,
		verbose:                 cfg.Verbose,
		dryRun:                  cfg.DryRun,
		toolContext:             tc,
		model:                   cfg.Model,
		allowRawCommandEvidence: cfg.AllowRawCommandEvidence,
		securityClass:           cfg.BackendSecurityClass,
		mcpRegistry:             cfg.MCPRegistry,
		backgroundTasks:         newBackgroundTaskStore(),
		autonomy:                cfg.Autonomy,
		observer:                cfg.Observer,
	}
}

// Run executes one full agent turn: the user prompt goes in, the agent loops
// through tool calls until the model produces a text response or hits the
// turn limit.
// SetConfirm replaces the confirmation function. A surface that owns the
// terminal installs its own here: the default reads os.Stdin directly, which
// would corrupt a raw-mode input loop.
//
// It takes both the run lock (so it cannot land midway through a turn) and
// dispatchMu, which guards confirm/baseConfirm against SetAutonomy and
// dispatchToolCall.
func (a *Agent) SetConfirm(fn ConfirmFunc) {
	if fn == nil {
		return
	}
	a.runMu.Lock()
	defer a.runMu.Unlock()
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.baseConfirm = fn
	a.confirm = a.wrapConfirm(fn)
}

// wrapConfirm applies the active autonomy policy to a base confirm.
// Callers must hold a lock covering confirm/autonomy.
func (a *Agent) wrapConfirm(fn ConfirmFunc) ConfirmFunc {
	if a.autonomy != "" && a.autonomy != AutonomyDefault {
		return autonomyConfirm(a.autonomy, fn)
	}
	return fn
}

// Run executes one full agent turn. Calls are serialized: the conversation is
// not safe for concurrent mutation, so a caller that abandons a turn (a
// cancellation the backend never acknowledged) blocks here until that turn
// actually exits rather than racing it.
func (a *Agent) Run(ctx context.Context, userPrompt string) error {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	return a.runLocked(ctx, userPrompt)
}

// SetAutonomy updates the autonomy mode and its confirmation policy,
// re-wrapping the surface-installed base confirm rather than StdinConfirm.
func (a *Agent) SetAutonomy(mode AutonomyMode) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	a.autonomy = mode
	base := a.baseConfirm
	if base == nil {
		base = StdinConfirm()
	}
	a.confirm = autonomyConfirm(mode, base)
}

// RunWithSinks executes one turn against turn-scoped sinks, restoring the
// previous ones when it returns. A surface that stamps events with the turn
// that produced them constructs a fresh observer and writer per turn and
// passes them here; because the swap happens under the same lock as the run,
// an abandoned turn keeps writing to its own sinks and can never be
// misattributed to the turn that replaced it.
//
// Either sink may be nil to leave that one unchanged.
func (a *Agent) RunWithSinks(ctx context.Context, userPrompt string, obs Observer, out io.Writer) error {
	a.runMu.Lock()
	defer a.runMu.Unlock()

	prevObs, prevOut := a.observer, a.output
	if obs != nil {
		a.observer = obs
	}
	if out != nil {
		a.output = out
	}
	defer func() { a.observer, a.output = prevObs, prevOut }()

	return a.runLocked(ctx, userPrompt)
}

func (a *Agent) runLocked(ctx context.Context, userPrompt string) error {
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: userPrompt})

	for turn := 0; turn < a.maxTurns; turn++ {
		if a.verbose {
			a.emitTurnStarted(turn+1, a.maxTurns)
		}
		// Proactively compress older conversation turns before sending context
		// to the model, so long sessions stay within the token budget.
		if err := a.compactContext(ctx); err != nil && a.verbose {
			a.emitCompactionSkipped(err)
		}
		msgs := a.conv.Messages()
		toolDefs := a.tools.Defs()

		// Clone msgs list and dynamically merge evidence into the first system message.
		clonedMsgs := make([]chat.Message, len(msgs))
		copy(clonedMsgs, msgs)

		evidence := a.retrieveEvidence(userPrompt)
		if evidence != "" {
			systemMsgIdx := -1
			for i, m := range clonedMsgs {
				if m.Role == chat.RoleSystem {
					systemMsgIdx = i
					break
				}
			}
			if systemMsgIdx >= 0 {
				if clonedMsgs[systemMsgIdx].Content != "" {
					clonedMsgs[systemMsgIdx].Content += "\n\n" + evidence
				} else {
					clonedMsgs[systemMsgIdx].Content = evidence
				}
			} else {
				clonedMsgs = append([]chat.Message{{Role: chat.RoleSystem, Content: evidence}}, clonedMsgs...)
			}
		}
		// Inject a live cluster snapshot so the agent continuously knows node
		// health, free memory, and resident models — and can adapt placement
		// mid-task (e.g. reroute when a node hits memory pressure).
		if clusterCtx := a.clusterContextSnippet(); clusterCtx != "" {
			systemMsgIdx := -1
			for i, m := range clonedMsgs {
				if m.Role == chat.RoleSystem {
					systemMsgIdx = i
					break
				}
			}
			if systemMsgIdx >= 0 {
				if clonedMsgs[systemMsgIdx].Content != "" {
					clonedMsgs[systemMsgIdx].Content += "\n\n" + clusterCtx
				} else {
					clonedMsgs[systemMsgIdx].Content = clusterCtx
				}
			} else {
				clonedMsgs = append([]chat.Message{{Role: chat.RoleSystem, Content: clusterCtx}}, clonedMsgs...)
			}
		}

		// Stream the model response with code block highlighting.
		cw := NewColorWriter(a.output)
		resp, err := a.client.ChatStream(ctx, clonedMsgs, toolDefs, cw)
		cw.Close()
		if err != nil {
			return fmt.Errorf("chat stream (turn %d): %w", turn, err)
		}

		a.conv.Append(resp)

		// If no tool calls, the model produced a final text answer — done.
		if len(resp.ToolCalls) == 0 {
			return nil
		}

		// Process tool calls. Dry-run calls are handled inline (instant); live
		// calls dispatch concurrently with a bounded worker pool so independent
		// reads/runs proceed in parallel. Results are appended to the conversation
		// in the original tool-call order so tool_call_id alignment is preserved.
		var liveCalls []chat.ToolCall
		for _, tc := range resp.ToolCalls {
			a.emitToolCalled(tc.ID, tc.Function.Name, string(tc.Function.Arguments))
			if a.dryRun {
				a.emitToolSkipped(tc.ID, tc.Function.Name, "dry-run")
				a.conv.Append(chat.Message{
					Role:       chat.RoleTool,
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf("[dry-run] %s execution skipped", tc.Function.Name),
				})
				continue
			}
			liveCalls = append(liveCalls, tc)
		}

		if len(liveCalls) > 0 {
			type toolResult struct {
				result string
				err    error
			}
			results := make([]toolResult, len(liveCalls))
			var outMu sync.Mutex
			var wg sync.WaitGroup
			concurrency := len(liveCalls)
			if concurrency > maxParallelTools {
				concurrency = maxParallelTools
			}
			sem := make(chan struct{}, concurrency)
			for i, tc := range liveCalls {
				wg.Add(1)
				go func(i int, tc chat.ToolCall) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					result, err := a.dispatchToolCall(ctx, tc)
					outMu.Lock()
					if err != nil {
						a.emitToolFailed(tc.ID, tc.Function.Name, err)
					} else {
						a.emitToolSucceeded(tc.ID, tc.Function.Name, formatToolResultSummary(tc.Function.Name, result), len(result))
					}
					outMu.Unlock()
					results[i] = toolResult{result: result, err: err}
				}(i, tc)
			}
			wg.Wait()

			// Append to conversation in original order (tool_call_id alignment).
			for i, tc := range liveCalls {
				if results[i].err != nil {
					errMsg := fmt.Sprintf("Error executing tool %q: %s", tc.Function.Name, results[i].err.Error())
					a.conv.Append(chat.Message{
						Role:       chat.RoleTool,
						ToolCallID: tc.ID,
						Content:    errMsg,
					})
					continue
				}
				a.conv.Append(chat.Message{
					Role:       chat.RoleTool,
					ToolCallID: tc.ID,
					Content:    results[i].result,
				})
			}
		}
	}

	a.emitMaxTurnsReached(a.maxTurns)
	return nil
}

// formatToolResultSummary produces a human-readable one-line summary of a
// tool result for operator feedback.
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

			decision := a.confirm(name, description, 0)
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
		a.dispatchMu.Lock()
		decision := a.confirm("run_shell", promptDesc, safetyScore)
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
		a.dispatchMu.Lock()
		decision := a.confirm("run_on_node", promptDesc, safetyScore)
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
		a.dispatchMu.Lock()
		decision := a.confirm("fleet_exec", promptDesc, safetyScore)
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

	a.dispatchMu.Lock()
	decision := a.confirm("axis_run_task", promptDesc, prepared.Result.DumbScore)
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

func (a *Agent) ToolNames() string {
	names := make([]string, 0, len(a.tools.executors))
	for n := range a.tools.executors {
		names = append(names, n)
	}
	return strings.Join(names, ", ")
}

func (a *Agent) ToolDefs() []chat.ToolDef {
	if a.tools == nil {
		return nil
	}
	return a.tools.defs
}

// Conversation returns the underlying conversation for inspection/testing.
func (a *Agent) Conversation() *chat.Conversation {
	return a.conv
}

// ContextTokens returns the current estimated conversation tokens.
func (a *Agent) ContextTokens() int {
	return a.conv.EstimateTokens()
}

// MaxTokens returns the session token limit.
func (a *Agent) MaxTokens() int {
	return a.maxTokens
}

// MCPRegistry returns the connected MCP client registry.
func (a *Agent) MCPRegistry() *mcpclient.Registry {
	return a.mcpRegistry
}

// SetBackend changes the active LLM backend and its trust classification.
func (a *Agent) SetBackend(client ChatBackend, securityClass BackendSecurityClass) {
	a.client = client
	a.securityClass = securityClass
}

// Backend returns the active LLM backend.
func (a *Agent) Backend() ChatBackend {
	return a.client
}

// Model returns the current active model name.
func (a *Agent) Model() string {
	return a.model
}

// Autonomy returns the current autonomy mode.
func (a *Agent) Autonomy() AutonomyMode {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if a.autonomy == "" {
		return AutonomyDefault
	}
	return a.autonomy
}

// ExecuteToolDirect allows direct tool execution (e.g. from slash commands or harness scripts).
func (a *Agent) ExecuteToolDirect(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if a.tools == nil {
		return "", fmt.Errorf("tool registry unavailable")
	}
	return a.tools.Execute(ctx, name, args)
}

// SetModel updates the current active model name.
func (a *Agent) SetModel(model string) {
	a.model = model
}

// SetRunShell replaces the local shell runner (e.g. after /model so OwnerLabel
// provenance matches the live model on the guarded path).
func (a *Agent) SetRunShell(r ShellRunner) {
	if r == nil {
		return
	}
	a.runnerMu.Lock()
	a.runShell = r
	a.runnerMu.Unlock()
}

// SetRunOnNode replaces the node shell runner (paired with SetRunShell after /model).
func (a *Agent) SetRunOnNode(r NodeShellRunner) {
	a.runnerMu.Lock()
	a.runOnNode = r
	a.runnerMu.Unlock()
}

// shellRunner snapshots the current local shell runner under lock.
func (a *Agent) shellRunner() ShellRunner {
	a.runnerMu.RLock()
	r := a.runShell
	a.runnerMu.RUnlock()
	return r
}

// nodeRunner snapshots the current node shell runner under lock.
func (a *Agent) nodeRunner() NodeShellRunner {
	a.runnerMu.RLock()
	r := a.runOnNode
	a.runnerMu.RUnlock()
	return r
}

// AgentStats holds session statistics.
type AgentStats struct {
	TokensIn  int
	TokensOut int
	Cost      float64
}

// Stats returns the accumulated statistics from the underlying client, if supported.
func (a *Agent) Stats() AgentStats {
	if sp, ok := a.client.(interface {
		Stats() (int, int, float64)
	}); ok {
		in, out, cost := sp.Stats()
		return AgentStats{
			TokensIn:  in,
			TokensOut: out,
			Cost:      cost,
		}
	}
	return AgentStats{}
}

type BackendSecurityClass int

const (
	BackendRemote BackendSecurityClass = iota
	BackendLocal
)

var (
	bearerRegex        = regexp.MustCompile(`(?i)(bearer\s+)[a-zA-Z0-9\-\._~\+\/]+=*`)
	apiKeyHeaderRegex  = regexp.MustCompile(`(?i)(x-api-key:\s*)\S+`)
	authHeaderRegex    = regexp.MustCompile(`(?i)(authorization:\s*)\S+`)
	genericSecretRegex = regexp.MustCompile(`(?i)(key|password|secret|token|passwd|credential)(["':=\s]+)([a-zA-Z0-9\-_]{8,})`)
)

func redactCommandFromDecision(dec string) string {
	var sb strings.Builder
	i := 0
	for i < len(dec) {
		ch := dec[i]
		if ch == '\'' || ch == '"' {
			end := strings.IndexByte(dec[i+1:], ch)
			if end >= 0 {
				sb.WriteByte(ch)
				sb.WriteString("[REDACTED]")
				sb.WriteByte(ch)
				i += 1 + end + 1
				continue
			}
		}
		sb.WriteByte(ch)
		i++
	}
	return sb.String()
}

func sanitizeAndRedactEvidence(payload string) string {
	var sb strings.Builder
	for _, r := range payload {
		if r < 32 && r != '\n' && r != '\t' {
			sb.WriteRune(' ')
		} else {
			sb.WriteRune(r)
		}
	}
	s := sb.String()
	s = bearerRegex.ReplaceAllString(s, "${1}[REDACTED]")
	s = apiKeyHeaderRegex.ReplaceAllString(s, "${1}[REDACTED]")
	s = authHeaderRegex.ReplaceAllString(s, "${1}[REDACTED]")
	s = genericSecretRegex.ReplaceAllString(s, "${1}${2}[REDACTED]")
	return s
}

func truncatePayload(payload string, maxBytes int) string {
	if len(payload) <= maxBytes {
		return payload
	}
	limit := maxBytes - 3
	truncateIdx := 0
	for idx := range payload {
		if idx > limit {
			break
		}
		truncateIdx = idx
	}
	return payload[:truncateIdx] + "..."
}

// clusterContextSnippet returns a compact live cluster snapshot for injection
// into the system prompt each turn, so the agent stays aware of node health,
// free memory, and resident models and can adapt placement mid-task.
func (a *Agent) clusterContextSnippet() string {
	if a.toolContext == nil {
		return ""
	}
	view := a.toolContext.GetView()
	if view == nil || view.Snapshot == nil {
		return ""
	}
	summary := summarizeSnapshot(view.Snapshot)
	if summary == "" {
		return ""
	}
	return "<live_cluster_context>\n" + summary + "\n</live_cluster_context>"
}

func (a *Agent) retrieveEvidence(userPrompt string) string {
	var recentDecisions []string
	if a.toolContext != nil {
		view := a.toolContext.GetView()
		if view != nil && view.State != nil {
			recentDecisions = view.State.Decisions
		}
	}
	if len(recentDecisions) == 0 {
		if st, err := state.Load(); err == nil && st != nil {
			recentDecisions = st.Decisions
		}
	}

	var matchedSkill skills.LearnedSkill
	var matched bool
	if a.toolContext != nil {
		view := a.toolContext.GetView()
		if view != nil && view.Skills != nil {
			matchedSkill, matched = view.Skills.BestMatch(userPrompt)
		}
	}
	if !matched {
		if sk, err := skills.Load(); err == nil && sk != nil {
			matchedSkill, matched = sk.BestMatch(userPrompt)
		}
	}

	if a.securityClass == BackendRemote {
		return a.remoteEvidence(matchedSkill, matched)
	}
	return a.localEvidence(recentDecisions, matchedSkill, matched)
}

func (a *Agent) remoteEvidence(skill skills.LearnedSkill, matched bool) string {
	if !matched {
		return ""
	}
	var b strings.Builder
	b.WriteString("Relevant learned skill matching current query:\n")
	b.WriteString(fmt.Sprintf("- ID: %s\n", skill.ID))
	b.WriteString(fmt.Sprintf("  Success Count: %d\n", skill.SuccessCount))
	b.WriteString(fmt.Sprintf("  Last Used: %s\n", skill.LastUsed.Format(time.RFC3339)))
	if skill.PreferredNode != "" {
		b.WriteString(fmt.Sprintf("  Preferred Node: %s\n", skill.PreferredNode))
	}
	if len(skill.NodeCount) > 0 {
		b.WriteString(fmt.Sprintf("  Node success counts: %s\n", formatNodeCounts(skill.NodeCount)))
	}
	return wrapEvidence(b.String())
}

func formatNodeCounts(counts map[string]int) string {
	nodes := make([]string, 0, len(counts))
	for n := range counts {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	parts := make([]string, 0, len(nodes))
	for _, n := range nodes {
		parts = append(parts, fmt.Sprintf("%s: %d successes", n, counts[n]))
	}
	return strings.Join(parts, ", ")
}

func (a *Agent) localEvidence(recentDecisions []string, skill skills.LearnedSkill, matched bool) string {
	excludeRawCommands := !a.allowRawCommandEvidence

	var decisionsStr []string
	if len(recentDecisions) > 0 {
		last := recentDecisions
		if len(last) > 5 {
			last = last[len(last)-5:]
		}
		for _, dec := range last {
			d := dec
			if excludeRawCommands {
				d = redactCommandFromDecision(d)
			}
			decisionsStr = append(decisionsStr, fmt.Sprintf("- %s", d))
		}
	}

	var b strings.Builder
	if len(decisionsStr) > 0 {
		b.WriteString("Recent placement decisions:\n")
		b.WriteString(strings.Join(decisionsStr, "\n"))
		b.WriteString("\n")
	}
	if matched {
		b.WriteString("Relevant learned skill matching current query:\n")
		b.WriteString(fmt.Sprintf("- Description: %s\n", skill.Description))
		if excludeRawCommands {
			b.WriteString("  Suggested Command: [REDACTED]\n")
		} else {
			b.WriteString(fmt.Sprintf("  Suggested Command: %s\n", skill.Command))
		}
		b.WriteString(fmt.Sprintf("  Success Count: %d\n", skill.SuccessCount))
		b.WriteString(fmt.Sprintf("  Last Used: %s\n", skill.LastUsed.Format(time.RFC3339)))
		if skill.PreferredNode != "" {
			b.WriteString(fmt.Sprintf("  Preferred Node: %s\n", skill.PreferredNode))
		}
		if len(skill.NodeCount) > 0 {
			b.WriteString(fmt.Sprintf("  Node success counts: %s\n", formatNodeCounts(skill.NodeCount)))
		}
	}

	payload := b.String()
	if payload == "" {
		return ""
	}
	sanitizedPayload := sanitizeAndRedactEvidence(payload)
	return wrapEvidence(sanitizedPayload)
}

func wrapEvidence(payload string) string {
	// Wrapper overhead:
	// prefix: "<untrusted_historical_evidence>\n" (31 bytes)
	// suffix: "\n</untrusted_historical_evidence>" (32 bytes)
	// Total overhead: 63 bytes. Max payload: 2048 - 63 = 1985.
	truncatedPayload := truncatePayload(payload, 1985)
	return "<untrusted_historical_evidence>\n" + truncatedPayload + "\n</untrusted_historical_evidence>"
}
