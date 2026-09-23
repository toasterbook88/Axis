package agent

import "strings"

// ToolScope is the set of tools the model may see and call.
// Observe is the default. Edit adds workspace writes and run_shell.
// Exec is the guarded-exec grant: edit plus axis_run_task.
// Tools in the never set stay registered for safety routes but are
// never advertised and never dispatched.
type ToolScope string

const (
	ScopeObserve ToolScope = "observe"
	ScopeEdit    ToolScope = "edit"
	ScopeExec    ToolScope = "exec"
)

// ScopeFor maps an autonomy mode onto a tool scope.
// default is observe. edit is the explicit edit grant.
// full is the guarded-exec grant.
func ScopeFor(mode AutonomyMode) ToolScope {
	switch mode {
	case AutonomyEdit:
		return ScopeEdit
	case AutonomyFull:
		return ScopeExec
	default:
		return ScopeObserve
	}
}

// DisplayScope is the footer name for a mode. It is observe or edit,
// never a raw autonomy string and never a fleet fraction.
func DisplayScope(mode AutonomyMode) string {
	switch ScopeFor(mode) {
	case ScopeEdit:
		return "edit"
	case ScopeExec:
		return "exec"
	default:
		return "observe"
	}
}

func observeTool(name string) bool {
	switch name {
	case "read_file", "list_directory", "grep_search", "symbol_search", "todo",
		"axis_status", "axis_facts", "axis_place", "axis_summary", "axis_reservations",
		"git_status", "git_diff", "git_log":
		return true
	default:
		return false
	}
}

func editTool(name string) bool {
	switch name {
	case "write_file", "edit_file", "multi_edit", "run_shell":
		return true
	default:
		return false
	}
}

func neverTool(name string) bool {
	switch name {
	case "spawn_subagent", "fleet_exec", "run_on_node", "remote_write_file",
		"remote_tail_logs", "remote_read_file", "remote_grep", "remote_list":
		return true
	default:
		return false
	}
}

// observeDeferred is registered but not a read. Observe must not advertise it.
// undo_last becomes visible with the edit grant. The rest stay hidden.
func observeDeferred(name string) bool {
	switch name {
	case "undo_last", "review_changes", "run_background", "check_task",
		"list_background_tasks", "branch_session", "rollback_session",
		"web_fetch", "web_search":
		return true
	default:
		return false
	}
}

// toolVisible reports whether name is advertised and callable in scope.
// MCP tools are visible when connected, under their real mcp_ names.
func toolVisible(name string, scope ToolScope) bool {
	if name == "" || neverTool(name) {
		return false
	}
	if strings.HasPrefix(name, "mcp_") || observeTool(name) {
		return true
	}
	if observeDeferred(name) {
		return name == "undo_last" && (scope == ScopeEdit || scope == ScopeExec)
	}
	if !editTool(name) && name != "axis_run_task" {
		return true
	}
	switch scope {
	case ScopeEdit:
		return editTool(name)
	case ScopeExec:
		return editTool(name) || name == "axis_run_task"
	default:
		return false
	}
}

// VisibleToolPrompt is the system-prompt tool list for the defs the model
// is being offered. It does not mention tools outside that list.
func VisibleToolPrompt(names []string) string {
	if len(names) == 0 {
		return "Tools you may call right now: none. Do not invent tool names. Footer status is chrome, not cluster inventory."
	}
	var b strings.Builder
	b.WriteString("Tools you may call right now:\n")
	for _, name := range names {
		b.WriteString("- `")
		b.WriteString(name)
		b.WriteString("`\n")
	}
	b.WriteString("Do not call a tool that is not in this list. Footer status is chrome, not cluster inventory.\n")
	return b.String()
}
