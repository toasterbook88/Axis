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
	if n := strings.Count(errW.String(), "/exit, /quit"); n != 1 {
		t.Fatalf("/help must list /exit once, got %d\n%s", n, errW.String())
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
	if shouldExit {
		t.Error("/context must not exit the REPL")
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
	if shouldExit {
		t.Error("/clear must not exit the REPL")
	}
	for _, msg := range a.Conversation().Messages() {
		if msg.Role != chat.RoleSystem {
			t.Errorf("expected conversation to be cleared of non-system messages, found role %q", msg.Role)
		}
	}

	// Test /model <name> unknown is rejected (no silent synthetic local)
	errW.Reset()
	// shouldExit is not asserted here: the command errors, and the exit
	// signal is not part of the contract on the error path.
	handled, _, err = handleREPLSlashCommand(session, "/model my-model-abc")
	if err == nil {
		t.Fatal("expected error for unknown model name")
	}
	if !handled {
		t.Error("expected /model with args to be handled")
	}
	if a.Model() == "my-model-abc" {
		t.Errorf("model should not switch on unknown name")
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

func TestHandleREPLSlashCommandFactsAndClusterUseSessionSnapshot(t *testing.T) {
	hn, err := os.Hostname()
	if err != nil || hn == "" {
		t.Skip("hostname unavailable")
	}
	a := agent.New(agent.Config{
		Endpoint:  "http://localhost:8081",
		Model:     "nemotron-3.5-lightning",
		MaxTokens: 4096,
	})
	var w, errW bytes.Buffer
	session := &agentREPLSession{
		Agent: a,
		Runtime: func(context.Context) (*runtimectx.Context, error) {
			return &runtimectx.Context{
				Snapshot: &models.ClusterSnapshot{
					Nodes: []models.NodeFacts{
						{
							Name:     "node-a",
							Hostname: hn,
							OS:       "linux",
							Arch:     "amd64",
							Status:   models.StatusComplete,
							ResidentModels: []models.ResidentModel{
								{Name: "NVIDIA-Nemotron-3.5-Lightning-30B-A3B-IQ4_XS", Runtime: "llama.cpp", Port: 8081},
							},
						},
						{Name: "node-b", Hostname: "node-b", Status: models.StatusComplete, OS: "linux", Arch: "amd64"},
					},
				},
				Config: &config.Config{},
			}, nil
		},
		Out:    &w,
		ErrOut: &errW,
	}

	handled, shouldExit, err := handleREPLSlashCommand(session, "/facts")
	if err != nil {
		t.Fatalf("/facts: %v", err)
	}
	if !handled || shouldExit {
		t.Fatalf("/facts handled=%v exit=%v", handled, shouldExit)
	}
	factsOut := w.String() + errW.String()
	if !strings.Contains(factsOut, "8081") {
		t.Fatalf("/facts must print probed resident port 8081, got %q", factsOut)
	}
	if !strings.Contains(factsOut, "NVIDIA-Nemotron") {
		t.Fatalf("/facts must print resident name, got %q", factsOut)
	}

	w.Reset()
	errW.Reset()
	handled, shouldExit, err = handleREPLSlashCommand(session, "/cluster")
	if err != nil {
		t.Fatalf("/cluster must use session snapshot, not fail: %v", err)
	}
	if !handled || shouldExit {
		t.Fatalf("/cluster handled=%v exit=%v", handled, shouldExit)
	}
	clusterOut := w.String() + errW.String()
	if !strings.Contains(clusterOut, "node-a") || !strings.Contains(clusterOut, "node-b") {
		t.Fatalf("/cluster must list snapshot nodes, got %q", clusterOut)
	}
}

func TestHandleREPLSlashCommandExecDeferred(t *testing.T) {
	a := agent.New(agent.Config{Endpoint: "http://localhost:11434", Model: "x", MaxTokens: 4096})
	var w, errW bytes.Buffer
	session := &agentREPLSession{
		Agent: a,
		Runtime: func(context.Context) (*runtimectx.Context, error) {
			return nil, nil
		},
		Out:    &w,
		ErrOut: &errW,
	}
	handled, shouldExit, err := handleREPLSlashCommand(session, "/exec echo hi")
	if err != nil {
		t.Fatalf("/exec must not error in v1, got %v", err)
	}
	if handled || shouldExit {
		t.Fatalf("/exec is deferred; handled=%v exit=%v", handled, shouldExit)
	}
}

func TestPrintAgentSessionDetailsIncludesStatusStrip(t *testing.T) {
	var buf bytes.Buffer
	printAgentSessionDetails(&buf, ModelChoice{
		Model:    "nemotron-3.5-lightning",
		Endpoint: "http://localhost:8081",
		Protocol: agent.ProtocolOpenAI,
	}, false, "", 0, 10)
	got := buf.String()
	if !strings.Contains(got, "[model:") || !strings.Contains(got, "nemotron-3.5-lightning") {
		t.Fatalf("missing model strip: %q", got)
	}
	if !strings.Contains(got, "[endpoint:") || !strings.Contains(got, "localhost:8081") {
		t.Fatalf("missing endpoint strip: %q", got)
	}
	if !strings.Contains(got, "[status:") {
		t.Fatalf("missing status strip: %q", got)
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
		ID:            "remote-node:ollama:gemma:7b",
		Model:         "gemma:7b",
		Protocol:      agent.ProtocolOllama,
		ProviderName:  "ollama",
		ProviderKind:  "local",
		Node:          "remote-node",
		Endpoint:      "http://192.168.1.100:11434",
		SecurityClass: agent.BackendRemote,
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
