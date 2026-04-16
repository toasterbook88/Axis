// Package cortex provides a client for the Cortex MCP server running on Foundry.
//
// Cortex is an optional cluster coordination layer that exposes:
//   - Distributed vector memory via Qdrant (semantic recall)
//   - A CI/CD event bus (test failures, deployment events)
//   - Cross-agent distributed locking (acquire_lock / release_lock)
//
// The Cortex MCP server speaks JSON-RPC 2.0 over HTTP on Foundry:8200.
// Authentication uses a Bearer token resolved from AXIS_CORTEX_SECRET or
// ~/.axis/cortex.token via internal/secrets.
package cortex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	defaultMCPPort    = 8200
	defaultQdrantPort = 6333
	defaultTimeout    = 10 * time.Second
	cortexCollection  = "cortex_memories"
	mcpPath           = "/mcp"
)

// Client talks to the Cortex MCP server running on Foundry.
// All methods are safe for concurrent use.
type Client struct {
	mcpURL    string
	qdrantURL string
	token     string
	http      *http.Client
}

// NewClient creates a Cortex client targeting the given Foundry hostname.
// token may be empty for unauthenticated access (if the server permits it).
// Prefer NewClientWithTimeout or NewClientWithOptions when you need to override timeouts.
func NewClient(foundryHost, token string) *Client {
	return NewClientWithOptions(foundryHost, token, defaultMCPPort, defaultQdrantPort, defaultTimeout)
}

// NewClientWithTimeout creates a Cortex client with a custom HTTP timeout.
// Use this when a specific operation (e.g. recall with cold embedding model)
// needs a longer deadline than the package default.
func NewClientWithTimeout(foundryHost, token string, timeout time.Duration) *Client {
	return NewClientWithOptions(foundryHost, token, defaultMCPPort, defaultQdrantPort, timeout)
}

// NewClientWithOptions creates a Cortex client with explicit port and timeout
// overrides. Use this in tests to point at httptest servers.
func NewClientWithOptions(foundryHost, token string, mcpPort, qdrantPort int, timeout time.Duration) *Client {
	return &Client{
		mcpURL:    fmt.Sprintf("http://%s:%d%s", foundryHost, mcpPort, mcpPath),
		qdrantURL: fmt.Sprintf("http://%s:%d", foundryHost, qdrantPort),
		token:     token,
		http:      &http.Client{Timeout: timeout},
	}
}

// — JSON-RPC 2.0 wire types —

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("cortex MCP error %d: %s", e.Code, e.Message)
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// — Public response types —

// HealthResponse is returned by Status.
type HealthResponse struct {
	// Status is "healthy" when the MCP server is reachable. Qdrant failures are
	// degraded and reflected by Memories=0 rather than changing Status.
	Status string `json:"status"`
	// MCPTools is the number of tools registered on the Cortex MCP server.
	MCPTools int `json:"mcp_tools"`
	// Memories is the number of vector points in the cortex_memories collection.
	Memories int `json:"memories"`
}

// MemoryHit is a single result from a Recall query.
type MemoryHit struct {
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// Event is a single entry from the Cortex event bus.
type Event struct {
	ID        string          `json:"event_id"`
	Type      string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

// LockResult is the outcome of an AcquireLock or ReleaseLock call.
type LockResult struct {
	// Status is "ACQUIRED" on success or "CONFLICT" when already held.
	Status    string `json:"status"`
	Resource  string `json:"resource"`
	SessionID string `json:"session_id,omitempty"`
}

// — Public methods —

// Status probes the Cortex MCP server and Qdrant, returning an aggregated health view.
// It lists MCP tools to verify the server is live, then queries Qdrant for the
// memory point count. Qdrant failures are surfaced as Memories=0, not as errors,
// since the MCP server being up is the primary health signal.
func (c *Client) Status(ctx context.Context) (*HealthResponse, error) {
	raw, err := c.listTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("cortex MCP status probe failed at %s: %w", c.mcpURL, err)
	}

	var toolsList struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &toolsList); err != nil {
		return nil, fmt.Errorf("cortex MCP at %s returned unexpected tools/list response: %w", c.mcpURL, err)
	}

	memories, _ := c.qdrantPointCount(ctx)

	return &HealthResponse{
		Status:   "healthy",
		MCPTools: len(toolsList.Tools),
		Memories: memories,
	}, nil
}

// Recall performs a semantic memory search via the Cortex MCP recall tool.
func (c *Client) Recall(ctx context.Context, query string) ([]MemoryHit, error) {
	raw, err := c.callTool(ctx, "recall", map[string]any{
		"query": query,
	})
	if err != nil {
		return nil, fmt.Errorf("cortex recall: %w", err)
	}
	var hits []MemoryHit
	if err := json.Unmarshal(raw, &hits); err != nil {
		return nil, fmt.Errorf("cortex recall: unexpected response shape: %w", err)
	}
	return hits, nil
}

