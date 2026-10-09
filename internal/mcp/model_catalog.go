package axismcp

import (
	"context"

	mcpproto "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/toasterbook88/axis/internal/modelinventory"
)

// registerModelTools mounts the tools that answer "which model, where":
// the inference route explainer and the model catalog.
func registerModelTools(s *mcpserver.MCPServer, cache *SessionCache) {
	registerInferenceRouteTool(s)
	s.AddTool(
		mcpproto.NewTool(
			"model_catalog",
			mcpproto.WithDescription("Return every model the cluster snapshot observed, one entry per model per node: state (loaded/installed), locality (on-node, or cloud-proxy when requests leave the cluster), capabilities, size, and the nodes that could not be observed. Each node reports its own models; empty capabilities means unknown."),
			mcpproto.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
			return modelCatalogTool(ctx, req, cache)
		},
	)
}

func modelCatalogTool(ctx context.Context, _ mcpproto.CallToolRequest, cache *SessionCache) (*mcpproto.CallToolResult, error) {
	snap, err := cache.GetSnapshot(ctx, GetSessionID(ctx))
	if err != nil {
		return mcpproto.NewToolResultError(err.Error()), nil
	}
	source := "live"
	if cache.useCache {
		source = "daemon-cache"
	}
	return mcpproto.NewToolResultJSON(modelinventory.Catalog(snap, source))
}
