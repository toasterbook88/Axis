package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestClientChatStreamError400WithTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/api/show":
			w.WriteHeader(http.StatusOK)
		case "/api/chat":
			w.WriteHeader(http.StatusBadRequest)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "gemma3n:e2b")
	var buf bytes.Buffer
	msgs := []Message{{Role: RoleUser, Content: "hi"}}
	tools := []ToolDef{{
		Type: "function",
		Function: ToolDefFunction{
			Name: "axis_status",
		},
	}}

	_, err := client.ChatStream(context.Background(), msgs, tools, &buf)
	if err == nil {
		t.Fatal("expected error on 400 with tools")
	}
	if !strings.Contains(err.Error(), "400 Bad Request") {
		t.Errorf("expected 400 mention, got %v", err)
	}
	if !strings.Contains(err.Error(), "may not support tool calling") {
		t.Errorf("expected tool-calling guidance, got %v", err)
	}
	if !strings.Contains(err.Error(), "gemma3n:e2b") {
		t.Errorf("expected model name in error, got %v", err)
	}
}

func TestClientChatStreamError400WithoutTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/api/show":
			w.WriteHeader(http.StatusOK)
		case "/api/chat":
			w.WriteHeader(http.StatusBadRequest)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "testmodel")
	var buf bytes.Buffer
	msgs := []Message{{Role: RoleUser, Content: "hi"}}

	_, err := client.ChatStream(context.Background(), msgs, nil, &buf)
	if err == nil {
		t.Fatal("expected error on 400 without tools")
	}
	if !strings.Contains(err.Error(), "400 Bad Request") {
		t.Errorf("expected 400 mention, got %v", err)
	}
	if strings.Contains(err.Error(), "tool calling") {
		t.Errorf("should NOT mention tool calling when no tools were sent")
	}
}

// ---------------------------------------------------------------------------
// message.go tests
// ---------------------------------------------------------------------------

func TestConversationAppendAndMessages(t *testing.T) {
	c := NewConversation(0) // unlimited
	c.Append(Message{Role: RoleSystem, Content: "sys"})
	c.Append(Message{Role: RoleUser, Content: "hello"})
	c.Append(Message{Role: RoleAssistant, Content: "hi"})

	if c.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", c.Len())
	}

	msgs := c.Messages()
	if len(msgs) != 3 {
		t.Fatalf("Messages() len = %d, want 3", len(msgs))
	}
	if msgs[0].Role != RoleSystem || msgs[0].Content != "sys" {
		t.Errorf("msgs[0] = %+v", msgs[0])
	}
}

func TestConversationClearKeepsSystem(t *testing.T) {
	c := NewConversation(0)
	c.Append(Message{Role: RoleSystem, Content: "sys"})
	c.Append(Message{Role: RoleUser, Content: "q"})
	c.Append(Message{Role: RoleAssistant, Content: "a"})
	c.Clear()

	if c.Len() != 1 {
		t.Fatalf("after Clear(), Len() = %d, want 1", c.Len())
	}
	if c.Messages()[0].Role != RoleSystem {
		t.Fatalf("expected system message preserved")
	}
}

func TestConversationEstimateTokens(t *testing.T) {
	c := NewConversation(0)
	c.Append(Message{Role: RoleUser, Content: "abcd"}) // 4 chars = ~1 token
	if got := c.EstimateTokens(); got != 1 {
		t.Fatalf("EstimateTokens() = %d, want 1", got)
	}
}

func TestConversationCompactsOldToolResults(t *testing.T) {
	c := NewConversation(100) // 100 tokens = 400 chars max

	c.Append(Message{Role: RoleSystem, Content: "sys"})
	// Add a tool result with a large payload (index 1)
	c.Append(Message{Role: RoleTool, Content: strings.Repeat("x", 300)})
	c.Append(Message{Role: RoleAssistant, Content: "summary"})
	c.Append(Message{Role: RoleUser, Content: "q2"})
	c.Append(Message{Role: RoleAssistant, Content: "a2"})
	// Now tool msg (index 1) is outside last-4 protection (indices 2-5)
	// Push over budget with a 6th message
	c.Append(Message{Role: RoleUser, Content: strings.Repeat("z", 200)})

	// The old tool message should have been compacted
	msgs := c.Messages()
	toolMsg := msgs[1]
	if toolMsg.Role != RoleTool {
		t.Fatalf("expected tool message at index 1, got %s", toolMsg.Role)
	}
	if len(toolMsg.Content) >= 300 {
		t.Fatalf("tool content should be compacted, still has %d chars", len(toolMsg.Content))
	}
	if !strings.Contains(toolMsg.Content, "[truncated]") {
		t.Fatalf("compacted content should contain [truncated], got %q", toolMsg.Content)
	}
}