// Events retrieves recent entries from the Cortex event bus via get_events_since.
// limit <= 0 defaults to 10.
func (c *Client) Events(ctx context.Context, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 10
	}
	raw, err := c.callTool(ctx, "get_events_since", map[string]any{
		"limit":      limit,
		"since":      "",
		"event_type": "",
	})
	if err != nil {
		return nil, fmt.Errorf("cortex events: %w", err)
	}
	var events []Event
	if err := json.Unmarshal(raw, &events); err != nil {
		return nil, fmt.Errorf("cortex events: unexpected response shape: %w", err)
	}
	return events, nil
}

// PublishEvent sends an event to the Cortex event bus.
// eventType is a short snake_case label (e.g. "test_failure", "deploy_start").
// payload may be any JSON-serialisable value.
func (c *Client) PublishEvent(ctx context.Context, eventType string, payload any) error {
	_, err := c.callTool(ctx, "publish_event", map[string]any{
		"event_type": eventType,
		"payload":    payload,
	})
	if err != nil {
		return fmt.Errorf("cortex publish_event: %w", err)
	}
	return nil
}

// AcquireLock requests a distributed lock on a named resource.
// sessionID must uniquely identify this agent instance
// (e.g. "claude-1747000000" or "codex-<unix-ts>").
// Returns LockResult.Status == "ACQUIRED" on success, "CONFLICT" when held by another session.
func (c *Client) AcquireLock(ctx context.Context, resource, sessionID string) (*LockResult, error) {
	raw, err := c.callTool(ctx, "acquire_lock", map[string]any{
		"resource":   resource,
		"session_id": sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("cortex acquire_lock: %w", err)
	}
	var result LockResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("cortex acquire_lock: unexpected response shape: %w", err)
	}
	return &result, nil
}

// ReleaseLock releases a previously acquired lock on the named resource.
func (c *Client) ReleaseLock(ctx context.Context, resource string) error {
	_, err := c.callTool(ctx, "release_lock", map[string]any{
		"resource": resource,
	})
	if err != nil {
		return fmt.Errorf("cortex release_lock: %w", err)
	}
	return nil
}

// — Internal helpers —

// callTool sends a tools/call JSON-RPC 2.0 request and unwraps the MCP
// content envelope that FastMCP 3.x wraps around tool results:
//
//	{"content":[{"type":"text","text":"<json>"}],"isError":bool}
//
// The actual payload is extracted from content[0].text.
func (c *Client) callTool(ctx context.Context, tool string, args any) (json.RawMessage, error) {
	raw, err := c.doRPC(ctx, rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params: map[string]any{
			"name":      tool,
			"arguments": args,
		},
	})
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return raw, nil // not an envelope — return raw JSON as-is
	}
	if envelope.IsError {
		if len(envelope.Content) > 0 && envelope.Content[0].Text != "" {
			return nil, fmt.Errorf("tool %s error: %s", tool, envelope.Content[0].Text)
		}
		return nil, fmt.Errorf("tool %s returned isError=true", tool)
	}
	if len(envelope.Content) == 0 {
		return raw, nil
	}
	item := envelope.Content[0]
	if item.Type != "text" || item.Text == "" {
		return nil, fmt.Errorf("tool %s: expected text content item, got type=%q text=%q", tool, item.Type, item.Text)
	}
	return json.RawMessage(item.Text), nil
}

// listTools sends a tools/list JSON-RPC 2.0 request.
func (c *Client) listTools(ctx context.Context) (json.RawMessage, error) {
	return c.doRPC(ctx, rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/list",
	})
}

// doRPC marshals req, POSTs it to the MCP endpoint, and returns the result payload.
func (c *Client) doRPC(ctx context.Context, req rpcRequest) (json.RawMessage, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.mcpURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, errors.New("authentication required — set AXIS_CORTEX_SECRET or ~/.axis/cortex.token")
	case http.StatusForbidden:
		return nil, errors.New("access denied — check AXIS_CORTEX_SECRET value")
	case http.StatusOK:
		// OK, fall through.
	default:
		return nil, fmt.Errorf("unexpected HTTP %d from Cortex MCP server", resp.StatusCode)
	}

	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}
	return rpcResp.Result, nil
}

// qdrantPointCount queries the Qdrant REST API directly for the point count
// in the cortex_memories collection. Returns 0 on any error so that a
// degraded Qdrant does not mask a healthy MCP server in Status().
func (c *Client) qdrantPointCount(ctx context.Context) (int, error) {
	url := fmt.Sprintf("%s/collections/%s", c.qdrantURL, cortexCollection)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected HTTP %d from Qdrant", resp.StatusCode)
	}

	var data struct {
		Result struct {
			PointsCount int `json:"points_count"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, err
	}
	return data.Result.PointsCount, nil
}
