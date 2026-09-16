package chat

import (
	"encoding/json"
	"testing"
)

func TestExtractFallbackToolCallsIgnoresNakedJSON(t *testing.T) {
	tools := []ToolDef{{Function: ToolDefFunction{Name: "read_file"}}}
	content := `Here is an example of the call you could make: {"name":"read_file","arguments":{"path":"/etc/passwd"}}`
	calls, clean := ExtractFallbackToolCalls(content, tools)
	if len(calls) != 0 {
		t.Fatalf("naked JSON must not execute, got %d calls", len(calls))
	}
	if clean != content {
		t.Fatalf("content should be untouched, got %q", clean)
	}
}

// Regression: some backends (observed: a LiteLLM-routed ollama Qwen GGUF under
// streaming) emit the tool call as a whole-message bare JSON object — no
// <tool_call> wrapper, no ```json fence. The operator sees the raw JSON and the
// call never executes. When the ENTIRE content is a single JSON object naming
// a registered tool, it must be promoted to a tool call; JSON embedded inside
// prose must still be ignored (see TestExtractFallbackToolCallsIgnoresNakedJSON).
func TestExtractFallbackToolCallsBareWholeMessageJSON(t *testing.T) {
	toolDefs := []ToolDef{{Function: ToolDefFunction{Name: "axis_status"}}}

	t.Run("BareObjectWholeMessage", func(t *testing.T) {
		// Exact shape observed in the failing session: pretty-printed, 2-space
		// indent, arguments on its own line.
		input := "{\n  \"name\": \"axis_status\",\n  \"arguments\": {}\n}"
		calls, clean := ExtractFallbackToolCalls(input, toolDefs)
		if len(calls) != 1 {
			t.Fatalf("expected 1 tool call, got %d", len(calls))
		}
		if calls[0].Function.Name != "axis_status" {
			t.Errorf("expected axis_status, got %s", calls[0].Function.Name)
		}
		if clean != "" {
			t.Errorf("whole-message JSON should clean to empty, got %q", clean)
		}
	})

	t.Run("BareObjectCompact", func(t *testing.T) {
		input := `{"name": "axis_status", "arguments": {}}`
		calls, clean := ExtractFallbackToolCalls(input, toolDefs)
		if len(calls) != 1 {
			t.Fatalf("expected 1 tool call, got %d", len(calls))
		}
		if clean != "" {
			t.Errorf("whole-message JSON should clean to empty, got %q", clean)
		}
	})

	t.Run("JSONInsideProseStillIgnored", func(t *testing.T) {
		input := `Sure! Here is the call: {"name":"axis_status","arguments":{}}`
		calls, clean := ExtractFallbackToolCalls(input, toolDefs)
		if len(calls) != 0 {
			t.Fatalf("JSON embedded in prose must not execute, got %d calls", len(calls))
		}
		if clean != input {
			t.Errorf("content should be untouched, got %q", clean)
		}
	})

	t.Run("UnknownToolIgnored", func(t *testing.T) {
		input := `{"name":"not_a_real_tool","arguments":{}}`
		calls, clean := ExtractFallbackToolCalls(input, toolDefs)
		if len(calls) != 0 {
			t.Fatalf("unknown tool must not execute, got %d calls", len(calls))
		}
		if clean != input {
			t.Errorf("content should be untouched, got %q", clean)
		}
	})
}

// Models that drift into a Hermes-style action protocol emit a JSON object
// like {"thought": "...", "action": {"name": "read_file", "arguments": {...}}}.
// When the action names a registered tool, promote it to a tool call and keep
// the thought text as the visible content — the operator should still see the
// reasoning, minus the protocol machinery.
func TestExtractFallbackToolCallsHermesThoughtAction(t *testing.T) {
	toolDefs := []ToolDef{{Function: ToolDefFunction{Name: "read_file"}}}
	input := "{\n  \"thought\": \"I should read the config file.\",\n  \"action\": {\"name\": \"read_file\", \"arguments\": {\"path\": \"~/.axis/ai.yaml\"}}\n}"
	calls, clean := ExtractFallbackToolCalls(input, toolDefs)
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Function.Name != "read_file" {
		t.Errorf("expected read_file, got %s", calls[0].Function.Name)
	}
	var args map[string]string
	if err := json.Unmarshal(calls[0].Function.Arguments, &args); err != nil || args["path"] != "~/.axis/ai.yaml" {
		t.Errorf("unexpected arguments: %s", string(calls[0].Function.Arguments))
	}
	if clean != "I should read the config file." {
		t.Errorf("thought should remain as content, got %q", clean)
	}
}

// A thought/action object whose action names no registered tool is just
// chatter; it must pass through untouched.
func TestExtractFallbackToolCallsHermesUnknownActionIgnored(t *testing.T) {
	toolDefs := []ToolDef{{Function: ToolDefFunction{Name: "read_file"}}}
	input := `{"thought": "thinking", "action": {"name": "unknown_tool", "arguments": {}}}`
	calls, clean := ExtractFallbackToolCalls(input, toolDefs)
	if len(calls) != 0 {
		t.Fatalf("unknown action tool must not execute, got %d calls", len(calls))
	}
	if clean != input {
		t.Errorf("content should be untouched, got %q", clean)
	}
}
