package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/models"
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

// --- Tool Registry Tests ---

func TestToolRegistryHasAllDefaultTools(t *testing.T) {
	tc := &ToolContext{}
	r := NewToolRegistry(tc)

	expected := []string{"axis_status", "axis_facts", "axis_place", "run_shell"}
	for _, name := range expected {
		if !r.HasTool(name) {
			t.Errorf("expected tool %q to be registered", name)
		}
	}
	if len(r.Defs()) != len(expected) {
		t.Errorf("expected %d tool defs, got %d", len(expected), len(r.Defs()))
	}
}

func TestToolRegistryUnknownTool(t *testing.T) {
	tc := &ToolContext{}
	r := NewToolRegistry(tc)

	_, err := r.Execute(context.Background(), "nonexistent_tool", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("error should mention 'unknown tool', got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "axis_status") {
		t.Errorf("error should list available tools, got: %s", err.Error())
	}
}

func TestToolStatusNilSnapshot(t *testing.T) {
	tc := &ToolContext{Snapshot: nil}
	r := NewToolRegistry(tc)

	result, err := r.Execute(context.Background(), "axis_status", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "no snapshot available") {
		t.Errorf("expected 'no snapshot available', got: %s", result)
	}
}

func TestToolStatusWithSnapshot(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Status: "healthy",
		Nodes:  []models.NodeFacts{{Name: "test-node"}},
	}
	tc := &ToolContext{Snapshot: snap}
	r := NewToolRegistry(tc)

	result, err := r.Execute(context.Background(), "axis_status", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "test-node") {
		t.Errorf("expected snapshot JSON with 'test-node', got: %s", result)
	}
}

func TestToolPlaceMissingArgs(t *testing.T) {
	tc := &ToolContext{}
	r := NewToolRegistry(tc)

	_, err := r.Execute(context.Background(), "axis_place", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing description")
	}
	if !strings.Contains(err.Error(), "non-empty") {
		t.Errorf("expected 'non-empty' error, got: %s", err.Error())
	}
}

func TestToolPlaceMalformedJSON(t *testing.T) {
	tc := &ToolContext{}
	r := NewToolRegistry(tc)

	_, err := r.Execute(context.Background(), "axis_place", json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "invalid arguments") {
		t.Errorf("expected 'invalid arguments' error, got: %s", err.Error())
	}
}

func TestToolShellBlockedByDesign(t *testing.T) {
	tc := &ToolContext{}
	r := NewToolRegistry(tc)

	_, err := r.Execute(context.Background(), "run_shell", json.RawMessage(`{"command":"echo hello"}`))
	if err == nil {
		t.Fatal("expected error — run_shell must go through agent safety gate")
	}
	if !strings.Contains(err.Error(), "safety gate") {
		t.Errorf("expected 'safety gate' error, got: %s", err.Error())
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

func TestAutoApproveReadOnlyTools(t *testing.T) {
	called := false
	fallback := func(toolName, description string, safetyScore int) ConfirmResult {
		called = true
		return ConfirmNo
	}

	confirm := AutoApproveConfirm(70, fallback)

	for _, tool := range []string{"axis_status", "axis_facts", "axis_place"} {
		result := confirm(tool, "test", 0)
		if result != ConfirmYes {
			t.Errorf("read-only tool %q should auto-approve", tool)
		}
	}
	if called {
		t.Error("fallback should not be called for read-only tools")
	}
}

func TestAutoApproveLowRiskShell(t *testing.T) {
	confirm := AutoApproveConfirm(70, neverConfirm())

	result := confirm("run_shell", "echo hello", 10)
	if result != ConfirmYes {
		t.Error("low-risk shell (score 10 < threshold 70) should auto-approve")
	}
}

func TestAutoApproveHighRiskShellDelegatesToFallback(t *testing.T) {
	confirm := AutoApproveConfirm(70, neverConfirm())

	result := confirm("run_shell", "rm important_file", 75)
	if result != ConfirmNo {
		t.Error("high-risk shell (score 75 >= threshold 70) should delegate to fallback")
	}
}

// --- Agent Loop Tests ---

func TestAgentSimpleTextResponse(t *testing.T) {
	server := mockOllamaChat(t, [][]mockStreamChunk{
		textResponse("Hello, operator!"),
	})
	defer server.Close()

	var out bytes.Buffer
	agent := New(Config{
		Endpoint: server.URL,
		Model:    "test-model",
		Output:   &out,
		Confirm:  alwaysConfirm(),
	})

	err := agent.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "Hello, operator!") {
		t.Errorf("expected 'Hello, operator!' in output, got: %s", out.String())
	}
}

func TestAgentToolCallThenTextResponse(t *testing.T) {
	server := mockOllamaChat(t, [][]mockStreamChunk{
		// Turn 1: model requests axis_status.
		toolCallResponse("axis_status", `{}`),
		// Turn 2: model produces text after seeing tool result.
		textResponse("The cluster has 1 node."),
	})
	defer server.Close()

	snap := &models.ClusterSnapshot{
		Status: "healthy",
		Nodes:  []models.NodeFacts{{Name: "test-node"}},
	}

	var out bytes.Buffer
	agent := New(Config{
		Endpoint:    server.URL,
		Model:       "test-model",
		Output:      &out,
		Confirm:     alwaysConfirm(),
		ToolContext: &ToolContext{Snapshot: snap},
	})

	err := agent.Run(context.Background(), "how many nodes?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "axis_status") {
		t.Errorf("expected tool trace for axis_status, got: %s", output)
	}
	if !strings.Contains(output, "The cluster has 1 node") {
		t.Errorf("expected final text response, got: %s", output)
	}
}

func TestAgentMaxTurnsStop(t *testing.T) {
	// Model keeps requesting tools forever — agent should stop at maxTurns.
	responses := make([][]mockStreamChunk, 5)
	for i := range responses {
		responses[i] = toolCallResponse("axis_status", `{}`)
	}

	server := mockOllamaChat(t, responses)
	defer server.Close()

	var out bytes.Buffer
	agent := New(Config{
		Endpoint:    server.URL,
		Model:       "test-model",
		MaxTurns:    3,
		Output:      &out,
		Confirm:     alwaysConfirm(),
		ToolContext: &ToolContext{},
	})

	err := agent.Run(context.Background(), "keep going")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "maximum turns") {
		t.Errorf("expected max turns warning, got: %s", out.String())
	}
}

