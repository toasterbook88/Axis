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

// "Never" blocks the whole session, including a tool approved "always"
// earlier. The generic path used to check the block only for tools that were
// not yet approved, so an approved MCP write kept running.
func TestNeverBlocksToolApprovedForSession(t *testing.T) {
	a := newApprovalAgent(t, &recordingConfirm{})
	a.SetConfirm(func(tool, _ string, _ int) ConfirmResult {
		if tool == "mcp_srv_query" {
			return ConfirmAlways
		}
		return ConfirmNever
	})
	dispatch(t, a, "mcp_srv_query", `{}`)
	if _, err := a.dispatchToolCall(context.Background(), chat.ToolCall{
		Function: chat.ToolCallFunction{Name: "mcp_srv_claim_task", Arguments: json.RawMessage(`{}`)},
	}); err == nil {
		t.Fatal("never answer did not refuse the tool")
	}
	_, err := a.dispatchToolCall(context.Background(), chat.ToolCall{
		Function: chat.ToolCallFunction{Name: "mcp_srv_query", Arguments: json.RawMessage(`{}`)},
	})
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("tool approved for the session still ran after never: err=%v", err)
	}
}

// Calls to one tool run in parallel and can reach confirmation in any order.
// Each approval record carries its tool-call ID, so it can be matched to the
// arguments the operator saw.
func TestApprovalRecordsCarryToolCallID(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenDailySessionLog(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	day := time.Date(2026, 10, 22, 15, 0, 0, 0, time.FixedZone("UTC-4", -4*3600))
	log.now = func() time.Time { return day }
	a := New(Config{
		Backend: &scriptedBackend{responses: []chat.Message{{
			Role: chat.RoleAssistant,
			ToolCalls: []chat.ToolCall{
				toolCall("c-allow", "mcp_srv_claim_task", `{"task":"allow"}`),
				toolCall("c-deny", "mcp_srv_claim_task", `{"task":"deny"}`),
			},
		}, {Role: chat.RoleAssistant, Content: "done"}}},
		MaxTurns: 3, MaxTokens: 100000, Output: io.Discard, ToolContext: &ToolContext{}, SessionLog: log,
		Confirm: func(_, desc string, _ int) ConfirmResult {
			if strings.Contains(desc, "deny") {
				return ConfirmNo
			}
			return ConfirmYes
		},
	})
	a.tools.allowExtra("mcp_srv_claim_task")
	a.tools.add("mcp_srv_claim_task", "test MCP tool", json.RawMessage(`{"type":"object"}`), func(context.Context, json.RawMessage) (string, error) {
		return "claimed", nil
	})
	if err := a.Run(context.Background(), "claim both"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "agent-2026-10-22.jsonl"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	got := map[string]string{}
	for _, rec := range allSessionRecords(t, raw) {
		if rec.Tool != nil && rec.Tool.Event == "approval" {
			got[rec.Tool.ID] = rec.Tool.Decision
		}
	}
	if got["c-allow"] != "approved" || got["c-deny"] != "denied" || len(got) != 2 {
		t.Fatalf("approval records by call ID = %v", got)
	}
}

// After "never", a sub-agent must not run tools the parent approved "always"
// earlier. The child used to start with the parent's approvals but without
// its block. spawn_subagent is hidden from the model today (neverTool), so
// this guards the sub-agent path directly in case that changes.
func TestNeverBlocksSubAgents(t *testing.T) {
	a := newApprovalAgent(t, &recordingConfirm{})
	a.SetConfirm(func(tool, _ string, _ int) ConfirmResult {
		if tool == "run_shell" {
			return ConfirmAlways
		}
		return ConfirmNever
	})
	dispatch(t, a, "run_shell", `{"command":"echo approved"}`)
	if _, err := a.dispatchToolCall(context.Background(), chat.ToolCall{
		Function: chat.ToolCallFunction{Name: "mcp_srv_claim_task", Arguments: json.RawMessage(`{}`)},
	}); err == nil {
		t.Fatal("never answer did not refuse the tool")
	}

	_, err := a.dispatchSubagent(context.Background(), json.RawMessage(`{"prompt":"run echo"}`))
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("spawn_subagent after never: err=%v, want blocked", err)
	}

	child := a.buildChildAgent(1, "", "")
	if !child.blockAll {
		t.Fatal("child did not inherit the session block")
	}
	child.tools.SetScope(ScopeExec)
	if _, err := child.dispatchToolCall(context.Background(), chat.ToolCall{
		Function: chat.ToolCallFunction{Name: "run_shell", Arguments: json.RawMessage(`{"command":"echo child"}`)},
	}); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("child ran a parent-approved shell command after never: err=%v", err)
	}
}
