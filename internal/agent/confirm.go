package agent

import (
	"bufio"
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
	ConfirmAlways                      // Auto-approve all future calls this session
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

// IsReadOnlyTool returns true for tools that only read cluster state.
func IsReadOnlyTool(name string) bool {
	return isReadOnlyTool(name)
}

// isReadOnlyTool returns true for tools that only read cluster state.
func isReadOnlyTool(name string) bool {
	switch name {
	case "axis_status", "axis_facts", "axis_place", "axis_summary",
		"axis_reservations", "read_file", "list_directory", "grep_search",
		"todo", "symbol_search", "web_fetch", "web_search", "review_changes",
		"remote_read_file", "remote_grep", "remote_list":
		return true
	}
	if strings.HasPrefix(name, "mcp_") {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "read") ||
			strings.Contains(lower, "list") ||
			strings.Contains(lower, "get") ||
			strings.Contains(lower, "recall") ||
			strings.Contains(lower, "status") ||
			strings.Contains(lower, "health") ||
			strings.Contains(lower, "search") ||
			strings.Contains(lower, "show") {
			return true
		}
	}
	return false
}

// StdinConfirm is a convenience for the common case of prompting on stdout/stdin.
func StdinConfirm() ConfirmFunc {
	return DefaultConfirm(os.Stdin, os.Stderr)
}