// --- Adversarial Tests ---

func TestAgentHallucinatedToolName(t *testing.T) {
	server := mockOllamaChat(t, [][]mockStreamChunk{
		// Turn 1: model hallucinates a nonexistent tool.
		toolCallResponse("imaginary_tool", `{"param":"value"}`),
		// Turn 2: after error feedback, model gives text.
		textResponse("Sorry, let me just tell you directly."),
	})
	defer server.Close()

	var out bytes.Buffer
	agent := New(Config{
		Endpoint:    server.URL,
		Model:       "test-model",
		Output:      &out,
		Confirm:     alwaysConfirm(),
		ToolContext: &ToolContext{},
	})

	err := agent.Run(context.Background(), "do something weird")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "unknown tool") {
		t.Errorf("expected 'unknown tool' error feedback, got: %s", output)
	}
	if !strings.Contains(output, "imaginary_tool") {
		t.Errorf("expected hallucinated tool name in error, got: %s", output)
	}
	// Agent should recover and produce final text.
	if !strings.Contains(output, "Sorry") {
		t.Errorf("expected recovery text response, got: %s", output)
	}
}

func TestAgentMalformedJSONArgs(t *testing.T) {
	// Model sends valid JSON that can't unmarshal into the tool's struct.
	server := mockOllamaChat(t, [][]mockStreamChunk{
		toolCallResponse("axis_place", `"just a string not an object"`),
		textResponse("Let me try a different approach."),
	})
	defer server.Close()

	var out bytes.Buffer
	agent := New(Config{
		Endpoint:    server.URL,
		Model:       "test-model",
		Output:      &out,
		Confirm:     alwaysConfirm(),
		ToolContext: &ToolContext{},
	})

	err := agent.Run(context.Background(), "place something")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "invalid arguments") {
		t.Errorf("expected 'invalid arguments' error, got: %s", output)
	}
	// Should recover.
	if !strings.Contains(output, "different approach") {
		t.Errorf("expected recovery text, got: %s", output)
	}
}

func TestAgentWrongArgTypes(t *testing.T) {
	// axis_place expects {"description": "string"}, model sends {"description": 42}.
	server := mockOllamaChat(t, [][]mockStreamChunk{
		toolCallResponse("axis_place", `{"description": 42}`),
		textResponse("I see, let me fix that."),
	})
	defer server.Close()

	var out bytes.Buffer
	agent := New(Config{
		Endpoint:    server.URL,
		Model:       "test-model",
		Output:      &out,
		Confirm:     alwaysConfirm(),
		ToolContext: &ToolContext{},
	})

	err := agent.Run(context.Background(), "place a task")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	// json.Unmarshal into string from int produces an error.
	if !strings.Contains(output, "invalid arguments") || !strings.Contains(output, "⚠") {
		t.Errorf("expected error feedback for wrong arg type, got: %s", output)
	}
}

