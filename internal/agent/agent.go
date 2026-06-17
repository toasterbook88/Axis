package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/git"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/ui"
)

// Agent drives a multi-turn tool-calling loop on top of the chat client.
// It is strictly a consumer of the fact plane — its output is never cluster truth.
type Agent struct {
	client    ChatBackend
	conv      *chat.Conversation
	tools     *ToolRegistry
	confirm   ConfirmFunc
	runShell  ShellRunner
	safety    ShellSafetyGate
	output    io.Writer
	maxTurns  int
	maxTokens int
	verbose   bool
	dryRun    bool

	// autoApproveAll is toggled when the operator selects "always" in confirmation.
	autoApproveAll bool
	// blockAll is toggled when the operator selects "never" in confirmation.
	blockAll bool
}

// Config configures an Agent.
type Config struct {
	Endpoint     string        // Ollama endpoint (default: chat.DefaultEndpoint)
	Model        string        // Ollama model name
	Backend      ChatBackend   // Optional custom backend override
	MaxTurns     int           // Maximum agent loop iterations (default: 10)
	MaxTokens    int           // Conversation token budget (default: 4096)
	AutoApprove  bool          // Auto-approve safe commands (score < 70)
	SystemExtra  string        // Extra text appended to system prompt
	Verbose      bool          // Emit trace output for tool calls and turns
	DryRun       bool          // Plan tool calls without executing them
	ShellTimeout time.Duration // Timeout for run_shell commands (default: 5m)

	// Cluster is optional. If non-nil, the agent injects a cluster summary
	// into the system prompt and uses it for safety checks.
	Cluster *chat.ClusterSummaryForPrompt

	// Knowledge is optional. Used for safety gating shell commands.
	Knowledge *knowledge.ClusterKnowledge

	// Snapshot and State are used to initialize tools.
	ToolContext *ToolContext

	// Output is where the agent writes assistant text and traces.
	Output io.Writer

	// Confirm is the confirmation function. If nil, StdinConfirm() is used.
	Confirm ConfirmFunc

	// RunShell executes an approved shell command. If nil, the agent falls back
	// to a direct local shell helper.
	RunShell ShellRunner

	// MCPRegistry is a connected registry of external MCP servers (optional).
	MCPRegistry *mcpclient.Registry
}

