package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// CallToolAutoRoute invokes a tool on the first available server that offers it,
// trying servers in deterministic name order until one succeeds.
func (r *Registry) CallToolAutoRoute(ctx context.Context, toolName string, args map[string]any) CallResult {
	entries := r.FindAllToolServers(toolName)
	if len(entries) == 0 {
		return CallResult{Err: fmt.Errorf("tool %q not found on any connected server", toolName)}
	}
	var lastErr error
	for _, entry := range entries {
		res := r.CallTool(ctx, entry.Server, toolName, args)
		if res.Err == nil {
			return res
		}
		lastErr = res.Err
	}
	return CallResult{Err: fmt.Errorf("tool %q failed on all %d server(s): last error: %w", toolName, len(entries), lastErr)}
}

type CallResult struct {
	Server string
	Result *mcp.CallToolResult
	Err    error
}

// isTransientError reports whether an error is likely temporary and worth retrying.
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if netErr, ok := err.(interface{ Temporary() bool }); ok && netErr.Temporary() {
		return true
	}
	// HTTP 5xx status codes (for HTTP transport)
	if strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), "502") ||
		strings.Contains(err.Error(), "503") || strings.Contains(err.Error(), "504") {
		return true
	}
	return false
}

// withRetry executes fn up to 3 times with exponential backoff starting at 200ms.
func withRetry(ctx context.Context, fn func() error) error {
	var err error
	backoff := 200 * time.Millisecond
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			backoff *= 2
		}
		err = fn()
		if err == nil {
			return nil
		}
		if !isTransientError(err) {
			return err
		}
	}
	return err
}

// CallToolWithProgress invokes a tool and prints progress notifications to stderr.
func (r *Registry) CallToolWithProgress(ctx context.Context, serverName, toolName string, args map[string]any, progressOut io.Writer) CallResult {
	sc := r.Get(serverName)
	if sc == nil {
		return CallResult{Server: serverName, Err: fmt.Errorf("server %q not configured", serverName)}
	}
	if !sc.Connected() {
		return CallResult{Server: serverName, Err: fmt.Errorf("server %q not connected: %v", serverName, sc.Err)}
	}

	sc.SetProgressHandler(func(p mcp.ProgressNotification) {
		msg := ""
		if p.Params.Message != "" {
			msg = " — " + p.Params.Message
		}
		if p.Params.Total > 0 {
			fmt.Fprintf(progressOut, "[progress %s] token=%s %.0f/%.0f%s\n", serverName, p.Params.ProgressToken, p.Params.Progress, p.Params.Total, msg)
		} else {
			fmt.Fprintf(progressOut, "[progress %s] token=%s %.0f%s\n", serverName, p.Params.ProgressToken, p.Params.Progress, msg)
		}
	})

	start := time.Now()
	var result *mcp.CallToolResult
	err := withRetry(ctx, func() error {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		req := mcp.CallToolRequest{}
		req.Params.Name = toolName
		req.Params.Arguments = args
		res, callErr := sc.Client.CallTool(cctx, req)
		result = res
		return callErr
	})
	sc.RecordCall(time.Since(start), err)
	return CallResult{Server: serverName, Result: result, Err: err}
}
func (r *Registry) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) CallResult {
	sc := r.Get(serverName)
	if sc == nil {
		return CallResult{Server: serverName, Err: fmt.Errorf("server %q not configured", serverName)}
	}
	if !sc.Connected() {
		return CallResult{Server: serverName, Err: fmt.Errorf("server %q not connected: %v", serverName, sc.Err)}
	}

	start := time.Now()
	var result *mcp.CallToolResult
	err := withRetry(ctx, func() error {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		req := mcp.CallToolRequest{}
		req.Params.Name = toolName
		req.Params.Arguments = args
		res, callErr := sc.Client.CallTool(cctx, req)
		result = res
		return callErr
	})
	sc.RecordCall(time.Since(start), err)
	return CallResult{Server: serverName, Result: result, Err: err}
}

// ReadResource fetches a resource by URI from a specific server.
func (r *Registry) ReadResource(ctx context.Context, serverName, uri string) (*mcp.ReadResourceResult, error) {
	sc := r.Get(serverName)
	if sc == nil {
		return nil, fmt.Errorf("server %q not configured", serverName)
	}
	if !sc.Connected() {
		return nil, fmt.Errorf("server %q not connected: %v", serverName, sc.Err)
	}

	start := time.Now()
	var result *mcp.ReadResourceResult
	err := withRetry(ctx, func() error {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		req := mcp.ReadResourceRequest{}
		req.Params.URI = uri
		res, callErr := sc.Client.ReadResource(cctx, req)
		result = res
		return callErr
	})
	sc.RecordCall(time.Since(start), err)
	return result, err
}

// ParseArgs unmarshals a raw JSON string into a map for tool invocation.
func ParseArgs(raw string) (map[string]any, error) {
	if raw == "" {
		return nil, nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("invalid JSON args: %w", err)
	}
	return args, nil
}
