// Package mcpclient provides a unified client for connecting to multiple MCP servers.
package mcpclient

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/toasterbook88/axis/internal/config"
)

// Registry manages connections to multiple MCP servers.
type Registry struct {
	mu      sync.RWMutex
	servers map[string]*ServerConnection
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		servers: make(map[string]*ServerConnection),
	}
}

// ConnectAll connects to every MCP server defined in the AXIS config.
// Errors are stored per-server and surfaced via the connection's Err field;
// the function never returns an error itself.
func (r *Registry) ConnectAll(ctx context.Context, cfg *config.Config) {
	if cfg == nil || len(cfg.MCPServers) == 0 {
		return
	}

	var wg sync.WaitGroup
	for name, serverCfg := range cfg.MCPServers {
		wg.Add(1)
		go func(n string, sc config.MCPServerConfig) {
			defer wg.Done()
			var conn *ServerConnection
			var err error
			switch strings.ToLower(sc.Transport) {
			case "stdio":
				conn, err = connectStdio(ctx, n, sc)
			case "http":
				conn, err = connectHTTP(ctx, n, sc)
			default:
				conn = &ServerConnection{
					Name:      n,
					Transport: sc.Transport,
					Config:    sc,
					Err:       fmt.Errorf("unsupported transport %q", sc.Transport),
				}
			}
			if err != nil {
				conn = &ServerConnection{
					Name:      n,
					Transport: sc.Transport,
					Config:    sc,
					Err:       err,
				}
			}
			r.mu.Lock()
			r.servers[n] = conn
			r.mu.Unlock()
		}(name, serverCfg)
	}
	wg.Wait()
}

// Get returns the connection for a named server, or nil.
func (r *Registry) Get(name string) *ServerConnection {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.servers[name]
}

// Names returns all configured server names, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.servers))
	for n := range r.servers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ConnectedNames returns names of successfully connected servers.
func (r *Registry) ConnectedNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var names []string
	for n, s := range r.servers {
		if s.Connected() {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// Close closes all managed connections.
func (r *Registry) Close() {
	r.mu.RLock()
	servers := make([]*ServerConnection, 0, len(r.servers))
	for _, s := range r.servers {
		servers = append(servers, s)
	}
	r.mu.RUnlock()
	for _, s := range servers {
		_ = s.Close()
	}
}

// ToolEntry is a tool annotated with the server that provides it.
type ToolEntry struct {
	Server string
	Tool   mcp.Tool
}

// ListAllTools returns every tool from every connected server.
func (r *Registry) ListAllTools() []ToolEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []ToolEntry
	for _, s := range r.servers {
		if !s.Connected() {
			continue
		}
		for _, t := range s.CachedTools() {
			out = append(out, ToolEntry{Server: s.Name, Tool: t})
		}
	}
	return out
}

// FindTool returns the first tool matching name across all connected servers.
// Tools are searched in deterministic server name order.
func (r *Registry) FindTool(name string) (ToolEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.servers))
	for n := range r.servers {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, n := range names {
		s := r.servers[n]
		if !s.Connected() {
			continue
		}
		for _, t := range s.CachedTools() {
			if t.Name == name {
				return ToolEntry{Server: s.Name, Tool: t}, true
			}
		}
	}
	return ToolEntry{}, false
}

// FindAllToolServers returns every server that offers a tool with the given name.
func (r *Registry) FindAllToolServers(name string) []ToolEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.servers))
	for n := range r.servers {
		names = append(names, n)
	}
	sort.Strings(names)

	var out []ToolEntry
	for _, n := range names {
		s := r.servers[n]
		if !s.Connected() {
			continue
		}
		for _, t := range s.CachedTools() {
			if t.Name == name {
				out = append(out, ToolEntry{Server: s.Name, Tool: t})
			}
		}
	}
	return out
}

// ResourceEntry is a resource annotated with its server.
type ResourceEntry struct {
	Server   string
	Resource mcp.Resource
}

// ListAllResources returns every resource from every connected server.
func (r *Registry) ListAllResources() []ResourceEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []ResourceEntry
	for _, s := range r.servers {
		if !s.Connected() {
			continue
		}
		for _, res := range s.CachedResources() {
			out = append(out, ResourceEntry{Server: s.Name, Resource: res})
		}
	}
	return out
}

// PromptEntry is a prompt annotated with its server.
type PromptEntry struct {
	Server string
	Prompt mcp.Prompt
}

// ListAllPrompts returns every prompt from every connected server.
func (r *Registry) ListAllPrompts() []PromptEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []PromptEntry
	for _, s := range r.servers {
		if !s.Connected() {
			continue
		}
		for _, p := range s.CachedPrompts() {
			out = append(out, PromptEntry{Server: s.Name, Prompt: p})
		}
	}
	return out
}

// GetPrompt fetches a specific prompt by name from a server, with optional arguments.
func (r *Registry) GetPrompt(ctx context.Context, serverName, promptName string, args map[string]any) (*mcp.GetPromptResult, error) {
	sc := r.Get(serverName)
	if sc == nil {
		return nil, fmt.Errorf("server %q not configured", serverName)
	}
	if !sc.Connected() {
		return nil, fmt.Errorf("server %q not connected: %v", serverName, sc.Err)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req := mcp.GetPromptRequest{}
	req.Params.Name = promptName
	// Convert map[string]any to map[string]string for MCP prompt arguments
	if args != nil {
		strArgs := make(map[string]string, len(args))
		for k, v := range args {
			strArgs[k] = fmt.Sprintf("%v", v)
		}
		req.Params.Arguments = strArgs
	}
	return sc.Client.GetPrompt(cctx, req)
}
