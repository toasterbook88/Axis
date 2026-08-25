package chat

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	thinkTagRegex      = regexp.MustCompile(`(?s)<think>(.*?)</think>`)
	toolCallTagRegex   = regexp.MustCompile(`(?s)<tool_call>(.*?)</tool_call>`)
	jsonCodeBlockRegex = regexp.MustCompile("(?s)```(?:json)?\\s*\\n?(\\{.*?\\}|\\[.*?\\])\\s*\\n?```")
)

// ExtractReasoning extracts any <think>...</think> blocks from content.
// Returns the accumulated thinking text (trimmed) and the content with thinking blocks removed.
func ExtractReasoning(content string) (thinking string, cleanContent string) {
	if !strings.Contains(content, "<think>") {
		return "", content
	}
	var thoughts []string
	matches := thinkTagRegex.FindAllStringSubmatch(content, -1)
	for _, m := range matches {
		if len(m) > 1 && strings.TrimSpace(m[1]) != "" {
			thoughts = append(thoughts, strings.TrimSpace(m[1]))
		}
	}
	clean := thinkTagRegex.ReplaceAllString(content, "")
	return strings.Join(thoughts, "\n\n"), strings.TrimSpace(clean)
}

// rawToolCallJSON represents typical shapes of JSON tool calls emitted by models in text.
type rawToolCallJSON struct {
	Name       string          `json:"name"`
	Function   string          `json:"function,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Parameters json.RawMessage `json:"parameters,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
}

// ExtractFallbackToolCalls inspects text content for embedded tool call patterns when
// the backend stream returned no native tool calls. It validates detected tool names
// against toolDefs. If matches are found, it returns the extracted ToolCalls and
// the remaining text content with tool markup removed.
func ExtractFallbackToolCalls(content string, toolDefs []ToolDef) ([]ToolCall, string) {
	if len(toolDefs) == 0 || strings.TrimSpace(content) == "" {
		return nil, content
	}

	validTools := make(map[string]bool, len(toolDefs))
	for _, td := range toolDefs {
		validTools[td.Function.Name] = true
	}

	var extracted []ToolCall
	clean := content

	// 1. Look for <tool_call>...</tool_call> tags
	toolCallMatches := toolCallTagRegex.FindAllStringSubmatch(clean, -1)
	for _, match := range toolCallMatches {
		if len(match) > 1 {
			raw := strings.TrimSpace(match[1])
			if tc, ok := parseSingleToolJSON(raw, validTools, len(extracted)+1); ok {
				extracted = append(extracted, tc)
			}
		}
	}
	if len(extracted) > 0 {
		clean = toolCallTagRegex.ReplaceAllString(clean, "")
		return extracted, strings.TrimSpace(clean)
	}

	// 2. Look for ```json ... ``` code blocks containing tool calls
	codeBlockMatches := jsonCodeBlockRegex.FindAllStringSubmatch(clean, -1)
	for _, match := range codeBlockMatches {
		if len(match) > 1 {
			raw := strings.TrimSpace(match[1])
			if tc, ok := parseSingleToolJSON(raw, validTools, len(extracted)+1); ok {
				extracted = append(extracted, tc)
			} else {
				// Check if it's an array of tool calls
				var list []rawToolCallJSON
				if err := json.Unmarshal([]byte(raw), &list); err == nil && len(list) > 0 {
					for _, item := range list {
						if tc, ok := rawItemToToolCall(item, validTools, len(extracted)+1); ok {
							extracted = append(extracted, tc)
						}
					}
				}
			}
		}
	}
	if len(extracted) > 0 {
		clean = jsonCodeBlockRegex.ReplaceAllString(clean, "")
		return extracted, strings.TrimSpace(clean)
	}

	return nil, content
}

func parseSingleToolJSON(raw string, validTools map[string]bool, seq int) (ToolCall, bool) {
	var item rawToolCallJSON
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return ToolCall{}, false
	}
	return rawItemToToolCall(item, validTools, seq)
}

func rawItemToToolCall(item rawToolCallJSON, validTools map[string]bool, seq int) (ToolCall, bool) {
	name := strings.TrimSpace(item.Name)
	if name == "" {
		name = strings.TrimSpace(item.Function)
	}
	if name == "" || !validTools[name] {
		return ToolCall{}, false
	}

	rawArgs := item.Arguments
	if len(rawArgs) == 0 {
		rawArgs = item.Parameters
	}
	if len(rawArgs) == 0 {
		rawArgs = item.Args
	}
	if len(rawArgs) == 0 {
		rawArgs = item.Input
	}
	if len(rawArgs) == 0 {
		rawArgs = json.RawMessage("{}")
	}

	return ToolCall{
		ID:   fmt.Sprintf("fallback-tc-%d", seq),
		Type: "function",
		Function: ToolCallFunction{
			Name:      name,
			Arguments: rawArgs,
		},
	}, true
}
