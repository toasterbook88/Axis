package agent

import (
	"testing"
)

// Characterization rows for formatToolResultSummary (was 13.0% cover).
// Test-only: every row locks today's output string for one branch.

func TestCharToolSummaryClusterReads(t *testing.T) {
	cases := []struct{ name, tool, result, want string }{
		{"status first line", "axis_status", "line-one\nline-two", "axis_status: line-one"},
		{"status single line falls to fallback", "axis_status", "only-line", "axis_status returned 9 chars"},
		{"summary full", "axis_summary", "  trimmed  ", "axis_summary: trimmed"},
		{"facts first line", "axis_facts", "first\nsecond", "axis_facts: first"},
		{"facts single line falls to fallback", "axis_facts", "only-line", "axis_facts returned 9 chars"},
		{"place full", "axis_place", "  chosen node-x  ", "axis_place: chosen node-x"},
	}
	for _, tc := range cases {
		got := formatToolResultSummary(tc.tool, tc.result)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestCharToolSummaryReservations(t *testing.T) {
	with := formatToolResultSummary("axis_reservations", "Active reservations\n-node-a\n-node-b\n")
	if got := with; got != "axis_reservations: found 2 nodes with active reservations" {
		t.Errorf("with entries: %q", got)
	}
	none := formatToolResultSummary("axis_reservations", "No active reservations")
	if got := none; got != "axis_reservations: no active reservations" {
		t.Errorf("no entries: %q", got)
	}
	// Marker present but no entry lines: falls through to the no-active branch.
	weird := formatToolResultSummary("axis_reservations", "Active reservations")
	if got := weird; got != "axis_reservations: no active reservations" {
		t.Errorf("marker without entries: %q", got)
	}
}

func TestCharToolSummaryFileTools(t *testing.T) {
	cases := []struct{ name, tool, result, want string }{
		{"read counts lines", "read_file", "a\nb\nc", "read_file: read 2 lines (5 chars)"},
		{"read empty", "read_file", "", "read_file: read 0 lines (0 chars)"},
		{"write", "write_file", "ignored", "write_file: wrote file"},
		{"edit", "edit_file", "ignored", "edit_file: edited file"},
		{"grep no matches", "grep_search", "No matches found", "grep_search: no matches found"},
		{"grep matches", "grep_search", "m1\nm2\nm3", "grep_search: found 3 match(es)"},
		{"grep single line", "grep_search", "only", "grep_search: found 1 match(es)"},
		{"list dir with header", "list_directory", "Directory: /tmp/x\nmore", "list_directory: /tmp/x"},
		{"list dir no header", "list_directory", "stuff only", "list_directory: listed directory"},
		{"run_shell", "run_shell", "out", "run_shell: executed shell command"},
	}
	for _, tc := range cases {
		got := formatToolResultSummary(tc.tool, tc.result)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestCharToolSummaryGitAndRemote(t *testing.T) {
	cases := []struct{ name, tool, result, want string }{
		{"git status branch", "git_status", "Branch: main\n2 files", "git_status: Branch: main"},
		{"git status fallback", "git_status", "nothing", "git_status: checked status"},
		{"git diff", "git_diff", "a\nb\nc", "git_diff: generated diff of 2 lines"},
		{"git log", "git_log", "a\nb", "git_log: retrieved 1 commits"},
		{"fleet exec", "fleet_exec", "x", "fleet_exec: executed across fleet"},
		{"remote write", "remote_write_file", "x", "remote_write_file: wrote remote file"},
		{"remote tail", "remote_tail_logs", "x", "remote_tail_logs: retrieved remote logs"},
	}
	for _, tc := range cases {
		got := formatToolResultSummary(tc.tool, tc.result)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestCharToolSummaryMCPAndFallback(t *testing.T) {
	mcp := formatToolResultSummary("mcp_server_tool", "payload")
	if mcp != "mcp_server_tool: executed successfully (7 chars)" {
		t.Errorf("mcp prefix: %q", mcp)
	}
	fallback := formatToolResultSummary("unknown_tool", "abc")
	if fallback != "unknown_tool returned 3 chars" {
		t.Errorf("fallback: %q", fallback)
	}
}
