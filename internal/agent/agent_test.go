package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

// Mock types mirroring the chat package's internal streaming types.
type mockChunkMessage struct {
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	ToolCalls []chat.ToolCall `json:"tool_calls,omitempty"`
}

type mockStreamChunk struct {
	Message mockChunkMessage `json:"message"`
	Done    bool             `json:"done"`
	// Final-chunk token counts, matching chat.chatStreamChunk.
	PromptEvalCount int `json:"prompt_eval_count,omitempty"`
	EvalCount       int `json:"eval_count,omitempty"`
}

// --- Helpers ---

// mockOllamaChat creates a test server that returns canned responses.
// Each call to the returned function pops the next response from the queue.
func mockOllamaChat(t *testing.T, responses [][]mockStreamChunk) *httptest.Server {
	t.Helper()
	idx := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/" && r.Method == http.MethodGet:
			// Health check.
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/show" && r.Method == http.MethodPost:
			// Model availability check.
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"modelfile":"test"}`)
		case r.URL.Path == "/api/chat" && r.Method == http.MethodPost:
			if idx >= len(responses) {
				t.Fatalf("mock server: no more canned responses (call #%d)", idx)
			}
			chunks := responses[idx]
			idx++
			w.Header().Set("Content-Type", "application/x-ndjson")
			for _, chunk := range chunks {
				data, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "%s\n", data)
			}
		default:
			t.Logf("mock server: unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// textResponse creates streaming chunks that return a plain text answer.
func textResponse(text string) []mockStreamChunk {
	return []mockStreamChunk{
		{Message: mockChunkMessage{Role: "assistant", Content: text}, Done: false},
		{Message: mockChunkMessage{Role: "assistant"}, Done: true},
	}
}

// toolCallResponse creates streaming chunks with a tool call.
func toolCallResponse(name string, args string) []mockStreamChunk {
	return []mockStreamChunk{
		{
			Message: mockChunkMessage{
				Role: "assistant",
				ToolCalls: []chat.ToolCall{{
					Function: chat.ToolCallFunction{
						Name:      name,
						Arguments: json.RawMessage(args),
					},
				}},
			},
			Done: false,
		},
		{Message: mockChunkMessage{Role: "assistant"}, Done: true},
	}
}

// alwaysConfirm auto-approves everything (for testing).
func alwaysConfirm() ConfirmFunc {
	return func(toolName, description string, safetyScore int) ConfirmResult {
		return ConfirmYes
	}
}

// neverConfirm declines everything (for testing).
func neverConfirm() ConfirmFunc {
	return func(toolName, description string, safetyScore int) ConfirmResult {
		return ConfirmNo
	}
}

// capturingConfirm stores the description it was asked to confirm.
func capturingConfirm(result *string) ConfirmFunc {
	return func(toolName, description string, safetyScore int) ConfirmResult {
		if result != nil {
			*result = description
		}
		return ConfirmYes
	}
}

// --- Tool Registry Tests ---

func TestToolShellBlockedByDesign(t *testing.T) {
	tc := NewToolContext(&RuntimeView{}, nil)
	r := NewToolRegistry(tc)

	_, err := r.Execute(context.Background(), "run_shell", json.RawMessage(`{"command":"echo hello"}`))
	if err == nil {
		t.Fatal("expected error — run_shell must go through agent safety gate")
	}
	if !strings.Contains(err.Error(), "safety gate") {
		t.Errorf("expected 'safety gate' error, got: %s", err.Error())
	}
}

func TestToolListDirectoryPathValidation(t *testing.T) {
	tc := NewToolContext(&RuntimeView{}, nil)
	r := NewToolRegistry(tc)

	_, err := r.Execute(context.Background(), "list_directory", json.RawMessage(`{"path":"/etc/shadow"}`))
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("expected 'escapes' error, got: %s", err.Error())
	}
}

func TestToolWriteFileConfirmationUsesNewFilePreview(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	var description string
	confirm := capturingConfirm(&description)
	agent := New(Config{
		Endpoint:    "http://unused.example.com",
		Model:       "unused",
		Confirm:     confirm,
		ToolContext: &ToolContext{},
	})

	_, err := agent.dispatchToolCall(context.Background(), chat.ToolCall{
		Function: chat.ToolCallFunction{
			Name:      "write_file",
			Arguments: json.RawMessage(`{"path":"new-dir/new.txt","content":"line1\nline2\nline3"}`),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(description, "Create new file") && !strings.Contains(description, "new-dir/new.txt") {
		t.Errorf("expected new-file preview description, got: %s", description)
	}
}

// --- Confirmation Tests ---

func TestDefaultConfirmYes(t *testing.T) {
	r := strings.NewReader("y\n")
	var w bytes.Buffer
	confirm := DefaultConfirm(r, &w)

	result := confirm("run_shell", "echo hello", 0)
	if result != ConfirmYes {
		t.Errorf("expected ConfirmYes, got %d", result)
	}
}

func TestDefaultConfirmNo(t *testing.T) {
	r := strings.NewReader("n\n")
	var w bytes.Buffer
	confirm := DefaultConfirm(r, &w)

	result := confirm("run_shell", "echo hello", 0)
	if result != ConfirmNo {
		t.Errorf("expected ConfirmNo, got %d", result)
	}
}

func TestDefaultConfirmAlways(t *testing.T) {
	r := strings.NewReader("a\n")
	var w bytes.Buffer
	confirm := DefaultConfirm(r, &w)

	result := confirm("run_shell", "echo hello", 0)
	if result != ConfirmAlways {
		t.Errorf("expected ConfirmAlways, got %d", result)
	}
}

func TestDefaultConfirmNever(t *testing.T) {
	r := strings.NewReader("v\n")
	var w bytes.Buffer
	confirm := DefaultConfirm(r, &w)

	result := confirm("run_shell", "echo hello", 0)
	if result != ConfirmNever {
		t.Errorf("expected ConfirmNever, got %d", result)
	}
}

func TestDefaultConfirmHighRiskPrefix(t *testing.T) {
	r := strings.NewReader("n\n")
	var w bytes.Buffer
	confirm := DefaultConfirm(r, &w)

	confirm("run_shell", "rm -rf /tmp/stuff", 75)
	if !strings.Contains(w.String(), "[HIGH RISK]") {
		t.Errorf("expected [HIGH RISK] prefix for score >= 70, got: %s", w.String())
	}
}

func TestAgentShellBlockedBySafety(t *testing.T) {
	server := mockOllamaChat(t, [][]mockStreamChunk{
		toolCallResponse("run_shell", `{"command":"rm -rf /"}`),
		textResponse("That command was blocked for safety."),
	})
	defer server.Close()

	var out bytes.Buffer
	agent := New(Config{
		Endpoint:    server.URL,
		Model:       "test-model",
		Output:      &out,
		Confirm:     neverConfirm(),
		ToolContext: &ToolContext{},
	})

	err := agent.Run(context.Background(), "delete everything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "blocked") && !strings.Contains(output, "declined") {
		t.Errorf("expected safety block or decline message for rm -rf /, got: %s", output)
	}
}

func TestAgentShellBlockedBySafetyOverride(t *testing.T) {
	server := mockOllamaChat(t, [][]mockStreamChunk{
		toolCallResponse("run_shell", `{"command":"rm -rf /"}`),
		textResponse("Safety override succeeded."),
	})
	defer server.Close()

	var out bytes.Buffer
	called := false
	agent := New(Config{
		Endpoint:    server.URL,
		Model:       "test-model",
		Output:      &out,
		Confirm:     alwaysConfirm(),
		ToolContext: &ToolContext{},
		RunShell: func(ctx context.Context, command string) (string, error) {
			called = true
			if command != "rm -rf /" {
				t.Fatalf("expected command 'rm -rf /', got %q", command)
			}
			return "mock override success", nil
		},
	})

	err := agent.Run(context.Background(), "delete everything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected safety override shell command to execute")
	}
}

func TestDispatchRunOnNodeUsesGuardedRunner(t *testing.T) {
	var sawNode, sawCmd string
	a := New(Config{
		Confirm: alwaysConfirm(),
		Output:  &bytes.Buffer{},
		RunOnNode: func(_ context.Context, node, command string) (string, error) {
			sawNode, sawCmd = node, command
			return `{"ok":true,"node":"nixos","output":"Linux"}`, nil
		},
	})
	args := json.RawMessage(mustJSON(t, map[string]any{"node": "nixos", "command": "uname -a"}))
	out, err := a.dispatchRunOnNode(context.Background(), args)
	if err != nil {
		t.Fatalf("dispatchRunOnNode: %v", err)
	}
	if sawNode != "nixos" || sawCmd != "uname -a" {
		t.Fatalf("runner saw node=%q cmd=%q", sawNode, sawCmd)
	}
	if !strings.Contains(out, "Linux") {
		t.Fatalf("out = %q", out)
	}

	// Registry direct execute must not bypass the agent gate.
	_, err = a.tools.Execute(context.Background(), "run_on_node", args)
	if err == nil || !strings.Contains(err.Error(), "safety gate") {
		t.Fatalf("expected registry safety-gate error, got %v", err)
	}
}

func TestExecuteShellExitError(t *testing.T) {
	out, err := ExecuteShell(context.Background(), "exit 1")
	if err != nil {
		t.Fatalf("non-timeout errors should not return Go error, got: %v", err)
	}
	if !strings.Contains(out, "[exit error]") {
		t.Errorf("expected exit error annotation, got: %s", out)
	}
}
