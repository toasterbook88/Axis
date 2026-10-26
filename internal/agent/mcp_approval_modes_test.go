package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

// promptRecorder is an operator callback that records each tool it was asked
// about and answers with a fixed decision.
type promptRecorder struct {
	asked  []string
	answer ConfirmResult
}

func (p *promptRecorder) confirm(tool, _ string, _ int) ConfirmResult {
	p.asked = append(p.asked, tool)
	return p.answer
}

func dispatchByName(t *testing.T, a *Agent, name string) error {
	t.Helper()
	_, err := a.dispatchToolCall(context.Background(), chat.ToolCall{
		Function: chat.ToolCallFunction{Name: name, Arguments: json.RawMessage(`{}`)},
	})
	return err
}

// An external MCP tool that is not allowlisted by exact name must reach the
// operator in every autonomy mode and with --auto-approve. AXIS cannot rate
// such a tool, so the zero score dispatch passes must never read as low risk.
func TestUnlistedMCPAsksInEveryMode(t *testing.T) {
	const unlisted = "mcp_gadget_forget_memory"
	cases := []struct {
		name        string
		autonomy    AutonomyMode
		autoApprove bool
	}{
		{"default", AutonomyDefault, false},
		{"edit", AutonomyEdit, false},
		{"full", AutonomyFull, false},
		{"auto-approve", AutonomyDefault, true},
		{"edit+auto-approve", AutonomyEdit, true},
		{"full+auto-approve", AutonomyFull, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := &promptRecorder{answer: ConfirmNo}
			server := &recordingMCPClient{}
			a := New(Config{
				Backend:     &scriptedBackend{},
				MCPRegistry: fakeApprovalRegistry(server),
				Output:      io.Discard,
				ToolContext: NewToolContext(&RuntimeView{}, nil),
				Confirm:     op.confirm,
				Autonomy:    tc.autonomy,
				AutoApprove: tc.autoApprove,
			})
			err := dispatchByName(t, a, unlisted)
			if err == nil || !strings.Contains(err.Error(), "declined") {
				t.Fatalf("unlisted MCP tool must be declined by the operator, got err=%v", err)
			}
			if len(op.asked) != 1 || op.asked[0] != unlisted {
				t.Fatalf("operator must be asked once for %s, asked=%v", unlisted, op.asked)
			}
			if len(server.called) != 0 {
				t.Fatalf("declined tool reached the MCP server: %v", server.called)
			}

			// The exact allowlist still skips the prompt in this mode.
			op.asked = nil
			if err := dispatchByName(t, a, "mcp_gadget_collab_search"); err != nil {
				t.Fatalf("allowlisted MCP tool: %v", err)
			}
			if len(op.asked) != 0 {
				t.Fatalf("allowlisted MCP tool must not prompt, asked=%v", op.asked)
			}
		})
	}
}

// Surfaces install their own confirm after construction and the operator
// can switch mode mid-session; neither path may reopen the bypass.
func TestUnlistedMCPAsksAfterRuntimeChanges(t *testing.T) {
	const unlisted = "mcp_gadget_list_and_wipe"
	a := New(Config{
		Backend:     &scriptedBackend{},
		MCPRegistry: fakeApprovalRegistry(&recordingMCPClient{}),
		Output:      io.Discard,
		ToolContext: NewToolContext(&RuntimeView{}, nil),
		Confirm:     func(string, string, int) ConfirmResult { return ConfirmNo },
		Autonomy:    AutonomyEdit,
		AutoApprove: true,
	})
	op := &promptRecorder{answer: ConfirmNo}
	a.SetConfirm(op.confirm) // console surface path
	for _, mode := range []AutonomyMode{AutonomyEdit, AutonomyFull, AutonomyDefault, AutonomyFull} {
		a.SetAutonomy(mode)
		op.asked = nil
		if err := dispatchByName(t, a, unlisted); err == nil {
			t.Fatalf("mode %s: unlisted MCP tool ran without approval", mode)
		}
		if len(op.asked) != 1 {
			t.Fatalf("mode %s: operator asked %d times, want 1", mode, len(op.asked))
		}
	}
}

// "Always" approves that one tool for the session; a different unlisted MCP
// tool still asks, even in full mode.
func TestMCPSessionApprovalIsPerToolInFullMode(t *testing.T) {
	op := &promptRecorder{answer: ConfirmAlways}
	server := &recordingMCPClient{}
	a := New(Config{
		Backend:     &scriptedBackend{},
		MCPRegistry: fakeApprovalRegistry(server),
		Output:      io.Discard,
		ToolContext: NewToolContext(&RuntimeView{}, nil),
		Confirm:     op.confirm,
		Autonomy:    AutonomyFull,
	})
	for i := 0; i < 2; i++ {
		if err := dispatchByName(t, a, "mcp_gadget_forget_memory"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if len(op.asked) != 1 {
		t.Fatalf("always should approve the tool once for the session, asked=%v", op.asked)
	}
	if err := dispatchByName(t, a, "mcp_gadget_search_and_delete"); err != nil {
		t.Fatalf("second tool: %v", err)
	}
	if len(op.asked) != 2 || op.asked[1] != "mcp_gadget_search_and_delete" {
		t.Fatalf("a different unlisted MCP tool must ask, asked=%v", op.asked)
	}
}

// With no interactive approver (stdin at EOF), an unlisted MCP tool is
// denied in every mode instead of auto-approved.
func TestUnlistedMCPDeniedWithoutApprover(t *testing.T) {
	for _, mode := range []AutonomyMode{AutonomyDefault, AutonomyEdit, AutonomyFull} {
		server := &recordingMCPClient{}
		a := New(Config{
			Backend:     &scriptedBackend{},
			MCPRegistry: fakeApprovalRegistry(server),
			Output:      io.Discard,
			ToolContext: NewToolContext(&RuntimeView{}, nil),
			Confirm:     DefaultConfirm(strings.NewReader(""), io.Discard),
			Autonomy:    mode,
			AutoApprove: true,
		})
		if err := dispatchByName(t, a, "mcp_gadget_search_and_delete"); err == nil {
			t.Fatalf("mode %s: unlisted MCP tool ran with no approver", mode)
		}
		if len(server.called) != 0 {
			t.Fatalf("mode %s: tool reached the server: %v", mode, server.called)
		}
	}
}

// The MCP guard must not tighten built-in tools: score-based auto-approval
// still applies to them.
func TestScoreAutoApprovalUnchangedForBuiltins(t *testing.T) {
	deny := func(string, string, int) ConfirmResult { return ConfirmNo }
	if got := autonomyConfirm(AutonomyFull, deny)("run_shell", "", 10); got != ConfirmYes {
		t.Errorf("full mode low-risk run_shell = %v, want ConfirmYes", got)
	}
	if got := autonomyConfirm(AutonomyEdit, deny)("write_file", "", 0); got != ConfirmYes {
		t.Errorf("edit mode write_file = %v, want ConfirmYes", got)
	}
	if got := AutoApproveConfirm(70, deny)("run_shell", "", 10); got != ConfirmYes {
		t.Errorf("auto-approve low-risk run_shell = %v, want ConfirmYes", got)
	}
}
