package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/safety"
)

// Agent drives a multi-turn tool-calling loop on top of the chat client.
// It is strictly a consumer of the fact plane — its output is never cluster truth.
type Agent struct {
	client   *chat.Client
	conv     *chat.Conversation
	tools    *ToolRegistry
	confirm  ConfirmFunc
	safety   ShellSafetyGate
	output   io.Writer
	maxTurns int

	// autoApproveAll is toggled when the operator selects "always" in confirmation.
	autoApproveAll bool
	// blockAll is toggled when the operator selects "never" in confirmation.
	blockAll bool
}

// Config configures an Agent.
type Config struct {
	Endpoint    string // Ollama endpoint (default: chat.DefaultEndpoint)
	Model       string // Ollama model name
	MaxTurns    int    // Maximum agent loop iterations (default: 10)
	MaxTokens   int    // Conversation token budget (default: 4096)
	AutoApprove bool   // Auto-approve safe commands (score < 70)
	SystemExtra string // Extra text appended to system prompt

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

	client := chat.NewClient(cfg.Endpoint, cfg.Model)
	conv := chat.NewConversation(cfg.MaxTokens)

	// Build system prompt.
	sysPrompt := chat.BuildSystemPrompt(cfg.Cluster, cfg.SystemExtra)
	sysPrompt += "\n\nYou have access to tools. When you need cluster data, use the tools rather than guessing. " +
		"If a tool call fails, read the error and try a corrected call. " +
		"Never fabricate tool results.\n"
	conv.Append(chat.Message{Role: chat.RoleSystem, Content: sysPrompt})

	// Build tool registry.
	tc := cfg.ToolContext
	if tc == nil {
		tc = &ToolContext{}
	}
	tools := NewToolRegistry(tc)

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

	return &Agent{
		client:   client,
		conv:     conv,
		tools:    tools,
		confirm:  confirm,
		safety:   safetyGate,
		output:   cfg.Output,
		maxTurns: cfg.MaxTurns,
	}
}

// Run executes one full agent turn: the user prompt goes in, the agent loops
// through tool calls until the model produces a text response or hits the
// turn limit.
func (a *Agent) Run(ctx context.Context, userPrompt string) error {
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: userPrompt})

	for turn := 0; turn < a.maxTurns; turn++ {
		msgs := a.conv.Messages()
		toolDefs := a.tools.Defs()

		// Stream the model response.
		resp, err := a.client.ChatStream(ctx, msgs, toolDefs, a.output)
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
			result, err := a.dispatchToolCall(ctx, tc)
			if err != nil {
				// Feed the error back to the model for self-correction.
				errMsg := fmt.Sprintf("Error executing tool %q: %s", tc.Function.Name, err.Error())
				fmt.Fprintf(a.output, "\n⚠ %s\n", errMsg)
				a.conv.Append(chat.Message{
					Role:    chat.RoleTool,
					Content: errMsg,
				})
				continue
			}

			fmt.Fprintf(a.output, "\n✓ %s returned %d chars\n", tc.Function.Name, len(result))
			a.conv.Append(chat.Message{
				Role:    chat.RoleTool,
				Content: result,
			})
		}
	}

	fmt.Fprintf(a.output, "\n⚠ Agent reached maximum turns (%d). Stopping.\n", a.maxTurns)
	return nil
}

// dispatchToolCall handles a single tool call with safety gating and confirmation.
func (a *Agent) dispatchToolCall(ctx context.Context, tc chat.ToolCall) (string, error) {
	name := tc.Function.Name
	args := tc.Function.Arguments

	// 1. Check if tool exists.
	if !a.tools.HasTool(name) {
		return "", fmt.Errorf("unknown tool %q — available tools: %s", name, a.toolNames())
	}

	// 2. Validate JSON arguments.
	if len(args) > 0 && !json.Valid(args) {
		return "", fmt.Errorf("malformed JSON arguments for tool %q: %s", name, string(args))
	}

	// 3. Special handling for shell commands.
	if name == "run_shell" {
		return a.dispatchShell(ctx, args)
	}

	// 4. Read-only tools execute directly (no confirmation needed).
	fmt.Fprintf(a.output, "\n▶ Executing: %s\n", name)
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
	allowed, reason := a.safety(sa.Command)
	if !allowed {
		return "", fmt.Errorf("command blocked by safety gate: %s", reason)
	}

	// Compute safety score for confirmation display.
	safetyScore := 0
	if a.safety != nil {
		result := safety.Check(nil, sa.Command, nil)
		safetyScore = result.Score
	}

	// Confirmation (unless auto-approved or session-level always).
	if !a.autoApproveAll {
		decision := a.confirm("run_shell", sa.Command, safetyScore)
		switch decision {
		case ConfirmNo:
			return "", fmt.Errorf("operator declined to execute: %s", sa.Command)
		case ConfirmAlways:
			a.autoApproveAll = true
		case ConfirmNever:
			a.blockAll = true
			return "", fmt.Errorf("operator has blocked all shell commands for this session")
		case ConfirmYes:
			// proceed
		}
	}

	fmt.Fprintf(a.output, "\n▶ Executing shell: %s\n", sa.Command)
	return ExecuteShell(ctx, sa.Command)
}

func (a *Agent) toolNames() string {
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
