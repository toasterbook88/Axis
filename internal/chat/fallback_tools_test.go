package chat

import (
	"encoding/json"
	"testing"
)

func TestExtractReasoning(t *testing.T) {
	input := "<think>\nThinking about the cluster status...\nLooking at nodes.\n</think>\nHere is the answer."
	thinking, clean := ExtractReasoning(input)

	if thinking != "Thinking about the cluster status...\nLooking at nodes." {
		t.Errorf("unexpected thinking: %q", thinking)
	}
	if clean != "Here is the answer." {
		t.Errorf("unexpected clean content: %q", clean)
	}

	noThink := "Just plain text."
	th2, cl2 := ExtractReasoning(noThink)
	if th2 != "" || cl2 != noThink {
		t.Errorf("unexpected non-thinking result: th=%q cl=%q", th2, cl2)
	}
}

func TestExtractFallbackToolCalls(t *testing.T) {
	toolDefs := []ToolDef{
		{
			Type: "function",
			Function: ToolDefFunction{
				Name:        "axis_status",
				Description: "status",
			},
		},
		{
			Type: "function",
			Function: ToolDefFunction{
				Name:        "read_file",
				Description: "read file",
			},
		},
	}

	t.Run("ToolCallTags", func(t *testing.T) {
		input := "Let me check the status for you:\n<tool_call>{\"name\": \"axis_status\", \"arguments\": {}}</tool_call>"
		calls, clean := ExtractFallbackToolCalls(input, toolDefs)
		if len(calls) != 1 {
			t.Fatalf("expected 1 tool call, got %d", len(calls))
		}
		if calls[0].Function.Name != "axis_status" {
			t.Errorf("expected axis_status, got %s", calls[0].Function.Name)
		}
		if clean != "Let me check the status for you:" {
			t.Errorf("unexpected clean content: %q", clean)
		}
	})

	t.Run("JSONCodeBlock", func(t *testing.T) {
		input := "I will read the file.\n```json\n{\n  \"name\": \"read_file\",\n  \"arguments\": {\"path\": \"main.go\"}\n}\n```"
		calls, clean := ExtractFallbackToolCalls(input, toolDefs)
		if len(calls) != 1 {
			t.Fatalf("expected 1 tool call, got %d", len(calls))
		}
		if calls[0].Function.Name != "read_file" {
			t.Errorf("expected read_file, got %s", calls[0].Function.Name)
		}
		var args map[string]string
		if err := json.Unmarshal(calls[0].Function.Arguments, &args); err != nil || args["path"] != "main.go" {
			t.Errorf("unexpected arguments: %s", string(calls[0].Function.Arguments))
		}
		if clean != "I will read the file." {
			t.Errorf("unexpected clean: %q", clean)
		}
	})

	t.Run("IgnoreUnknownTools", func(t *testing.T) {
		input := "```json\n{\"name\": \"unknown_dangerous_tool\", \"arguments\": {}}\n```"
		calls, clean := ExtractFallbackToolCalls(input, toolDefs)
		if len(calls) != 0 {
			t.Fatalf("expected 0 tool calls for unknown tool, got %d", len(calls))
		}
		if clean != input {
			t.Errorf("expected untouched clean text, got %q", clean)
		}
	})
}

// A naked JSON object in prose must NOT be promoted to a tool call: the model
// may merely be showing an example, and executing it would be injection.
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

	t.Run("TrailingProseStaysInert", func(t *testing.T) {
		// JSON that parses but has trailing prose after the top-level object
		// must NOT be promoted (Go's Unmarshal rejects trailing non-whitespace).
		input := `{"name":"axis_status","arguments":{}} done!`
		calls, clean := ExtractFallbackToolCalls(input, toolDefs)
		if len(calls) != 0 {
			t.Fatalf("trailing prose must prevent promotion, got %d calls", len(calls))
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

// Flat Hermes drift: a single JSON object with both a top-level "thought"
// field and the tool-call fields. The thought should be preserved as visible
// content, just like the nested action variant.
func TestExtractFallbackToolCallsFlatThoughtAction(t *testing.T) {
	toolDefs := []ToolDef{{Function: ToolDefFunction{Name: "read_file"}}}
	input := `{"thought": "I should read the config file.", "name": "read_file", "arguments": {"path": "~/.axis/ai.yaml"}}`
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

// A non-string top-level "thought" field must not fail the whole parse and
// suppress a valid registered tool call.
func TestExtractFallbackToolCallsFlatNonStringThoughtIgnored(t *testing.T) {
	toolDefs := []ToolDef{{Function: ToolDefFunction{Name: "read_file"}}}
	input := `{"thought": 123, "name": "read_file", "arguments": {"path": "~/.axis/ai.yaml"}}`
	calls, clean := ExtractFallbackToolCalls(input, toolDefs)
	if len(calls) != 1 {
		t.Fatalf("non-string thought must not suppress call, got %d", len(calls))
	}
	if calls[0].Function.Name != "read_file" {
		t.Errorf("expected read_file, got %s", calls[0].Function.Name)
	}
	// Thought is ignored because it is not a JSON string.
	if clean != "" {
		t.Errorf("clean content should be empty when thought is non-string, got %q", clean)
	}
}

// A flat thought/action object whose tool name is not registered is just
// chatter; it must pass through untouched.
func TestExtractFallbackToolCallsFlatUnknownActionIgnored(t *testing.T) {
	toolDefs := []ToolDef{{Function: ToolDefFunction{Name: "read_file"}}}
	input := `{"thought": "thinking", "name": "unknown_tool", "arguments": {}}`
	calls, clean := ExtractFallbackToolCalls(input, toolDefs)
	if len(calls) != 0 {
		t.Fatalf("unknown flat action must not execute, got %d calls", len(calls))
	}
	if clean != input {
		t.Errorf("content should be untouched, got %q", clean)
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
