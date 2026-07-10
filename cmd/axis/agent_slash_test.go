package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/ui"
)

type mockSelector struct {
	result ui.SelectResult
	err    error
}

func (m *mockSelector) Select(ctx context.Context, title string, options []ui.SelectOption) (ui.SelectResult, error) {
	return m.result, m.err
}

func TestHandleREPLSlashCommand(t *testing.T) {
	a := agent.New(agent.Config{
		Endpoint:  "http://localhost:11434",
		Model:     "granite3.1-moe:1b",
		MaxTokens: 4096,
	})

	var w, errW bytes.Buffer
	session := &agentREPLSession{
		Agent:       a,
		MCPRegistry: nil,
		Runtime: func(context.Context) (*runtimectx.Context, error) {
			return nil, nil
		},
		Selector: nil,
		In:       nil,
		Out:      &w,
		ErrOut:   &errW,
	}

	// Test /help
	handled, shouldExit, err := handleREPLSlashCommand(session, "/help")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Error("expected /help to be handled")
	}
	if shouldExit {
		t.Error("expected /help not to cause exit")
	}
	if !strings.Contains(errW.String(), "Available commands:") {
		t.Errorf("expected help output, got %q", errW.String())
	}

	// Test /context
	errW.Reset()
	handled, shouldExit, err = handleREPLSlashCommand(session, "/context")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Error("expected /context to be handled")
	}
	if !strings.Contains(errW.String(), "Tokens used:") {
		t.Errorf("expected context output, got %q", errW.String())
	}

	// Test /clear
	errW.Reset()
	a.Conversation().Append(chat.Message{Role: chat.RoleUser, Content: "hello"})
	if a.Conversation().Len() <= 1 {
		t.Fatal("expected conversation to have messages")
	}
	handled, shouldExit, err = handleREPLSlashCommand(session, "/clear")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Error("expected /clear to be handled")
	}
	for _, msg := range a.Conversation().Messages() {
		if msg.Role != chat.RoleSystem {
			t.Errorf("expected conversation to be cleared of non-system messages, found role %q", msg.Role)
		}
	}

	// Test /model <name> (direct switch)
	errW.Reset()
	handled, shouldExit, err = handleREPLSlashCommand(session, "/model my-model-abc")
	if err != nil {
		t.Fatalf("unexpected error on direct model switch: %v", err)
	}
	if !handled {
		t.Error("expected /model with args to be handled")
	}
	if !strings.Contains(errW.String(), "Switched to local Ollama model") {
		t.Errorf("expected switch log message, got %q", errW.String())
	}
	if a.Model() != "my-model-abc" {
		t.Errorf("expected active model to switch to my-model-abc, got %q", a.Model())
	}

	// Test /exit
	handled, shouldExit, err = handleREPLSlashCommand(session, "/exit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled || !shouldExit {
		t.Error("expected /exit to handle and cause exit")
	}
}

func TestHandleREPLSlashCommandModelsInteractive(t *testing.T) {
	a := agent.New(agent.Config{
		Endpoint:  "http://localhost:11434",
		Model:     "granite3.1-moe:1b",
		MaxTokens: 4096,
	})

	var w, errW bytes.Buffer
	sel := &mockSelector{
		result: ui.SelectResult{
			ID:       "cloud:openai:gpt-4o",
			Index:    0,
			Selected: true,
		},
	}

	cfg := &config.Config{
		AIProviders: map[string]config.AIProviderConfig{
			"openai": {
				Enabled:  true,
				Type:     "cloud",
				Kind:     "openai",
				Endpoint: "https://api.openai.com/v1",
				Models: []config.AIModelConfig{
					{Name: "gpt-4o"},
				},
				APIKeyEnv: "OPENAI_API_KEY",
			},
		},
	}
	os.Setenv("OPENAI_API_KEY", "test-key-123")
	defer os.Unsetenv("OPENAI_API_KEY")

	session := &agentREPLSession{
		Agent: a,
		Runtime: func(context.Context) (*runtimectx.Context, error) {
			return &runtimectx.Context{
				Config: cfg,
			}, nil
		},
		Selector: sel,
		In:       nil,
		Out:      &w,
		ErrOut:   &errW,
	}

	handled, shouldExit, err := handleREPLSlashCommand(session, "/models")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled || shouldExit {
		t.Errorf("unexpected handled/shouldExit: handled=%t, shouldExit=%t", handled, shouldExit)
	}
	if a.Model() != "gpt-4o" {
		t.Errorf("expected active model to switch to gpt-4o, got %q", a.Model())
	}
}

