package agent

import (
	"io"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/mcpclient"
)

// fakeCortexRegistry builds an MCP registry with a "cortex" server exposing
// the recall/remember/acquire_lock tools, so the Cortex-memory guidance path
// can be exercised without a live Cortex connection.
func fakeCortexRegistry() *mcpclient.Registry {
	r := mcpclient.NewRegistry()
	r.Add(&mcpclient.ServerConnection{
		Name:       "cortex",
		InitResult: &mcp.InitializeResult{}, // marks the connection as "connected"
		Tools: []mcp.Tool{
			{Name: "recall", Description: "semantic search of cluster memories", InputSchema: mcp.ToolInputSchema{Type: "object"}},
			{Name: "remember", Description: "store a cluster memory", InputSchema: mcp.ToolInputSchema{Type: "object"}},
			{Name: "acquire_lock", Description: "acquire a shared resource lock", InputSchema: mcp.ToolInputSchema{Type: "object"}},
		},
	})
	return r
}

func TestCortexMemoryGuidanceContent(t *testing.T) {
	g := cortexMemoryGuidance()
	for _, want := range []string{"mcp_cortex_recall", "mcp_cortex_remember", "mcp_cortex_acquire_lock", "mcp_cortex_release_lock", "mcp_cortex_publish_event", "advisory"} {
		if !strings.Contains(g, want) {
			t.Errorf("guidance missing %q", want)
		}
	}
}

func TestCortexGuidanceInjectedWhenCortexConnected(t *testing.T) {
	a := New(Config{
		Backend:     &scriptedBackend{responses: []chat.Message{{Role: "assistant", Content: "ok"}}},
		MCPRegistry: fakeCortexRegistry(),
		Output:      io.Discard,
		ToolContext: NewToolContext(&RuntimeView{}, nil),
	})
	// The cortex tools must be registered under the mcp_cortex_* prefix.
	if !a.tools.HasTool("mcp_cortex_recall") || !a.tools.HasTool("mcp_cortex_remember") || !a.tools.HasTool("mcp_cortex_acquire_lock") {
		t.Fatalf("cortex MCP tools not registered; have: %s", a.ToolNames())
	}
	// The guidance must appear in the system messages.
	var sys string
	for _, m := range a.conv.Messages() {
		if m.Role == "system" {
			sys += m.Content + "\n"
		}
	}
	if !strings.Contains(sys, "mcp_cortex_acquire_lock") || !strings.Contains(sys, "Cluster-shared memory (Cortex) is connected") {
		t.Fatalf("Cortex memory guidance not injected into system prompt")
	}
}

func TestCortexGuidanceNotInjectedWhenCortexAbsent(t *testing.T) {
	a := New(Config{
		Backend:     &scriptedBackend{responses: []chat.Message{{Role: "assistant", Content: "ok"}}},
		Output:      io.Discard,
		ToolContext: NewToolContext(&RuntimeView{}, nil),
	})
	if a.tools.HasTool("mcp_cortex_recall") {
		t.Fatalf("cortex tool should not be registered without an MCP registry")
	}
	var sys string
	for _, m := range a.conv.Messages() {
		if m.Role == "system" {
			sys += m.Content + "\n"
		}
	}
	if strings.Contains(sys, "Cluster-shared memory (Cortex) is connected") {
		t.Fatalf("Cortex guidance must not be injected when Cortex is not connected")
	}
}
