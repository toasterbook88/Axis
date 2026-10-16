package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/toasterbook88/axis/internal/mcpclient"
)

// ToolAutoApproved reports whether name runs without operator confirmation:
// a built-in read-only tool, or an external MCP tool the operator listed in
// mcp_servers.<server>.auto_approve_tools.
func (a *Agent) ToolAutoApproved(name string) bool {
	return isReadOnlyTool(name) || a.tools.mcpAutoApprovedTool(name)
}

// RegisterMCPTools registers all tools from connected MCP servers. A tool
// skips confirmation only when its server's auto_approve_tools lists its
// exact name; the server's own name, description and annotations never
// grant that.
func (r *ToolRegistry) RegisterMCPTools(mcpRegistry *mcpclient.Registry) {
	if mcpRegistry == nil {
		return
	}
	for _, entry := range mcpRegistry.ListAllTools() {
		server := entry.Server
		toolName := entry.Tool.Name
		agentToolName := fmt.Sprintf("mcp_%s_%s", server, toolName)

		paramSchema, err := json.Marshal(entry.Tool.InputSchema)
		if err != nil {
			continue
		}
		if sc := mcpRegistry.Get(server); sc != nil && sc.Config.AutoApproves(toolName) {
			if r.mcpAutoApproved == nil {
				r.mcpAutoApproved = make(map[string]bool)
			}
			r.mcpAutoApproved[agentToolName] = true
		}

		r.add(agentToolName, entry.Tool.Description, json.RawMessage(paramSchema), func(ctx context.Context, args json.RawMessage) (string, error) {
			var mcpArgs map[string]any
			if len(args) > 0 {
				if err := json.Unmarshal(args, &mcpArgs); err != nil {
					return "", fmt.Errorf("invalid JSON arguments for MCP tool: %w", err)
				}
			}
			res := mcpRegistry.CallTool(ctx, server, toolName, mcpArgs)
			if res.Err != nil {
				return "", res.Err
			}

			var parts []string
			for _, content := range res.Result.Content {
				if tc, ok := content.(mcp.TextContent); ok {
					parts = append(parts, tc.Text)
				} else if ic, ok := content.(mcp.ImageContent); ok {
					parts = append(parts, fmt.Sprintf("[image: %s %d bytes]", ic.MIMEType, len(ic.Data)))
				} else {
					b, _ := json.MarshalIndent(content, "", "  ")
					parts = append(parts, string(b))
				}
			}
			return strings.Join(parts, "\n"), nil
		})
	}
}
