package mcpclient

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/toasterbook88/axis/internal/config"
)

// mockClient implements mcpclient.MCPClient for testing.
type mockClient struct {
	initResult *mcp.InitializeResult
	tools      []mcp.Tool
	resources  []mcp.Resource
	prompts    []mcp.Prompt
	closed     bool
}

func (m *mockClient) Initialize(ctx context.Context, req mcp.InitializeRequest) (*mcp.InitializeResult, error) {
	return m.initResult, nil
}

func (m *mockClient) Ping(ctx context.Context) error { return nil }
func (m *mockClient) ListResourcesByPage(ctx context.Context, req mcp.ListResourcesRequest) (*mcp.ListResourcesResult, error) {
	return nil, nil
}
func (m *mockClient) ListResources(ctx context.Context, req mcp.ListResourcesRequest) (*mcp.ListResourcesResult, error) {
	return &mcp.ListResourcesResult{Resources: m.resources}, nil
}
func (m *mockClient) ListResourceTemplatesByPage(ctx context.Context, req mcp.ListResourceTemplatesRequest) (*mcp.ListResourceTemplatesResult, error) {
	return nil, nil
}
func (m *mockClient) ListResourceTemplates(ctx context.Context, req mcp.ListResourceTemplatesRequest) (*mcp.ListResourceTemplatesResult, error) {
	return nil, nil
}
func (m *mockClient) ReadResource(ctx context.Context, req mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	return nil, nil
}
func (m *mockClient) Subscribe(ctx context.Context, req mcp.SubscribeRequest) error     { return nil }
func (m *mockClient) Unsubscribe(ctx context.Context, req mcp.UnsubscribeRequest) error { return nil }
func (m *mockClient) ListPromptsByPage(ctx context.Context, req mcp.ListPromptsRequest) (*mcp.ListPromptsResult, error) {
	return nil, nil
}
func (m *mockClient) ListPrompts(ctx context.Context, req mcp.ListPromptsRequest) (*mcp.ListPromptsResult, error) {
	return &mcp.ListPromptsResult{Prompts: m.prompts}, nil
}
func (m *mockClient) GetPrompt(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	return nil, nil
}
func (m *mockClient) ListToolsByPage(ctx context.Context, req mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
	return nil, nil
}
func (m *mockClient) ListTools(ctx context.Context, req mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{Tools: m.tools}, nil
}
func (m *mockClient) CallTool(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return nil, nil
}
func (m *mockClient) SetLevel(ctx context.Context, req mcp.SetLevelRequest) error { return nil }
func (m *mockClient) Complete(ctx context.Context, req mcp.CompleteRequest) (*mcp.CompleteResult, error) {
	return nil, nil
}
func (m *mockClient) Close() error {
	m.closed = true
	return nil
}
func (m *mockClient) OnNotification(handler func(notification mcp.JSONRPCNotification)) {}

func TestRegistryNames(t *testing.T) {
	r := NewRegistry()
	r.servers["alpha"] = &ServerConnection{Name: "alpha"}
	r.servers["beta"] = &ServerConnection{Name: "beta"}

	names := r.Names()
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %d", len(names))
	}
	if names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("unexpected names: %v", names)
	}
}

func TestRegistryConnectedNames(t *testing.T) {
	r := NewRegistry()
	r.servers["up"] = &ServerConnection{
		Name:       "up",
		InitResult: &mcp.InitializeResult{},
	}
	r.servers["down"] = &ServerConnection{
		Name: "down",
		Err:  context.DeadlineExceeded,
	}

	connected := r.ConnectedNames()
	if len(connected) != 1 || connected[0] != "up" {
		t.Fatalf("expected [up], got %v", connected)
	}
}

func TestRegistryListAllTools(t *testing.T) {
	r := NewRegistry()
	r.servers["a"] = &ServerConnection{
		Name:       "a",
		InitResult: &mcp.InitializeResult{},
		Tools:      []mcp.Tool{{Name: "tool-a"}, {Name: "tool-a2"}},
	}
	r.servers["b"] = &ServerConnection{
		Name:       "b",
		InitResult: &mcp.InitializeResult{},
		Tools:      []mcp.Tool{{Name: "tool-b"}},
	}

	tools := r.ListAllTools()
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}
}

func TestRegistryFindTool(t *testing.T) {
	r := NewRegistry()
	r.servers["a"] = &ServerConnection{
		Name:       "a",
		InitResult: &mcp.InitializeResult{},
		Tools:      []mcp.Tool{{Name: "find-me"}},
	}

	entry, ok := r.FindTool("find-me")
	if !ok {
		t.Fatal("expected to find tool")
	}
	if entry.Server != "a" || entry.Tool.Name != "find-me" {
		t.Fatalf("unexpected entry: %+v", entry)
	}

	_, ok = r.FindTool("missing")
	if ok {
		t.Fatal("expected not to find tool")
	}
}

func TestRegistryClose(t *testing.T) {
	mock := &mockClient{}
	r := NewRegistry()
	r.servers["x"] = &ServerConnection{Name: "x", Client: mock}

	r.Close()
	if !mock.closed {
		t.Fatal("expected mock client to be closed")
	}
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		input string
		want  map[string]any
	}{
		{"", nil},
		{`{"key":"value"}`, map[string]any{"key": "value"}},
	}

	for _, tt := range tests {
		got, err := ParseArgs(tt.input)
		if err != nil {
			t.Fatalf("ParseArgs(%q) error: %v", tt.input, err)
		}
		if tt.want == nil && got != nil {
			t.Fatalf("ParseArgs(%q) expected nil, got %v", tt.input, got)
		}
		if tt.want != nil {
			if len(got) != len(tt.want) {
				t.Fatalf("ParseArgs(%q) expected %v, got %v", tt.input, tt.want, got)
			}
		}
	}
}

func TestParseArgsInvalidJSON(t *testing.T) {
	_, err := ParseArgs("not-json")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestConnectAllUnsupportedTransport(t *testing.T) {
	r := NewRegistry()
	cfg := &config.Config{
		Nodes: []config.NodeConfig{
			{Name: "dummy", Hostname: "localhost", SSHUser: "root"},
		},
		MCPServers: map[string]config.MCPServerConfig{
			"bad": {Transport: "unknown"},
		},
	}
	ctx := context.Background()
	r.ConnectAll(ctx, cfg)

	sc := r.Get("bad")
	if sc == nil {
		t.Fatal("expected server connection to exist")
	}
	if sc.Err == nil {
		t.Fatal("expected error for unsupported transport")
	}
}
