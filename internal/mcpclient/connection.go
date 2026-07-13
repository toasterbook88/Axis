// Package mcpclient provides a unified client for connecting to multiple MCP servers.
package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/netutil"
)

// ServerConnection holds the state of a single MCP server connection.
type ServerConnection struct {
	Name            string
	Transport       string
	Config          config.MCPServerConfig
	Client          mcpclient.MCPClient
	InitResult      *mcp.InitializeResult
	Tools           []mcp.Tool
	Resources       []mcp.Resource
	Prompts         []mcp.Prompt
	Err             error
	mu              sync.RWMutex
	cachedTools     []mcp.Tool
	cachedResources []mcp.Resource
	cachedPrompts   []mcp.Prompt
	cacheExpires    time.Time
	// Metrics
	callCount    int64
	errorCount   int64
	totalLatency time.Duration
	connectedAt  time.Time
}

// CacheTTL is the default duration for cached tool/resource/prompt listings.
const CacheTTL = 60 * time.Second

// Connected reports whether the server handshake succeeded.
func (sc *ServerConnection) Connected() bool {
	return sc.InitResult != nil && sc.Err == nil
}

// ConnectedAt returns the timestamp when the connection was established.
func (sc *ServerConnection) ConnectedAt() time.Time {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.connectedAt
}

// ToolCount returns the number of discovered tools.
func (sc *ServerConnection) ToolCount() int {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	if sc.cacheValid() {
		return len(sc.cachedTools)
	}
	return len(sc.Tools)
}

// ResourceCount returns the number of discovered resources.
func (sc *ServerConnection) ResourceCount() int {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	if sc.cacheValid() {
		return len(sc.cachedResources)
	}
	return len(sc.Resources)
}

// CachedTools returns cached tools if valid, otherwise falls back to live data.
func (sc *ServerConnection) CachedTools() []mcp.Tool {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	if sc.cacheValid() {
		return sc.cachedTools
	}
	return sc.Tools
}

// CachedResources returns cached resources if valid, otherwise falls back to live data.
func (sc *ServerConnection) CachedResources() []mcp.Resource {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	if sc.cacheValid() {
		return sc.cachedResources
	}
	return sc.Resources
}

// CachedPrompts returns cached prompts if valid, otherwise falls back to live data.
func (sc *ServerConnection) CachedPrompts() []mcp.Prompt {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	if sc.cacheValid() {
		return sc.cachedPrompts
	}
	return sc.Prompts
}

// RefreshCache updates the cache with current live data and resets TTL.
func (sc *ServerConnection) RefreshCache() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.cachedTools = make([]mcp.Tool, len(sc.Tools))
	copy(sc.cachedTools, sc.Tools)
	sc.cachedResources = make([]mcp.Resource, len(sc.Resources))
	copy(sc.cachedResources, sc.Resources)
	sc.cachedPrompts = make([]mcp.Prompt, len(sc.Prompts))
	copy(sc.cachedPrompts, sc.Prompts)
	sc.cacheExpires = time.Now().Add(CacheTTL)
}

func (sc *ServerConnection) cacheValid() bool {
	return !sc.cacheExpires.IsZero() && time.Now().Before(sc.cacheExpires)
}

// RecordCall increments call count and records latency.
func (sc *ServerConnection) RecordCall(latency time.Duration, err error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.callCount++
	sc.totalLatency += latency
	if err != nil {
		sc.errorCount++
	}
}

// Metrics returns a snapshot of the connection's metrics.
func (sc *ServerConnection) Metrics() (calls, errors int64, avgLatency time.Duration, uptime time.Duration) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	calls = sc.callCount
	errors = sc.errorCount
	if sc.callCount > 0 {
		avgLatency = sc.totalLatency / time.Duration(sc.callCount)
	}
	if !sc.connectedAt.IsZero() {
		uptime = time.Since(sc.connectedAt)
	}
	return
}

// Close closes the underlying client connection.
func (sc *ServerConnection) Close() error {
	if sc.Client != nil {
		return sc.Client.Close()
	}
	return nil
}

// connectStdio launches a stdio MCP server subprocess and connects to it.
func connectStdio(ctx context.Context, name string, cfg config.MCPServerConfig) (*ServerConnection, error) {
	if len(cfg.Command) == 0 {
		return nil, fmt.Errorf("mcp server %q: stdio transport requires command", name)
	}

	cmd := cfg.Command[0]
	args := cfg.Command[1:]

	client, err := mcpclient.NewStdioMCPClient(cmd, nil, args...)
	if err != nil {
		return nil, fmt.Errorf("mcp server %q: start stdio client: %w", name, err)
	}

	return handshake(ctx, name, "stdio", cfg, client)
}

// connectHTTP connects to an HTTP/SSE MCP server endpoint.
func connectHTTP(ctx context.Context, name string, cfg config.MCPServerConfig) (*ServerConnection, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("mcp server %q: http transport requires url", name)
	}
	if err := netutil.ValidateOutboundURL(cfg.URL); err != nil {
		return nil, fmt.Errorf("mcp server %q: %w", name, err)
	}

	var opts []transport.StreamableHTTPCOption
	if len(cfg.Headers) > 0 {
		opts = append(opts, transport.WithHTTPHeaders(cfg.Headers))
	}

	client, err := mcpclient.NewStreamableHttpClient(cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("mcp server %q: start http client: %w", name, err)
	}

	return handshake(ctx, name, "http", cfg, client)
}

// handshake performs the MCP initialize exchange and capability discovery.
func handshake(ctx context.Context, name, transport string, cfg config.MCPServerConfig, client mcpclient.MCPClient) (*ServerConnection, error) {
	sc := &ServerConnection{
		Name:      name,
		Transport: transport,
		Config:    cfg,
		Client:    client,
	}

	hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "axis-mcp-client",
		Version: "0.10.7",
	}

	initRes, err := client.Initialize(hctx, initReq)
	if err != nil {
		sc.Err = fmt.Errorf("initialize: %w", err)
		return sc, nil
	}
	sc.InitResult = initRes
	sc.connectedAt = time.Now()

	// Discover tools
	tctx, tcancel := context.WithTimeout(ctx, 10*time.Second)
	defer tcancel()
	if toolsRes, err := client.ListTools(tctx, mcp.ListToolsRequest{}); err == nil {
		sc.Tools = toolsRes.Tools
	}

	// Discover resources
	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	defer rcancel()
	if resRes, err := client.ListResources(rctx, mcp.ListResourcesRequest{}); err == nil {
		sc.Resources = resRes.Resources
	}

	// Discover prompts
	pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
	defer pcancel()
	if promptRes, err := client.ListPrompts(pctx, mcp.ListPromptsRequest{}); err == nil {
		sc.Prompts = promptRes.Prompts
	}

	sc.RefreshCache()
	return sc, nil
}

// SetProgressHandler registers a handler for progress notifications on this connection.
func (sc *ServerConnection) SetProgressHandler(handler func(mcp.ProgressNotification)) {
	if sc.Client != nil {
		sc.Client.OnNotification(func(notification mcp.JSONRPCNotification) {
			if notification.Method == "notifications/progress" {
				// Best-effort decode
				var progress mcp.ProgressNotification
				if data, err := json.Marshal(notification.Params); err == nil {
					_ = json.Unmarshal(data, &progress)
				}
				handler(progress)
			}
		})
	}
}
