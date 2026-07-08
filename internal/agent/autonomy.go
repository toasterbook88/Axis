package agent

import "fmt"

// AutonomyMode controls how much the agent does without operator prompts.
type AutonomyMode string

const (
	// AutonomyDefault prompts for every mutating action (file edits, shell,
	// remote execution). Read-only tools are never prompted. This is the
	// safest interactive mode.
	AutonomyDefault AutonomyMode = "default"
	// AutonomyEdit auto-approves file edits (write_file, edit_file, multi_edit)
	// without prompting, but still prompts for shell and remote execution
	// unless the safety score is low. Good for focused coding sessions where
	// you trust the edits but want to vet commands.
	AutonomyEdit AutonomyMode = "edit"
	// AutonomyFull auto-approves everything except safety-gate-blocked commands
	// (score >= 80). The Structured Safety Engine is the only barrier. Use for
	// unattended / long-running autonomous work.
	AutonomyFull AutonomyMode = "full"
)

// autonomySafetyThreshold is the score at and above which a command is
// considered safety-blocked and always prompts, even in full autonomy.
const autonomySafetyThreshold = 80

// autonomyLowRiskThreshold is the score below which a command is low-risk and
// auto-approved in edit autonomy (for shell/remote tools).
const autonomyLowRiskThreshold = 70

// isFileEditTool reports whether a tool mutates local files (and so is
// auto-approved in edit autonomy).
func isFileEditTool(name string) bool {
	switch name {
	case "write_file", "edit_file", "multi_edit":
		return true
	}
	return false
}

// ParseAutonomyMode parses an autonomy mode string, returning an error for
// unknown values.
func ParseAutonomyMode(s string) (AutonomyMode, error) {
	switch AutonomyMode(s) {
	case AutonomyDefault, AutonomyEdit, AutonomyFull:
		return AutonomyMode(s), nil
	case "":
		return AutonomyDefault, nil
	}
	return AutonomyDefault, fmt.Errorf("unknown autonomy mode %q (want default, edit, or full)", s)
}

// autonomyConfirm returns a ConfirmFunc implementing the given autonomy mode.
// Read-only tools are always auto-approved. The fallback is used for any case
// the mode chooses to prompt (so the operator still gets a say in interactive
// sessions).
func autonomyConfirm(mode AutonomyMode, fallback ConfirmFunc) ConfirmFunc {
	return func(toolName, description string, safetyScore int) ConfirmResult {
		// Read-only tools never need confirmation, regardless of mode.
		if isReadOnlyTool(toolName) {
			return ConfirmYes
		}
		switch mode {
		case AutonomyFull:
			// Auto-approve everything except safety-blocked commands.
			if safetyScore < autonomySafetyThreshold {
				return ConfirmYes
			}
			return fallback(toolName, description, safetyScore)
		case AutonomyEdit:
			// Auto-approve file edits without prompting.
			if isFileEditTool(toolName) {
				return ConfirmYes
			}
			// Auto-approve low-risk shell/remote commands.
			if safetyScore < autonomyLowRiskThreshold {
				return ConfirmYes
			}
			return fallback(toolName, description, safetyScore)
		default: // AutonomyDefault
			return fallback(toolName, description, safetyScore)
		}
	}
}