func TestResolveNodeEndpoint(t *testing.T) {
	// Remote node with valid IP address
	remoteNode := models.NodeFacts{
		Name: "remote-node",
		Ollama: &models.OllamaInfo{
			Installed: true,
			Port:      11434,
		},
		Hostname: "remote-host",
		Addresses: []models.NetworkAddress{
			{Address: "192.168.1.100", Scope: "global"},
		},
	}
	// 1. Should pick SSHTarget if available
	endpoint, err := resolveNodeEndpoint(remoteNode, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if endpoint != "http://192.168.1.100:11434" {
		t.Errorf("unexpected remote endpoint: %s", endpoint)
	}

	// Remote node with no addresses but has hostname
	remoteNodeNoAddr := models.NodeFacts{
		Name: "remote-node-no-addr",
		Ollama: &models.OllamaInfo{
			Installed: true,
			Port:      11434,
		},
		Hostname: "remote-host-only",
	}
	// 2. Should fallback to Hostname if no valid addresses
	endpoint, err = resolveNodeEndpoint(remoteNodeNoAddr, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if endpoint != "http://remote-host-only:11434" {
		t.Errorf("unexpected remote endpoint: %s", endpoint)
	}

	// Remote node with no addresses and no hostname
	remoteNodeInvalid := models.NodeFacts{
		Name: "remote-node-invalid",
		Ollama: &models.OllamaInfo{
			Installed: true,
			Port:      11434,
		},
	}
	// 3. Should return error if no valid addresses and no hostname
	_, err = resolveNodeEndpoint(remoteNodeInvalid, 0)
	if err == nil {
		t.Fatal("expected error resolving remote node with no valid address or hostname")
	}
}

func TestSwitchAgentToModelChoiceRemoteOllama(t *testing.T) {
	a := agent.New(agent.Config{
		Endpoint:  "http://localhost:11434",
		Model:     "granite3.1-moe:1b",
		MaxTokens: 4096,
	})

	var w, errW bytes.Buffer
	session := &agentREPLSession{
		Agent: a,
		Runtime: func(context.Context) (*runtimectx.Context, error) {
			return nil, nil
		},
		Out:    &w,
		ErrOut: &errW,
	}

	choice := ModelChoice{
		ID:            "remote-node:gemma:7b",
		Model:         "gemma:7b",
		ProviderName:  "ollama",
		ProviderKind:  "local",
		Node:          "remote-node",
		Endpoint:      "http://192.168.1.100:11434",
		SecurityClass: agent.BackendLocal,
	}

	err := switchAgentToModelChoice(session, choice)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify that the backend is set to the remote Ollama endpoint, not chat.DefaultEndpoint
	backend := a.Backend()
	if backend == nil {
		t.Fatal("expected agent backend to be set")
	}

	client, ok := backend.(*chat.Client)
	if !ok {
		t.Fatalf("expected backend to be *chat.Client, got %T", backend)
	}

	if client.Endpoint != "http://192.168.1.100:11434" {
		t.Errorf("expected backend endpoint http://192.168.1.100:11434, got %q", client.Endpoint)
	}
	if client.Model != "gemma:7b" {
		t.Errorf("expected backend model gemma:7b, got %q", client.Model)
	}
}

type mockMCPClient struct {
	mcpgo.MCPClient
	pingErr error
}

func (m *mockMCPClient) Ping(ctx context.Context) error {
	return m.pingErr
}

type multiMockSelector struct {
	results []ui.SelectResult
	idx     int
}

func (m *multiMockSelector) Select(ctx context.Context, title string, options []ui.SelectOption) (ui.SelectResult, error) {
	if m.idx >= len(m.results) {
		return ui.SelectResult{Selected: false}, nil
	}
	res := m.results[m.idx]
	m.idx++
	return res, nil
}

func TestMCPAgentMenu(t *testing.T) {
	a := agent.New(agent.Config{
		Endpoint:  "http://localhost:11434",
		Model:     "granite3.1-moe:1b",
		MaxTokens: 4096,
	})

	mcpReg := mcpclient.NewRegistry()

	// 1. Create a fake connected server with tools and resources
	conn := &mcpclient.ServerConnection{
		Name:      "test-server",
		Transport: "stdio",
		InitResult: &mcp.InitializeResult{
			ProtocolVersion: "2024-11-05",
			ServerInfo: mcp.Implementation{
				Name:    "Test Server",
				Version: "1.0.0",
			},
		},
		Tools: []mcp.Tool{
			{Name: "test-tool", Description: "A test tool"},
		},
		Resources: []mcp.Resource{
			{Name: "test-resource", URI: "file:///test", Description: "A test resource"},
		},
		Client: &mockMCPClient{pingErr: nil},
	}
	mcpReg.Add(conn)

	// 2. Set up our mock selector to navigate the menu:
	//    - First select: choose server "test-server" (index 0)
	//    - Second select: choose "List Tools" (index 0, ID "tools")
	//    - Third select: choose "List Resources" (index 1, ID "resources")
	//    - Fourth select: choose "Show Server Status & Diagnostics" (index 2, ID "diagnostics")
	//    - Fifth select: choose "Back" (index 3, ID "back")
	//    - Sixth select: choose cancel (Selected: false) to exit the server menu loop
	choices := []ui.SelectResult{
		{ID: "test-server", Index: 0, Selected: true}, // Select server
		{ID: "tools", Index: 0, Selected: true},       // List Tools
		{ID: "resources", Index: 1, Selected: true},   // List Resources
		{ID: "diagnostics", Index: 2, Selected: true}, // Diagnostics
		{ID: "back", Index: 3, Selected: true},        // Back (should return to server list)
		{Selected: false},                             // Cancel server menu loop
	}

	mockSel := &multiMockSelector{results: choices}

	var w, errW bytes.Buffer
	session := &agentREPLSession{
		Agent:       a,
		MCPRegistry: mcpReg,
		Runtime: func(context.Context) (*runtimectx.Context, error) {
			return nil, nil
		},
		Selector: mockSel,
		Out:      &w,
		ErrOut:   &errW,
	}

	handled, shouldExit, err := handleREPLSlashCommand(session, "/mcp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Error("expected /mcp to be handled")
	}
	if shouldExit {
		t.Error("expected /mcp not to cause exit")
	}

	output := w.String()
	// Assert List Tools outputs tools
	if !strings.Contains(output, "Tools exposed by test-server:") {
		t.Errorf("expected tools output, got: %q", output)
	}
	if !strings.Contains(output, "test-tool") {
		t.Errorf("expected test-tool in output, got: %q", output)
	}

	// Assert List Resources outputs resources
	if !strings.Contains(output, "Resources exposed by test-server:") {
		t.Errorf("expected resources output, got: %q", output)
	}
	if !strings.Contains(output, "test-resource") {
		t.Errorf("expected test-resource in output, got: %q", output)
	}

	// Assert Diagnostics outputs details & successful ping
	if !strings.Contains(output, "MCP Server Details: test-server") {
		t.Errorf("expected diagnostics details, got: %q", output)
	}
	stripped := ui.StripANSIAndControls(output)
	if !strings.Contains(stripped, "Status:   connected") {
		t.Errorf("expected successful connected status, got: %q", stripped)
	}
}