func TestAgentEmptyToolCalls(t *testing.T) {
	// Model sends a tool_call with empty function name.
	server := mockOllamaChat(t, [][]mockStreamChunk{
		toolCallResponse("", `{}`),
		textResponse("Never mind."),
	})
	defer server.Close()

	var out bytes.Buffer
	agent := New(Config{
		Endpoint:    server.URL,
		Model:       "test-model",
		Output:      &out,
		Confirm:     alwaysConfirm(),
		ToolContext: &ToolContext{},
	})

	err := agent.Run(context.Background(), "test empty")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "unknown tool") {
		t.Errorf("expected 'unknown tool' error for empty name, got: %s", output)
	}
}

func TestAgentShellDeclinedByOperator(t *testing.T) {
	server := mockOllamaChat(t, [][]mockStreamChunk{
		toolCallResponse("run_shell", `{"command":"echo hello"}`),
		textResponse("OK, I won't run that."),
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

	err := agent.Run(context.Background(), "run something")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "declined") {
		t.Errorf("expected 'declined' message, got: %s", output)
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
		Confirm:     alwaysConfirm(),
		ToolContext: &ToolContext{},
	})

	err := agent.Run(context.Background(), "delete everything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "blocked") {
		t.Errorf("expected 'blocked' message for rm -rf /, got: %s", output)
	}
}

func TestAgentShellExecutesWhenApproved(t *testing.T) {
	server := mockOllamaChat(t, [][]mockStreamChunk{
		toolCallResponse("run_shell", `{"command":"echo agent-test-output"}`),
		textResponse("Done."),
	})
	defer server.Close()

	var out bytes.Buffer
	agent := New(Config{
		Endpoint:    server.URL,
		Model:       "test-model",
		Output:      &out,
		Confirm:     alwaysConfirm(),
		ToolContext: &ToolContext{},
	})

	err := agent.Run(context.Background(), "echo test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check the conversation has the shell result.
	msgs := agent.Conversation().Messages()
	found := false
	for _, m := range msgs {
		if m.Role == chat.RoleTool && strings.Contains(m.Content, "agent-test-output") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected shell output 'agent-test-output' in conversation")
	}
}

func TestAgentNeverBlocksAllFutureShell(t *testing.T) {
	// First call: operator selects "never" → second shell call should auto-block.
	neverOnce := func() ConfirmFunc {
		return func(toolName, description string, safetyScore int) ConfirmResult {
			return ConfirmNever
		}
	}

	server := mockOllamaChat(t, [][]mockStreamChunk{
		toolCallResponse("run_shell", `{"command":"echo first"}`),
		toolCallResponse("run_shell", `{"command":"echo second"}`),
		textResponse("All done."),
	})
	defer server.Close()

	var out bytes.Buffer
	agent := New(Config{
		Endpoint:    server.URL,
		Model:       "test-model",
		Output:      &out,
		Confirm:     neverOnce(),
		ToolContext: &ToolContext{},
	})

	err := agent.Run(context.Background(), "run two commands")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "blocked all shell commands") {
		t.Errorf("expected session block message, got: %s", output)
	}
}

func TestExecuteShellCaptures(t *testing.T) {
	out, err := ExecuteShell(context.Background(), "echo hello-world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "hello-world") {
		t.Errorf("expected 'hello-world' in output, got: %s", out)
	}
}

func TestExecuteShellCapturesStderr(t *testing.T) {
	out, err := ExecuteShell(context.Background(), "echo err-msg >&2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "[stderr]") || !strings.Contains(out, "err-msg") {
		t.Errorf("expected stderr capture, got: %s", out)
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

// --- isReadOnlyTool Tests ---

func TestIsReadOnlyTool(t *testing.T) {
	readOnly := []string{"axis_status", "axis_facts", "axis_place"}
	for _, name := range readOnly {
		if !isReadOnlyTool(name) {
			t.Errorf("expected %q to be read-only", name)
		}
	}
	notReadOnly := []string{"run_shell", "unknown", ""}
	for _, name := range notReadOnly {
		if isReadOnlyTool(name) {
			t.Errorf("expected %q to NOT be read-only", name)
		}
	}
}
