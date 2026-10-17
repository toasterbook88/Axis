package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	mcpgoclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/mcpclient"
)

// recordingMCPClient records which tools actually reached the server.
type recordingMCPClient struct {
	mcpgoclient.MCPClient
	called []string
}

func (c *recordingMCPClient) CallTool(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	c.called = append(c.called, req.Params.Name)
	return &mcp.CallToolResult{Content: []mcp.Content{mcp.TextContent{Type: "text", Text: "ok"}}}, nil
}

// fakeApprovalRegistry exposes deceptively named tools from a server whose
// own name contains "get", with one tool allowlisted by exact name.
func fakeApprovalRegistry(client *recordingMCPClient) *mcpclient.Registry {
	r := mcpclient.NewRegistry()
	r.Add(&mcpclient.ServerConnection{
		Name:       "gadget",
		InitResult: &mcp.InitializeResult{},
		Client:     client,
		Config:     config.MCPServerConfig{AutoApproveTools: []string{"collab_search"}},
		Tools: []mcp.Tool{
			{Name: "search_and_delete", InputSchema: mcp.ToolInputSchema{Type: "object"}},
			{Name: "forget_memory", InputSchema: mcp.ToolInputSchema{Type: "object"}},
			{Name: "list_and_wipe", InputSchema: mcp.ToolInputSchema{Type: "object"}},
			{Name: "Collab_Search", InputSchema: mcp.ToolInputSchema{Type: "object"}},
			{Name: "collab_search", InputSchema: mcp.ToolInputSchema{Type: "object"}},
		},
	})
	return r
}

func TestIsReadOnlyToolNeverTrustsMCPNames(t *testing.T) {
	for _, name := range []string{
		"mcp_x_search_and_delete", "mcp_x_forget_memory", "mcp_gadget_run",
		"mcp_x_read_and_write", "mcp_cortex_recall", "mcp_x_get_status",
	} {
		if isReadOnlyTool(name) {
			t.Errorf("isReadOnlyTool(%q) = true; MCP names must never imply read-only", name)
		}
	}
}

func TestMCPAutoApprovalIsExactAllowlist(t *testing.T) {
	a := New(Config{
		Backend:     &scriptedBackend{},
		MCPRegistry: fakeApprovalRegistry(&recordingMCPClient{}),
		Output:      io.Discard,
		ToolContext: NewToolContext(&RuntimeView{}, nil),
	})
	if !a.ToolAutoApproved("mcp_gadget_collab_search") {
		t.Error("allowlisted mcp_gadget_collab_search should be auto-approved")
	}
	for _, name := range []string{
		"mcp_gadget_search_and_delete", "mcp_gadget_forget_memory",
		"mcp_gadget_list_and_wipe", "mcp_gadget_Collab_Search", "mcp_other_collab_search",
	} {
		if a.ToolAutoApproved(name) {
			t.Errorf("%s must ask first: only exact allowlisted names skip confirmation", name)
		}
	}
	if !a.ToolAutoApproved("read_file") {
		t.Error("built-in read-only tools stay auto-approved")
	}
}

func TestMCPDispatchAsksUnlessAllowlisted(t *testing.T) {
	var asked []string
	server := &recordingMCPClient{}
	a := New(Config{
		Backend:     &scriptedBackend{},
		MCPRegistry: fakeApprovalRegistry(server),
		Output:      io.Discard,
		ToolContext: NewToolContext(&RuntimeView{}, nil),
		Confirm: func(tool, _ string, _ int) ConfirmResult {
			asked = append(asked, tool)
			return ConfirmNo
		},
	})
	call := func(name string) error {
		_, err := a.dispatchToolCall(context.Background(), chat.ToolCall{
			Function: chat.ToolCallFunction{Name: name, Arguments: json.RawMessage(`{}`)},
		})
		return err
	}

	err := call("mcp_gadget_search_and_delete")
	if err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("unlisted MCP tool must be denied pending approval, got %v", err)
	}
	if len(asked) != 1 || asked[0] != "mcp_gadget_search_and_delete" {
		t.Fatalf("operator should have been asked once for the unlisted tool, asked=%v", asked)
	}

	asked = nil
	if err := call("mcp_gadget_collab_search"); err != nil {
		t.Fatalf("allowlisted MCP tool: %v", err)
	}
	if len(asked) != 0 {
		t.Fatalf("allowlisted MCP tool must not prompt, asked=%v", asked)
	}
	if strings.Join(server.called, ",") != "collab_search" {
		t.Fatalf("only the allowlisted tool may reach the server, called=%v", server.called)
	}
}
