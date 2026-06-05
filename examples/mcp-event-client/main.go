package main

import (
	"context"
	"fmt"
	"log"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// Minimal reference client showing how an AI agent (or tool) can
// interact with AXIS lifecycle events via MCP.
//
// This is an example only — not production code.
func main() {
	ctx := context.Background()

	// Connect to a local AXIS MCP server (stdio mode for example)
	// In real use this would be launched by the agent or connected over stdio/HTTP.
	c, err := client.NewStdioMCPClient("axis", []string{}, "--mcp")
	if err != nil {
		log.Fatalf("failed to create MCP client: %v", err)
	}
	defer c.Close()

	// Initialize
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "axis-event-example", Version: "0.1.0"}

	_, err = c.Initialize(ctx, initReq)
	if err != nil {
		log.Fatalf("initialize failed: %v", err)
	}

	// List available lifecycle events
	listReq := mcp.CallToolRequest{}
	listReq.Params.Name = "list_lifecycle_events"
	result, err := c.CallTool(ctx, listReq)
	if err != nil {
		log.Fatalf("list_lifecycle_events failed: %v", err)
	}
	fmt.Printf("Available events: %+v\n", result)

	// Register interest (example)
	regReq := mcp.CallToolRequest{}
	regReq.Params.Name = "register_event_interest"
	regReq.Params.Arguments = map[string]any{
		"events":        "task.execution.pre,task.execution.post,daemon.refresh.post",
		"callback_tool": "my-agent-event-handler",
	}
	regResult, _ := c.CallTool(ctx, regReq)
	fmt.Printf("Registration result: %+v\n", regResult)

	// Poll recent events (in a real loop an agent would do this periodically)
	recentReq := mcp.CallToolRequest{}
	recentReq.Params.Name = "get_recent_events"
	recentReq.Params.Arguments = map[string]any{"limit": 10}
	recent, _ := c.CallTool(ctx, recentReq)
	fmt.Printf("Recent events: %+v\n", recent)

	fmt.Println("Example client finished. In a real agent you would keep a loop polling or using notifications.")
}
