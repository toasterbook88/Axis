package agent

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
)

// recordingConfirm answers "always" and records every tool it was asked about.
type recordingConfirm struct {
	mu    sync.Mutex
	asked []string
}

func (r *recordingConfirm) confirm(tool, _ string, _ int) ConfirmResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, tool)
	return ConfirmAlways
}

func (r *recordingConfirm) count(tool string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, t := range r.asked {
		if t == tool {
			n++
		}
	}
	return n
}

func newApprovalAgent(t *testing.T, rc *recordingConfirm) *Agent {
	t.Helper()
	a := New(Config{
		Backend: &hookBackend{}, MaxTurns: 1, MaxTokens: 100000, Output: io.Discard,
		Confirm:     rc.confirm,
		ToolContext: &ToolContext{},
		RunShell:    func(context.Context, string) (string, error) { return "ran", nil },
	})
	a.tools.SetScope(ScopeExec)
	for _, name := range []string{"mcp_srv_query", "mcp_srv_claim_task"} {
		a.tools.allowExtra(name)
		a.tools.add(name, "test MCP tool", json.RawMessage(`{"type":"object"}`), func(context.Context, json.RawMessage) (string, error) {
			return "done", nil
		})
	}
	return a
}

func dispatch(t *testing.T, a *Agent, name, args string) {
	t.Helper()
	if _, err := a.dispatchToolCall(context.Background(), chat.ToolCall{
		Function: chat.ToolCallFunction{Name: name, Arguments: json.RawMessage(args)},
	}); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// "Always" approves one tool for the session. Approving a query used to set a
// session-wide flag, so a later MCP write (claim a shared task) and shell
// commands ran without asking.
func TestSessionApprovalIsPerTool(t *testing.T) {
	rc := &recordingConfirm{}
	a := newApprovalAgent(t, rc)

	dispatch(t, a, "mcp_srv_query", `{}`)
	dispatch(t, a, "mcp_srv_query", `{}`)
	if got := rc.count("mcp_srv_query"); got != 1 {
		t.Fatalf("query prompts = %d, want 1 (always applies to that tool)", got)
	}

	dispatch(t, a, "mcp_srv_claim_task", `{}`)
	if got := rc.count("mcp_srv_claim_task"); got != 1 {
		t.Fatalf("claim_task prompts = %d, want 1: approving a query must not approve another tool", got)
	}

	dispatch(t, a, "run_shell", `{"command":"echo hi"}`)
	if got := rc.count("run_shell"); got != 1 {
		t.Fatalf("run_shell prompts = %d, want 1: approving an MCP tool must not approve shell", got)
	}
	dispatch(t, a, "run_shell", `{"command":"echo again"}`)
	if got := rc.count("run_shell"); got != 1 {
		t.Fatalf("run_shell prompts = %d after always, want 1", got)
	}
}

// A sub-agent inherits exactly the tools approved for the session, not a
// blanket yes.
func TestSubAgentInheritsPerToolApprovals(t *testing.T) {
	rc := &recordingConfirm{}
	a := newApprovalAgent(t, rc)
	dispatch(t, a, "mcp_srv_query", `{}`)

	child := a.buildChildAgent(1, "", "")
	if !child.sessionApproved["mcp_srv_query"] {
		t.Fatal("child lost the parent's approval for the approved tool")
	}
	if child.sessionApproved["run_shell"] || child.sessionApproved["mcp_srv_claim_task"] {
		t.Fatalf("child gained approvals the parent never gave: %v", child.sessionApproved)
	}
	// The copy is independent: a child approval does not leak to the parent.
	child.sessionApproved["run_shell"] = true
	if a.sessionApproved["run_shell"] {
		t.Fatal("child approval leaked into the parent")
	}
}

// Approval decisions are written to the session log, so a later review can
// tell an approved MCP write from one that ran without asking.
func TestApprovalDecisionsAreLogged(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenDailySessionLog(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	day := time.Date(2026, 10, 22, 15, 0, 0, 0, time.FixedZone("UTC-4", -4*3600))
	log.now = func() time.Time { return day }
	rc := &recordingConfirm{}
	a := New(Config{
		Backend: &hookBackend{}, MaxTurns: 1, MaxTokens: 100000, Output: io.Discard,
		Confirm: rc.confirm, ToolContext: &ToolContext{}, SessionLog: log,
	})
	a.tools.allowExtra("mcp_srv_claim_task")
	a.tools.add("mcp_srv_claim_task", "test MCP tool", json.RawMessage(`{"type":"object"}`), func(context.Context, json.RawMessage) (string, error) {
		return "done", nil
	})
	dispatch(t, a, "mcp_srv_claim_task", `{}`)

	raw, err := os.ReadFile(filepath.Join(dir, "agent-2026-10-22.jsonl"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var found bool
	for _, rec := range allSessionRecords(t, raw) {
		if rec.Tool != nil && rec.Tool.Event == "approval" && rec.Tool.Name == "mcp_srv_claim_task" {
			found = rec.Tool.Decision == "approved_for_session" && rec.Tool.OK
		}
	}
	if !found {
		t.Fatalf("no approval record for the MCP write: %s", raw)
	}
}

// The prompt for an external MCP tool says it is not rated, instead of
// passing a zero score that read as "no gate finding".
func TestMCPApprovalPromptSaysNotRated(t *testing.T) {
	var gotDesc string
	a := New(Config{
		Backend: &hookBackend{}, MaxTurns: 1, MaxTokens: 100000, Output: io.Discard,
		Confirm:     func(_, desc string, _ int) ConfirmResult { gotDesc = desc; return ConfirmYes },
		ToolContext: &ToolContext{},
	})
	a.tools.allowExtra("mcp_srv_claim_task")
	a.tools.add("mcp_srv_claim_task", "test MCP tool", json.RawMessage(`{"type":"object"}`), func(context.Context, json.RawMessage) (string, error) {
		return "done", nil
	})
	dispatch(t, a, "mcp_srv_claim_task", `{}`)
	if !strings.HasPrefix(gotDesc, "safety why: "+unratedMCPWhy+"\n") {
		t.Fatalf("MCP approval description = %q", gotDesc)
	}
}