func TestConversationProtectsRecentMessages(t *testing.T) {
	c := NewConversation(50) // 50 tokens = 200 chars

	// All 4 messages are "recent" (last 4), so nothing should be compacted
	c.Append(Message{Role: RoleUser, Content: "q1"})
	c.Append(Message{Role: RoleTool, Content: strings.Repeat("x", 300)})
	c.Append(Message{Role: RoleAssistant, Content: "a1"})
	c.Append(Message{Role: RoleUser, Content: "q2"})

	// The tool message is within the last 4, so it's protected
	msgs := c.Messages()
	if len(msgs[1].Content) != 300 {
		t.Fatalf("recent tool message should NOT be compacted, len = %d", len(msgs[1].Content))
	}
}

func TestMessageToolCallSerialization(t *testing.T) {
	m := Message{
		Role:    RoleAssistant,
		Content: "",
		ToolCalls: []ToolCall{{
			Function: ToolCallFunction{
				Name:      "axis_status",
				Arguments: json.RawMessage(`{"format":"json"}`),
			},
		}},
	}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(decoded.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(decoded.ToolCalls))
	}
	if decoded.ToolCalls[0].Function.Name != "axis_status" {
		t.Fatalf("tool name = %q, want axis_status", decoded.ToolCalls[0].Function.Name)
	}
}

// ---------------------------------------------------------------------------
// system.go tests
// ---------------------------------------------------------------------------

func TestBuildSystemPromptMinimal(t *testing.T) {
	prompt := BuildSystemPrompt(nil, "")

	if !strings.Contains(prompt, "AXIS") {
		t.Fatal("prompt should mention AXIS")
	}
	if !strings.Contains(prompt, "axis status") {
		t.Fatal("prompt should list axis status command")
	}
	if strings.Contains(prompt, "Current cluster summary") {
		t.Fatal("prompt should NOT contain cluster summary when nil")
	}
}

func TestBuildSystemPromptWithCluster(t *testing.T) {
	cs := &ClusterSummaryForPrompt{
		NodeCount:      3,
		ReachableCount: 2,
		TotalRAMMB:     32768,
		FreeRAMMB:      16384,
		Status:         "degraded",
		Tools:          []string{"ollama", "git", "docker"},
	}
	prompt := BuildSystemPrompt(cs, "")

	if !strings.Contains(prompt, "3 total, 2 reachable") {
		t.Fatalf("missing node counts in prompt: %s", prompt)
	}
	if !strings.Contains(prompt, "32768 MB total") {
		t.Fatal("missing RAM info")
	}
	if !strings.Contains(prompt, "degraded") {
		t.Fatal("missing status")
	}
	if !strings.Contains(prompt, "ollama, git, docker") {
		t.Fatal("missing tools")
	}
}

func TestBuildSystemPromptWithExtra(t *testing.T) {
	prompt := BuildSystemPrompt(nil, "always respond in haiku")
	if !strings.Contains(prompt, "always respond in haiku") {
		t.Fatal("extra prompt text not included")
	}
	if !strings.Contains(prompt, "Operator instructions") {
		t.Fatal("extra section header not included")
	}
}

func TestBuildClusterSummaryNilSnapshot(t *testing.T) {
	s := BuildClusterSummary(nil)
	if s != nil {
		t.Fatalf("expected nil for nil snapshot, got %+v", s)
	}
}

func TestBuildClusterSummaryEmptyNodes(t *testing.T) {
	snap := &models.ClusterSnapshot{}
	s := BuildClusterSummary(snap)
	if s != nil {
		t.Fatalf("expected nil for empty nodes, got %+v", s)
	}
}