// New creates an Agent from the given configuration.
func New(cfg Config) *Agent {
	if cfg.Endpoint == "" {
		cfg.Endpoint = chat.DefaultEndpoint
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 10
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 4096
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

	sysPrompt += "\n\nYou have access to tools. When you need cluster data or file operations, use the tools rather than guessing. " +
		"If a tool call fails, read the error and try a corrected call. " +
		"Never fabricate tool results.\n" +
		"\nYou have first-class tools to inspect and modify the workspace directly:\n" +
		"- `read_file` to read the contents of a file.\n" +
		"- `write_file` to create or overwrite a file with new content.\n" +
		"- `edit_file` to replace a specific, unique block of text inside a file.\n" +
		"- `list_directory` to list directory entries.\n" +
		"- `grep_search` to find a pattern or query recursively within text files.\n" +
		"- `run_shell` to execute a shell command.\n" +
		"- `git_status` to view repository status.\n" +
		"- `git_diff` to view git differences.\n" +
		"- `git_log` to view git commit history.\n" +
		"\nExternal capabilities and MCP services are auto-registered. You can invoke any external tool prefixed with `mcp_` (e.g. `mcp_cortex_recall` or `mcp_cortex_remember` to interact with the Cortex shared vector memory).\n"
	conv.Append(chat.Message{Role: chat.RoleSystem, Content: sysPrompt})

	// Build tool registry.
	tc := cfg.ToolContext
	if tc == nil {
		tc = &ToolContext{}
	}
	tools := NewToolRegistry(tc)
	if cfg.MCPRegistry != nil {
		tools.RegisterMCPTools(cfg.MCPRegistry)
	}

	// Build confirmation function.
	confirm := cfg.Confirm
	if confirm == nil {
		confirm = StdinConfirm()
	}
	if cfg.AutoApprove {
		confirm = AutoApproveConfirm(70, confirm)
	}

	// Build safety gate.
	safetyGate := DefaultSafetyGate(cfg.Knowledge)
	if cfg.ShellTimeout <= 0 {
		cfg.ShellTimeout = 5 * time.Minute
	}
	runShell := cfg.RunShell
	if runShell == nil {
		runShell = ExecuteShellWithTimeout(cfg.ShellTimeout)
	}

	return &Agent{
		client:    client,
		conv:      conv,
		tools:     tools,
		confirm:   confirm,
		runShell:  runShell,
		safety:    safetyGate,
		output:    cfg.Output,
		maxTurns:  cfg.MaxTurns,
		maxTokens: cfg.MaxTokens,
		verbose:   cfg.Verbose,
		dryRun:    cfg.DryRun,
	}
}

// Run executes one full agent turn: the user prompt goes in, the agent loops
// through tool calls until the model produces a text response or hits the
// turn limit.
func (a *Agent) Run(ctx context.Context, userPrompt string) error {
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: userPrompt})

	for turn := 0; turn < a.maxTurns; turn++ {
		if a.verbose {
			fmt.Fprintf(a.output, "\n%s\n", ui.Dim(fmt.Sprintf("─── Turn %d/%d ──────────────────────────────────────────────────", turn+1, a.maxTurns)))
		}
		msgs := a.conv.Messages()
		toolDefs := a.tools.Defs()

		// Stream the model response with code block highlighting.
		cw := NewColorWriter(a.output)
		resp, err := a.client.ChatStream(ctx, msgs, toolDefs, cw)
		cw.Close()
		if err != nil {
			return fmt.Errorf("chat stream (turn %d): %w", turn, err)
		}

		a.conv.Append(resp)

		// If no tool calls, the model produced a final text answer — done.
		if len(resp.ToolCalls) == 0 {
			return nil
		}

		// Process each tool call.
		for _, tc := range resp.ToolCalls {
			fmt.Fprintf(a.output, "\n%s Calling %s...\n", ui.Cyan("▶"), ui.Bold(tc.Function.Name))
			if a.verbose && len(tc.Function.Arguments) > 0 {
				fmt.Fprintf(a.output, "  %s Parameters: %s\n", ui.Dim("→"), tc.Function.Arguments)
			}
			if a.dryRun {
				fmt.Fprintf(a.output, "  %s Skipped execution of %s\n", ui.Yellow("[dry-run]"), tc.Function.Name)
				a.conv.Append(chat.Message{
					Role:       chat.RoleTool,
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf("[dry-run] %s execution skipped", tc.Function.Name),
				})
				continue
			}
			result, err := a.dispatchToolCall(ctx, tc)
			if err != nil {
				// Feed the error back to the model for self-correction.
				errMsg := fmt.Sprintf("Error executing tool %q: %s", tc.Function.Name, err.Error())
				fmt.Fprintf(a.output, "  %s %s\n", ui.Red("⚠"), errMsg)
				a.conv.Append(chat.Message{
					Role:       chat.RoleTool,
					ToolCallID: tc.ID,
					Content:    errMsg,
				})
				continue
			}

			// Print a compact summary line instead of raw char count.
			summary := formatToolResultSummary(tc.Function.Name, result)
			fmt.Fprintf(a.output, "%s %s\n", ui.Green("✓"), summary)
			if a.verbose {
				fmt.Fprintf(a.output, "  %s Result: %d chars\n", ui.Dim("←"), len(result))
			}
			a.conv.Append(chat.Message{
				Role:       chat.RoleTool,
				ToolCallID: tc.ID,
				Content:    result,
			})
		}
	}

	fmt.Fprintf(a.output, "\n⚠ Agent reached maximum turns (%d). Stopping.\n", a.maxTurns)
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

	// 3. Special handling for shell commands.
	if name == "run_shell" {
		return a.dispatchShell(ctx, args)
	}

	// 3.5. Confirmation for mutating tools (e.g. write_file, edit_file, or mutating MCP tools).
	if !isReadOnlyTool(name) && !a.autoApproveAll {
		if a.blockAll {
			return "", fmt.Errorf("operator has blocked all tool execution for this session")
		}

		description := fmt.Sprintf("Execute tool %s with arguments: %s", name, string(args))
		if name == "write_file" {
			var wArgs struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(args, &wArgs); err == nil && wArgs.Path != "" {
				cleanPath, err := validateToolPath(wArgs.Path)
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
			return "", fmt.Errorf("operator declined to execute tool: %s", name)
		case ConfirmAlways:
			a.autoApproveAll = true
		case ConfirmNever:
			a.blockAll = true
			return "", fmt.Errorf("operator has blocked all tool execution for this session")
		case ConfirmYes:
			// proceed
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
	if a.blockAll {
		return "", fmt.Errorf("operator has blocked all shell commands for this session")
	}

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
	if !a.autoApproveAll || forceConfirm {
		promptDesc := sa.Command
		if sa.Cwd != "" {
			promptDesc = fmt.Sprintf("[in %s] %s", sa.Cwd, sa.Command)
		}
		if forceConfirm {
			promptDesc = fmt.Sprintf("[OVERRIDE SAFETY - BLOCKED REASON: %s] %s", reason, promptDesc)
		}
		decision := a.confirm("run_shell", promptDesc, safetyScore)
		switch decision {
		case ConfirmNo:
			if forceConfirm {
				return "", fmt.Errorf("operator declined to execute safety-blocked command: %s (safety block: %s)", sa.Command, reason)
			}
			return "", fmt.Errorf("operator declined to execute: %s", sa.Command)
		case ConfirmAlways:
			if !forceConfirm {
				a.autoApproveAll = true
			}
		case ConfirmNever:
			a.blockAll = true
			return "", fmt.Errorf("operator has blocked all shell commands for this session")
		case ConfirmYes:
			// proceed
		}
	}

	cmdToRun := sa.Command
	if sa.Cwd != "" {
		cleanCwd, err := validateToolPath(sa.Cwd)
		if err != nil {
			return "", err
		}
		cmdToRun = fmt.Sprintf("cd %s && %s", cleanCwd, sa.Command)
	}

	if sa.Cwd != "" {
		fmt.Fprintf(a.output, "\n▶ Executing shell (in %s): %s\n", sa.Cwd, sa.Command)
	} else {
		fmt.Fprintf(a.output, "\n▶ Executing shell: %s\n", sa.Command)
	}
	return a.runShell(ctx, cmdToRun)
}

func (a *Agent) ToolNames() string {
	names := make([]string, 0, len(a.tools.executors))
	for n := range a.tools.executors {
		names = append(names, n)
	}
	return strings.Join(names, ", ")
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

// SetBackend changes the active LLM backend.
func (a *Agent) SetBackend(client ChatBackend) {
	a.client = client
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
