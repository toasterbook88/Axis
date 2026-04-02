package agent

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
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
		colorPrefix := ""
		if safetyScore >= 70 {
			colorPrefix = "[HIGH RISK] "
		} else if safetyScore >= 40 {
			colorPrefix = "[CAUTION] "
		}

		fmt.Fprintf(w, "\n%s▶ Agent wants to execute: %s\n", colorPrefix, toolName)
		fmt.Fprintf(w, "  %s\n", description)
		if safetyScore > 0 {
			fmt.Fprintf(w, "  Safety score: %d/100\n", safetyScore)
		}
		fmt.Fprintf(w, "  [y]es / [n]o / [a]lways / ne[v]er: ")

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
	case "axis_status", "axis_facts", "axis_place":
		return true
	}
	return false
}

// StdinConfirm is a convenience for the common case of prompting on stdout/stdin.
func StdinConfirm() ConfirmFunc {
	return DefaultConfirm(os.Stdin, os.Stderr)
}