func TestBuildClusterSummaryExtractsTools(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Status: "healthy",
		Nodes: []models.NodeFacts{
			{
				Name: "n1",
				Tools: []models.ToolInfo{
					{Name: "git"},
					{Name: "ollama"},
				},
			},
			{
				Name: "n2",
				Tools: []models.ToolInfo{
					{Name: "git"},
					{Name: "docker"},
				},
			},
		},
		Summary: models.ClusterSummary{
			TotalNodes:     2,
			ReachableNodes: 2,
			TotalRAMMB:     16384,
			TotalFreeRAMMB: 8192,
		},
	}

	s := BuildClusterSummary(snap)
	if s == nil {
		t.Fatal("expected non-nil summary")
	}
	if s.NodeCount != 2 {
		t.Fatalf("NodeCount = %d, want 2", s.NodeCount)
	}
	if s.TotalRAMMB != 16384 {
		t.Fatalf("TotalRAMMB = %d, want 16384", s.TotalRAMMB)
	}
	// Tools should be deduplicated
	if len(s.Tools) != 3 {
		t.Fatalf("expected 3 unique tools, got %v", s.Tools)
	}
}

// ---------------------------------------------------------------------------
// client.go tests
// ---------------------------------------------------------------------------

func TestClientChatStreamTextOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
		case "/api/show":
			w.WriteHeader(http.StatusOK)
		case "/api/chat":
			if r.Method != "POST" {
				t.Errorf("expected POST, got %s", r.Method)
			}
			// Verify the request body has messages array
			var req chatRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if len(req.Messages) == 0 {
				t.Fatal("expected messages in request")
			}

			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"message":{"role":"assistant","content":"Hello"},"done":false}` + "\n"))
			w.Write([]byte(`{"message":{"role":"assistant","content":" World!"},"done":true}` + "\n"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "testmodel")

	var buf bytes.Buffer
	msgs := []Message{{Role: RoleUser, Content: "hi"}}

	result, err := client.ChatStream(context.Background(), msgs, nil, &buf)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	if buf.String() != "Hello World!" {
		t.Errorf("streamed output = %q, want 'Hello World!'", buf.String())
	}
	if result.Role != RoleAssistant {
		t.Errorf("result role = %q, want assistant", result.Role)
	}
	if result.Content != "Hello World!" {
		t.Errorf("result content = %q, want 'Hello World!'", result.Content)
	}
	if len(result.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(result.ToolCalls))
	}
}

func TestClientChatStreamWithToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/api/show":
			w.WriteHeader(http.StatusOK)
		case "/api/chat":
			w.Header().Set("Content-Type", "application/json")
			// Model returns a tool call instead of text
			tc := `{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"axis_status","arguments":{"format":"json"}}}]},"done":true}` + "\n"
			w.Write([]byte(tc))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "testmodel")
	var buf bytes.Buffer
	msgs := []Message{{Role: RoleUser, Content: "check status"}}

	result, err := client.ChatStream(context.Background(), msgs, nil, &buf)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	if buf.String() != "" {
		t.Errorf("expected no streamed text for tool call, got %q", buf.String())
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].Function.Name != "axis_status" {
		t.Errorf("tool name = %q, want axis_status", result.ToolCalls[0].Function.Name)
	}
}

func TestClientChatStreamErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/api/show":
			w.WriteHeader(http.StatusOK)
		case "/api/chat":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "testmodel")
	var buf bytes.Buffer
	msgs := []Message{{Role: RoleUser, Content: "hi"}}

	_, err := client.ChatStream(context.Background(), msgs, nil, &buf)
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention 500, got %v", err)
	}
}

func TestClientChatStreamModelMissing(t *testing.T) {
	// /api/tags returns empty — error falls back to the basic pull suggestion.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
		case "/api/show":
			w.WriteHeader(http.StatusNotFound)
		case "/api/tags":
			// Return empty model list — no installed models to suggest.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "nomodel")
	var buf bytes.Buffer
	msgs := []Message{{Role: RoleUser, Content: "hi"}}

	_, err := client.ChatStream(context.Background(), msgs, nil, &buf)
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	if !strings.Contains(err.Error(), "ollama pull nomodel") {
		t.Errorf("error should suggest pull, got %v", err)
	}
}

// TestClientChatStreamModelMissingListsAvailable verifies that when the
// requested model is absent but other models are installed, the error message
// lists them and suggests a generic --model override.
func TestClientChatStreamModelMissingListsAvailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
		case "/api/show":
			w.WriteHeader(http.StatusNotFound)
		case "/api/tags":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:4b"},{"name":"llama3.2:latest"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "nomodel")
	var buf bytes.Buffer
	msgs := []Message{{Role: RoleUser, Content: "hi"}}

	_, err := client.ChatStream(context.Background(), msgs, nil, &buf)
	if err == nil {
		t.Fatal("expected error for missing model")
	}
	if !strings.Contains(err.Error(), "llama3.2:latest") {
		t.Errorf("error should list available models, got %v", err)
	}
	if !strings.Contains(err.Error(), "--model") {
		t.Errorf("error should suggest --model flag, got %v", err)
	}
	if !strings.Contains(err.Error(), "chat.default_model") {
		t.Errorf("error should mention chat.default_model, got %v", err)
	}
	if !strings.Contains(err.Error(), "ollama pull nomodel") {
		t.Errorf("error should still suggest pull for the missing model, got %v", err)
	}
}

func TestClientChatStreamSendsToolDefs(t *testing.T) {
	var receivedTools []ToolDef

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/api/show":
			w.WriteHeader(http.StatusOK)
		case "/api/chat":
			var req chatRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			receivedTools = req.Tools
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"message":{"role":"assistant","content":"done"},"done":true}` + "\n"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "testmodel")
	tools := []ToolDef{{
		Type: "function",
		Function: ToolDefFunction{
			Name:        "axis_status",
			Description: "Get cluster status",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		},
	}}

	msgs := []Message{{Role: RoleUser, Content: "check"}}
	_, err := client.ChatStream(context.Background(), msgs, tools, nil)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	if len(receivedTools) != 1 {
		t.Fatalf("server received %d tools, want 1", len(receivedTools))
	}
	if receivedTools[0].Function.Name != "axis_status" {
		t.Errorf("tool name = %q, want axis_status", receivedTools[0].Function.Name)
	}
}

