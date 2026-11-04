package agent

import (
	"io"
	"testing"
)

// Cluster autonomy (#566) must keep the "ask before unlisted MCP tools in
// every mode" promise. The approval policy itself gates mcp_ tools before
// the mode switch, so a future scope change cannot silently auto-approve them.
func TestClusterAutonomyConfirmGatesUnlistedMCP(t *testing.T) {
	op := &promptRecorder{answer: ConfirmNo}
	confirm := autonomyConfirm(AutonomyCluster, op.confirm)
	if got := confirm("mcp_gadget_forget_memory", "", 0); got != ConfirmNo {
		t.Fatalf("cluster mode auto-approved an unlisted MCP tool: %v", got)
	}
	if len(op.asked) != 1 {
		t.Fatalf("operator asked %d times, want 1", len(op.asked))
	}
	op.asked = nil
	if got := AutoApproveConfirm(70, op.confirm)("mcp_gadget_forget_memory", "", 0); got != ConfirmNo || len(op.asked) != 1 {
		t.Fatalf("auto-approve wrapper bypassed the MCP gate: got=%v asked=%v", got, op.asked)
	}
}

// The cluster-only scope must not become an MCP bypass: in cluster mode,
// with or without --auto-approve, neither an unlisted nor an allowlisted
// MCP tool reaches the server.
func TestClusterScopeDoesNotBypassMCPGate(t *testing.T) {
	for _, autoApprove := range []bool{false, true} {
		op := &promptRecorder{answer: ConfirmYes}
		server := &recordingMCPClient{}
		a := New(Config{
			Backend:     &scriptedBackend{},
			MCPRegistry: fakeApprovalRegistry(server),
			Output:      io.Discard,
			ToolContext: NewToolContext(&RuntimeView{}, nil),
			Confirm:     op.confirm,
			Autonomy:    AutonomyCluster,
			AutoApprove: autoApprove,
		})
		for _, name := range []string{"mcp_gadget_forget_memory", "mcp_gadget_collab_search"} {
			if err := dispatchByName(t, a, name); err == nil {
				t.Fatalf("auto-approve=%v: %s ran in cluster mode", autoApprove, name)
			}
		}
		if len(server.called) != 0 {
			t.Fatalf("auto-approve=%v: MCP server reached in cluster mode: %v", autoApprove, server.called)
		}
	}
}
