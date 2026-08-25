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