func TestClientChatStreamNilWriter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/api/show":
			w.WriteHeader(http.StatusOK)
		case "/api/chat":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"message":{"role":"assistant","content":"ok"},"done":true}` + "\n"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "testmodel")
	msgs := []Message{{Role: RoleUser, Content: "hi"}}

	// nil writer should not panic
	result, err := client.ChatStream(context.Background(), msgs, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	if result.Content != "ok" {
		t.Errorf("content = %q, want ok", result.Content)
	}
}

func TestClientEnsureRunning_ToolCapabilitiesWarning(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		showResp    string
		wantWarning bool
	}{
		{
			name:        "known tool capable model - no warning",
			model:       "qwen3.5:4b",
			showResp:    `{"modelfile":"","template":""}`,
			wantWarning: false,
		},
		{
			name:        "unknown model, template has tools - no warning",
			model:       "custom-model",
			showResp:    `{"modelfile":"","template":"{{.Tools}}"}`,
			wantWarning: false,
		},
		{
			name:        "unknown model, modelfile has tools - no warning",
			model:       "custom-model",
			showResp:    `{"modelfile":"PARAMETER tools [...]","template":""}`,
			wantWarning: false,
		},
		{
			name:        "unknown model, neither has tools - warning expected",
			model:       "custom-model",
			showResp:    `{"modelfile":"basic system message","template":"no helpers template"}`,
			wantWarning: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/":
					w.WriteHeader(http.StatusOK)
				case "/api/show":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(tt.showResp))
				default:
					t.Fatalf("unexpected path: %s", r.URL.Path)
				}
			}))
			defer server.Close()

			client := NewClient(server.URL, tt.model)
			var buf bytes.Buffer
			err := client.EnsureRunning(context.Background(), &buf)
			if err != nil {
				t.Fatalf("EnsureRunning returned error: %v", err)
			}

			hasWarning := strings.Contains(buf.String(), "may not support tool calling")
			if hasWarning != tt.wantWarning {
				t.Errorf("warning status = %v (output: %q), want %v", hasWarning, buf.String(), tt.wantWarning)
			}
		})
	}
}
