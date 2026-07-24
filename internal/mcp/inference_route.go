package axismcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	mcpproto "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/llmrouter"
)

// Test seams for AI config loading (operator-local ~/.axis/ai.yaml).
var (
	loadAIConfigForMCP = config.LoadAIOrEmpty
	aiConfigPathForMCP = config.DefaultAIConfigPath
	resolveRoleForMCP  = llmrouter.ResolveRole
)

func registerInferenceRouteTool(s *mcpserver.MCPServer) {
	s.AddTool(
		mcpproto.NewTool(
			"inference_route_explain",
			mcpproto.WithDescription("Advisory dry-run: resolve an inference role or model to a backend from ~/.axis/ai.yaml (does not generate completions)"),
			mcpproto.WithReadOnlyHintAnnotation(true),
			mcpproto.WithString(
				"role",
				mcpproto.Description("Configured role name (e.g. default, fast). Optional if model is set."),
			),
			mcpproto.WithString(
				"model",
				mcpproto.Description("Model id override, or bare model when role is empty"),
			),
			mcpproto.WithBoolean(
				"skip_probe",
				mcpproto.Description("If true, skip live health probes (config prefer order only)"),
			),
			mcpproto.WithBoolean(
				"allow_unlisted",
				mcpproto.Description("If true, accept healthy backends even when the model is absent from a non-empty list (default false)"),
			),
			mcpproto.WithBoolean(
				"require_model_listed",
				mcpproto.Description("Deprecated alias: if false, same as allow_unlisted=true. Prefer allow_unlisted."),
			),
		),
		inferenceRouteExplainTool,
	)
}

func inferenceRouteExplainTool(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
	role := strings.TrimSpace(optionalString(req, "role"))
	model := strings.TrimSpace(optionalString(req, "model"))
	if role == "" && model == "" {
		return mcpproto.NewToolResultError("provide role and/or model"), nil
	}

	skipProbe := optionalBool(req, "skip_probe", false)
	allowUnlisted := optionalBool(req, "allow_unlisted", false)
	// Backward-compatible optional: require_model_listed=false means allow unlisted.
	if v, ok := optionalBoolPresent(req, "require_model_listed"); ok && !v {
		allowUnlisted = true
	}
	requireListed := !skipProbe && !allowUnlisted

	path := aiConfigPathForMCP()
	cfg, err := loadAIConfigForMCP(path)
	if err != nil {
		return mcpproto.NewToolResultError(fmt.Sprintf("load AI config %s: %v", path, err)), nil
	}
	if len(cfg.Backends) == 0 {
		return mcpproto.NewToolResultError(fmt.Sprintf("no backends configured; copy ai.example.yaml to %s", path)), nil
	}

	// Bound probe time for MCP callers.
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
	}

	dec, err := resolveRoleForMCP(ctx, cfg, llmrouter.ResolveRoleOptions{
		Role:               role,
		Model:              model,
		SkipProbe:          skipProbe,
		RequireModelListed: requireListed,
	})
	if err != nil {
		// Return structured partial decision when available, with isError.
		if dec.Model != "" || len(dec.Reasoning) > 0 {
			result, jerr := mcpproto.NewToolResultJSON(dec)
			if jerr != nil {
				return mcpproto.NewToolResultError(err.Error()), nil
			}
			// IsError true for unlisted models and other resolve failures so MCP
			// clients do not treat a partial decision as success.
			result.IsError = true
			if errors.Is(err, llmrouter.ErrModelUnlisted) {
				return result, nil
			}
			return result, nil
		}
		return mcpproto.NewToolResultError(err.Error()), nil
	}
	return mcpproto.NewToolResultJSON(dec)
}

func optionalString(req mcpproto.CallToolRequest, key string) string {
	args := req.GetArguments()
	if args == nil {
		return ""
	}
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func optionalBool(req mcpproto.CallToolRequest, key string, def bool) bool {
	if v, ok := optionalBoolPresent(req, key); ok {
		return v
	}
	return def
}

func optionalBoolPresent(req mcpproto.CallToolRequest, key string) (bool, bool) {
	args := req.GetArguments()
	if args == nil {
		return false, false
	}
	v, ok := args[key]
	if !ok || v == nil {
		return false, false
	}
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no":
			return false, true
		}
	}
	return false, false
}
