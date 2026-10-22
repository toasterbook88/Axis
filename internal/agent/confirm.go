package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/toasterbook88/axis/internal/ui"
)

// ConfirmResult is the operator's response to a tool-execution prompt.
type ConfirmResult int

const (
	ConfirmYes    ConfirmResult = iota // Execute this one time
	ConfirmNo                          // Skip this tool call
	ConfirmAlways                      // Auto-approve future calls of this tool this session
	ConfirmNever                       // Block all future calls this session
)

// ConfirmFunc asks the operator for confirmation before executing a tool call.
// It receives the tool name, a human-readable description of what will happen,
// and the safety score (0-100). It returns the operator's decision.
type ConfirmFunc func(toolName, description string, safetyScore int) ConfirmResult

// DefaultConfirm reads from stdin, writing the prompt to w.
func DefaultConfirm(r io.Reader, w io.Writer) ConfirmFunc {
	return func(toolName, description string, safetyScore int) ConfirmResult {
		boldTool := ui.Bold(toolName)
		boldRisk := ""
		if safetyScore >= 70 {
			boldRisk = ui.Red(" [HIGH RISK]")
		} else if safetyScore >= 40 {
			boldRisk = ui.Yellow(" [CAUTION]")
		}

		fmt.Fprintf(w, "\n%s %s%s\n", ui.Cyan("▶"), ui.Bold("Agent wants to execute:"), boldRisk)
		fmt.Fprintf(w, "  Tool: %s\n", boldTool)
		if safetyScore > 0 {
			fmt.Fprintf(w, "  Safety Score: %d/100\n", safetyScore)
		}
		fmt.Fprintf(w, "  Details:\n")
		// Indent the description lines
		lines := strings.Split(description, "\n")
		for _, l := range lines {
			fmt.Fprintf(w, "    %s\n", l)
		}
		fmt.Fprintf(w, "\n  %s (%s/%s/%s/%s): ",
			ui.Bold("Confirm action?"),
			ui.Green("y")+"es",
			ui.Red("n")+"o",
			ui.Yellow("a")+"lways",
			"ne"+ui.Dim("v")+"er",
		)

		scanner := bufio.NewScanner(r)
		if scanner.Scan() {
			switch strings.TrimSpace(strings.ToLower(scanner.Text())) {
			case "y", "yes":
				return ConfirmYes
			case "a", "always":
				return ConfirmAlways
			case "v", "never":
				return ConfirmNever
			default:
				return ConfirmNo
			}
		}
		return ConfirmNo
	}
}

// AutoApproveConfirm returns a ConfirmFunc that auto-approves read-only tools
// and tools with safety score below the threshold, but still prompts for
// anything risky.
func AutoApproveConfirm(threshold int, fallback ConfirmFunc) ConfirmFunc {
	return func(toolName, description string, safetyScore int) ConfirmResult {
		if toolName == "axis_run_task" {
			return fallback(toolName, description, safetyScore)
		}
		// Read-only AXIS tools never need confirmation.
		if isReadOnlyTool(toolName) {
			return ConfirmYes
		}
		// Low-risk commands auto-approve.
		if safetyScore < threshold {
			return ConfirmYes
		}
		// High-risk: delegate to fallback (usually interactive).
		return fallback(toolName, description, safetyScore)
	}
}

// isReadOnlyTool returns true for tools that only read cluster state.
func isReadOnlyTool(name string) bool {
	switch name {
	case "axis_status", "axis_facts", "axis_models", "axis_place", "axis_summary",
		"axis_reservations", "read_file", "list_directory", "grep_search",
		"todo", "symbol_search", "web_fetch", "web_search", "review_changes",
		"remote_read_file", "remote_grep", "remote_list", "remote_tail_logs",
		"check_task", "list_background_tasks",
		"branch_session", "rollback_session":
		return true
	}
	// External MCP tools are never read-only by name: a server can call a
	// mutating tool anything. They skip confirmation only when the operator
	// lists them in mcp_servers.<server>.auto_approve_tools (see
	// ToolRegistry.mcpAutoApproved).
	return false
}

// StdinConfirm is a convenience for the common case of prompting on stdout/stdin.
func StdinConfirm() ConfirmFunc {
	return DefaultConfirm(os.Stdin, os.Stderr)
}

// approveForSession records an "always" answer for one tool. The caller holds
// dispatchMu.
func (a *Agent) approveForSession(tool string) {
	if a.sessionApproved == nil {
		a.sessionApproved = make(map[string]bool)
	}
	a.sessionApproved[tool] = true
}

// sessionApprovalsCopy returns a copy of the per-tool session approvals for a
// sub-agent.
func (a *Agent) sessionApprovalsCopy() map[string]bool {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if len(a.sessionApproved) == 0 {
		return nil
	}
	out := make(map[string]bool, len(a.sessionApproved))
	for k, v := range a.sessionApproved {
		out[k] = v
	}
	return out
}

type toolCallIDKey struct{}

// withToolCallID tags ctx with the tool call being dispatched.
func withToolCallID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, toolCallIDKey{}, id)
}

// toolCallIDFrom returns the tool call ID set by withToolCallID, or "".
func toolCallIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(toolCallIDKey{}).(string)
	return id
}
