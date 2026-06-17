package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/mcpclient"
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

func TestToolRegistryHasAllDefaultTools(t *testing.T) {
	tc := &ToolContext{}
	r := NewToolRegistry(tc)

	expected := []string{
		"axis_status", "axis_facts", "axis_place", "axis_summary",
		"axis_reservations", "read_file", "write_file", "edit_file",
		"list_directory", "grep_search", "run_shell",
		"git_status", "git_diff", "git_log",
	}
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
	if !strings.Contains(result, "No cluster snapshot available") {
		t.Errorf("expected 'No cluster snapshot available', got: %s", result)
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

func TestToolSummary(t *testing.T) {
	snap := &models.ClusterSnapshot{
		Status: "healthy",
		Nodes:  []models.NodeFacts{{Name: "test-node"}},
		Summary: models.ClusterSummary{
			TotalNodes:     1,
			ReachableNodes: 1,
		},
	}
	tc := &ToolContext{Snapshot: snap}
	r := NewToolRegistry(tc)

	result, err := r.Execute(context.Background(), "axis_summary", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "1 nodes (1 reachable), status: healthy") {
		t.Errorf("expected summary result, got: %s", result)
	}
}

func TestToolReadFile(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.txt")
	content := []byte("Hello, AXIS!")
	if err := os.WriteFile(tmpFile, content, 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	// Change to temp dir so relative paths work.
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	tc := &ToolContext{}
	r := NewToolRegistry(tc)

	result, err := r.Execute(context.Background(), "read_file", json.RawMessage(fmt.Sprintf(`{"path":%q}`, "test.txt")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Hello, AXIS!") {
		t.Errorf("expected file content, got: %s", result)
	}
}

func TestToolReadFilePathValidation(t *testing.T) {
	tc := &ToolContext{}
	r := NewToolRegistry(tc)

	_, err := r.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"../secret.txt"}`))
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	// EvalSymlinks may report lstat failure for non-existent paths outside cwd.
	if !strings.Contains(err.Error(), "escapes") && !strings.Contains(err.Error(), "cannot resolve") {
		t.Errorf("expected 'escapes' or 'cannot resolve' error, got: %s", err.Error())
	}
}

func TestToolListDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatalf("failed to write temp file: %v", err)
		}
	}

	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	tc := &ToolContext{}
	r := NewToolRegistry(tc)

	result, err := r.Execute(context.Background(), "list_directory", json.RawMessage(`{"path":"."}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "a.txt") || !strings.Contains(result, "b.txt") {
		t.Errorf("expected directory listing with files, got: %s", result)
	}
}

func TestToolListDirectoryPathValidation(t *testing.T) {
	tc := &ToolContext{}
	r := NewToolRegistry(tc)

	_, err := r.Execute(context.Background(), "list_directory", json.RawMessage(`{"path":"/etc/shadow"}`))
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("expected 'escapes' error, got: %s", err.Error())
	}
}

func TestToolWriteFile(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	tc := &ToolContext{}
	r := NewToolRegistry(tc)

	result, err := r.Execute(context.Background(), "write_file", json.RawMessage(`{"path":"subdir/new.txt","content":"file content"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Successfully wrote") {
		t.Errorf("expected success message, got: %s", result)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "subdir/new.txt"))
	if err != nil {
		t.Fatalf("failed to read back file: %v", err)
	}
	if string(data) != "file content" {
		t.Errorf("expected 'file content', got: %q", string(data))
	}
}

func TestToolEditFile(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	tc := &ToolContext{}
	r := NewToolRegistry(tc)

	path := filepath.Join(tmpDir, "edit.txt")
	if err := os.WriteFile(path, []byte("hello world\nhello again"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	result, err := r.Execute(context.Background(), "edit_file", json.RawMessage(`{
		"path": "edit.txt",
		"target_content": "world",
		"replacement_content": "axis"
	}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Successfully replaced") {
		t.Errorf("expected success message, got: %s", result)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "hello axis") {
		t.Errorf("expected 'hello axis', got: %q", string(data))
	}

	_, err = r.Execute(context.Background(), "edit_file", json.RawMessage(`{
		"path": "edit.txt",
		"target_content": "hello",
		"replacement_content": "hi"
	}`))
	if err == nil {
		t.Fatal("expected error for non-unique replacement")
	}
}

func TestToolGrepSearch(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	tc := &ToolContext{}
	r := NewToolRegistry(tc)

	if err := os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("the quick brown fox"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("jumps over the lazy dog"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	result, err := r.Execute(context.Background(), "grep_search", json.RawMessage(`{"query":"fox"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "file1.txt:1: the quick brown fox") {
		t.Errorf("expected matching line in grep result, got: %s", result)
	}

	result, err = r.Execute(context.Background(), "grep_search", json.RawMessage(`{"query":"lazy"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "file2.txt:1: jumps over the lazy dog") {
		t.Errorf("expected matching line in grep result, got: %s", result)
	}
}

func TestToolGrepSearchLimitsMatches(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	tc := &ToolContext{}
	r := NewToolRegistry(tc)

	// Create enough files to exceed the 50 match limit with a single line each.
	for i := 0; i < 55; i++ {
		name := filepath.Join(tmpDir, fmt.Sprintf("file%03d.txt", i))
		if err := os.WriteFile(name, []byte("match token\n"), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}
	}

	result, err := r.Execute(context.Background(), "grep_search", json.RawMessage(`{"query":"token"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) != 50 {
		t.Errorf("expected 50 matches, got %d", len(lines))
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

func TestAgentMutatingToolDeclinedByOperator(t *testing.T) {
	server := mockOllamaChat(t, [][]mockStreamChunk{
		toolCallResponse("write_file", `{"path":"foo.txt","content":"hello"}`),
		textResponse("OK, I won't write that file."),
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

	err := agent.Run(context.Background(), "write hello to foo.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "declined") && !strings.Contains(output, "operator declined to execute tool") {
		t.Errorf("expected 'declined' message, got: %s", output)
	}
}

func TestAgentMCPToolRegistration(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			ID     any    `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")

		switch req.Method {
		case "initialize":
			w.Write([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"test-server","version":"1.0"}}}`, req.ID)))
		case "tools/list":
			w.Write([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"result":{"tools":[{"name":"echo","description":"echoes input","inputSchema":{"type":"object"}}]}}`, req.ID)))
		case "resources/list":
			w.Write([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"result":{"resources":[]}}`, req.ID)))
		case "prompts/list":
			w.Write([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"result":{"prompts":[]}}`, req.ID)))
		}
	}))
	defer s.Close()

	cfg := &config.Config{
		Nodes: []config.NodeConfig{{Name: "dummy", Hostname: "localhost", SSHUser: "root"}},
		MCPServers: map[string]config.MCPServerConfig{
			"mock": {
				Transport: "http",
				URL:       s.URL,
			},
		},
	}

	mcpReg := mcpclient.NewRegistry()
	mcpReg.ConnectAll(context.Background(), cfg)
	defer mcpReg.Close()

	tc := &ToolContext{}
	r := NewToolRegistry(tc)
	r.RegisterMCPTools(mcpReg)

	if !r.HasTool("mcp_mock_echo") {
		t.Fatal("expected MCP tool 'mcp_mock_echo' to be registered")
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

func TestAgentShellExecutesWhenApproved(t *testing.T) {
	server := mockOllamaChat(t, [][]mockStreamChunk{
		toolCallResponse("run_shell", `{"command":"echo agent-test-output"}`),
		textResponse("Done."),
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
			if command != "echo agent-test-output" {
				t.Fatalf("RunShell command = %q, want echo agent-test-output", command)
			}
			return `{"ok":true,"node":"alpha","output":"agent-test-output"}`, nil
		},
	})

	err := agent.Run(context.Background(), "echo test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected injected RunShell to be called")
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
	readOnly := []string{
		"axis_status", "axis_facts", "axis_place", "axis_summary",
		"axis_reservations", "read_file", "list_directory",
	}
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
