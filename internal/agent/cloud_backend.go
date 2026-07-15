package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/llmrouter"
)

// CloudBackend implements ChatBackend for cloud providers (OpenAI-compatible and Anthropic).
type CloudBackend struct {
	providerName string
	providerKind string
	endpoint     string
	apiKey       string
	model        string
	costPer1K    float64
	client       *http.Client

	mu        sync.RWMutex
	tokensIn  int
	tokensOut int
	totalCost float64
}

// NewCloudBackendWithKey creates a new CloudBackend with a pre-resolved API key.
func NewCloudBackendWithKey(providerKind, providerName, endpoint, apiKey, model string, costPer1K float64) (*CloudBackend, error) {
	resolved, err := llmrouter.ResolveEndpoint(providerKind, endpoint)
	if err != nil {
		return nil, err
	}
	return &CloudBackend{
		providerName: providerName,
		providerKind: providerKind,
		endpoint:     resolved,
		apiKey:       apiKey,
		model:        model,
		costPer1K:    costPer1K,
		client:       &http.Client{Timeout: 120 * time.Second}, // longer timeout for agent loops
	}, nil
}

// NewOpenAICompatibleBackend creates a ChatBackend for local OpenAI-compatible
// servers (MLX, llama.cpp, etc.). endpoint is a base URL such as
// http://host:8080; /v1 is appended when missing. apiKey is optional (many
// local servers ignore auth); when non-empty it is sent as Bearer.
func NewOpenAICompatibleBackend(endpoint, model, apiKey string) (*CloudBackend, error) {
	base := strings.TrimSpace(endpoint)
	if base == "" {
		return nil, fmt.Errorf("openai-compatible endpoint is empty")
	}
	base = strings.TrimRight(base, "/")
	// Accept either http://host:port or http://host:port/v1.
	if !strings.HasSuffix(base, "/v1") {
		base = base + "/v1"
	}
	return &CloudBackend{
		providerName: "openai-compatible",
		providerKind: "openai",
		endpoint:     base,
		apiKey:       apiKey,
		model:        model,
		client:       &http.Client{Timeout: 120 * time.Second},
	}, nil
}

// Stats returns the accumulated prompt tokens, completion tokens, and cost.
func (b *CloudBackend) Stats() (int, int, float64) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.tokensIn, b.tokensOut, b.totalCost
}

// ChatStream satisfies the ChatBackend interface.
func (b *CloudBackend) ChatStream(ctx context.Context, msgs []chat.Message, tools []chat.ToolDef, w io.Writer) (chat.Message, error) {
	isAnthropic := strings.EqualFold(b.providerKind, "anthropic")

	if isAnthropic {
		return b.streamAnthropic(ctx, msgs, tools, w)
	}
	return b.streamOpenAI(ctx, msgs, tools, w)
}

// streamOpenAI handles standard OpenAI/OpenRouter/Groq SSE streaming.
func (b *CloudBackend) streamOpenAI(ctx context.Context, msgs []chat.Message, tools []chat.ToolDef, w io.Writer) (chat.Message, error) {
	// 1. Convert messages to OpenAI format.
	type openAIMessage struct {
		Role       string           `json:"role"`
		Content    string           `json:"content,omitempty"`
		ToolCallID string           `json:"tool_call_id,omitempty"`
		ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	}

	formattedMsgs := make([]openAIMessage, len(msgs))
	for i, m := range msgs {
		var toolCalls []openAIToolCall
		for _, tc := range m.ToolCalls {
			toolCalls = append(toolCalls, openAIToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: openAIFunctionCall{
					Name:      tc.Function.Name,
					Arguments: string(tc.Function.Arguments),
				},
			})
		}
		formattedMsgs[i] = openAIMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			ToolCalls:  toolCalls,
		}
	}

	// 2. Prepare request payload.
	reqBody := map[string]any{
		"model":    b.model,
		"messages": formattedMsgs,
		"stream":   true,
	}
	if len(tools) > 0 {
		reqBody["tools"] = tools
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return chat.Message{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint+"/chat/completions", bytes.NewReader(reqBytes))
	if err != nil {
		return chat.Message{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(b.apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+b.apiKey)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return chat.Message{}, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return chat.Message{}, fmt.Errorf("http status %d: %s", resp.StatusCode, string(respBytes))
	}

	// 3. Process SSE stream.
	var result chat.Message
	result.Role = chat.RoleAssistant

	var accumulatedTools []chat.ToolCall
	indexMap := make(map[int]int)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	usageReported := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" || data == "" {
			continue
		}

		var chunk openAIChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// Some providers send non-JSON error messages or metadata
			continue
		}

		if len(chunk.Choices) == 0 {
			if chunk.Usage != nil {
				b.accumulateTokens(chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens)
				usageReported = true
			}
			continue
		}

		delta := chunk.Choices[0].Delta
		if delta.Content != "" {
			if w != nil {
				fmt.Fprint(w, delta.Content)
			}
			result.Content += delta.Content
		}

		for _, tcDelta := range delta.ToolCalls {
			idx, exists := indexMap[tcDelta.Index]
			if !exists {
				newTC := chat.ToolCall{
					ID:       tcDelta.ID,
					Type:     "function",
					Function: chat.ToolCallFunction{},
				}
				accumulatedTools = append(accumulatedTools, newTC)
				idx = len(accumulatedTools) - 1
				indexMap[tcDelta.Index] = idx
			}

			if tcDelta.Function.Name != "" {
				accumulatedTools[idx].Function.Name += tcDelta.Function.Name
			}
			if tcDelta.Function.Arguments != "" {
				accumulatedTools[idx].Function.Arguments = append(
					accumulatedTools[idx].Function.Arguments,
					[]byte(tcDelta.Function.Arguments)...,
				)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("stream read: %w", err)
	}

	result.ToolCalls = accumulatedTools

	// If token usage wasn't reported in the stream, estimate it.
	if !usageReported {
		pLen := 0
		for _, m := range msgs {
			pLen += len(m.Content)
			for _, tc := range m.ToolCalls {
				pLen += len(tc.Function.Name) + len(tc.Function.Arguments)
			}
		}
		for _, tc := range result.ToolCalls {
			pLen += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
		estIn := pLen / 4
		if estIn == 0 {
			estIn = 1
		}
		estOut := len(result.Content) / 4
		if estOut == 0 {
			estOut = 1
		}
		b.accumulateTokens(estIn, estOut)
	}

	return result, nil
}

// streamAnthropic handles Anthropic native messages streaming.
func (b *CloudBackend) streamAnthropic(ctx context.Context, msgs []chat.Message, tools []chat.ToolDef, w io.Writer) (chat.Message, error) {
	// 1. Extract system prompt and construct messages list.
	var systemPrompt string
	var anthropicMsgs []map[string]any

	for _, m := range msgs {
		if m.Role == chat.RoleSystem {
			systemPrompt = m.Content
			continue
		}

		if m.Role == chat.RoleTool {
			// Convert tool response
			toolBlock := map[string]any{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     m.Content,
			}
			// Anthropic requires tool results to be inside user messages.
			// Group with previous tool results if possible, or append new user message.
			if len(anthropicMsgs) > 0 && anthropicMsgs[len(anthropicMsgs)-1]["role"] == "user" {
				prevContent, ok := anthropicMsgs[len(anthropicMsgs)-1]["content"].([]any)
				if ok {
					anthropicMsgs[len(anthropicMsgs)-1]["content"] = append(prevContent, toolBlock)
					continue
				}
			}
			anthropicMsgs = append(anthropicMsgs, map[string]any{
				"role":    "user",
				"content": []any{toolBlock},
			})
			continue
		}

		if m.Role == chat.RoleAssistant {
			if len(m.ToolCalls) > 0 {
				var contentBlocks []any
				if m.Content != "" {
					contentBlocks = append(contentBlocks, map[string]any{
						"type": "text",
						"text": m.Content,
					})
				}
				for _, tc := range m.ToolCalls {
					var input map[string]any
					_ = json.Unmarshal(tc.Function.Arguments, &input)
					contentBlocks = append(contentBlocks, map[string]any{
						"type":  "tool_use",
						"id":    tc.ID,
						"name":  tc.Function.Name,
						"input": input,
					})
				}
				anthropicMsgs = append(anthropicMsgs, map[string]any{
					"role":    "assistant",
					"content": contentBlocks,
				})
			} else {
				anthropicMsgs = append(anthropicMsgs, map[string]any{
					"role":    "assistant",
					"content": m.Content,
				})
			}
			continue
		}

		// User message
		anthropicMsgs = append(anthropicMsgs, map[string]any{
			"role":    "user",
			"content": m.Content,
		})
	}

	// 2. Format Anthropic tools
	var anthropicTools []map[string]any
	for _, t := range tools {
		var inputSchema map[string]any
		_ = json.Unmarshal(t.Function.Parameters, &inputSchema)
		anthropicTools = append(anthropicTools, map[string]any{
			"name":         t.Function.Name,
			"description":  t.Function.Description,
			"input_schema": inputSchema,
		})
	}

	// 3. Prepare payload.
	reqBody := map[string]any{
		"model":      b.model,
		"messages":   anthropicMsgs,
		"stream":     true,
		"max_tokens": 4096,
	}
	if systemPrompt != "" {
		reqBody["system"] = systemPrompt
	}
	if len(anthropicTools) > 0 {
		reqBody["tools"] = anthropicTools
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return chat.Message{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint+"/messages", bytes.NewReader(reqBytes))
	if err != nil {
		return chat.Message{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", b.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := b.client.Do(req)
	if err != nil {
		return chat.Message{}, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return chat.Message{}, fmt.Errorf("http status %d: %s", resp.StatusCode, string(respBytes))
	}

	// 4. Process Anthropic stream events.
	var result chat.Message
	result.Role = chat.RoleAssistant

	var accumulatedTools []chat.ToolCall
	toolInputBufs := make(map[string]*strings.Builder)
	blockToToolID := make(map[int]string)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	usageReported := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}

		var event anthropicEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "content_block_start":
			if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
				tc := chat.ToolCall{
					ID:   event.ContentBlock.ID,
					Type: "function",
					Function: chat.ToolCallFunction{
						Name: event.ContentBlock.Name,
					},
				}
				accumulatedTools = append(accumulatedTools, tc)
				toolInputBufs[tc.ID] = &strings.Builder{}
				blockToToolID[event.Index] = tc.ID
			}
		case "content_block_delta":
			if event.Delta != nil {
				if event.Delta.Type == "text_delta" && event.Delta.Text != "" {
					if w != nil {
						fmt.Fprint(w, event.Delta.Text)
					}
					result.Content += event.Delta.Text
				} else if event.Delta.Type == "input_json_delta" && event.Delta.PartialJSON != "" {
					if tcID, ok := blockToToolID[event.Index]; ok {
						if buf, ok := toolInputBufs[tcID]; ok {
							buf.WriteString(event.Delta.PartialJSON)
						}
					}
				}
			}
		case "message_delta":
			if event.Usage != nil {
				b.accumulateTokens(0, event.Usage.OutputTokens)
				usageReported = true
			}
		case "message_start":
			if event.Message != nil && event.Message.Usage != nil {
				b.accumulateTokens(event.Message.Usage.InputTokens, 0)
				usageReported = true
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("stream read: %w", err)
	}

	// Finalize tool inputs.
	for i, tc := range accumulatedTools {
		if buf, ok := toolInputBufs[tc.ID]; ok {
			accumulatedTools[i].Function.Arguments = json.RawMessage(buf.String())
		}
	}
	result.ToolCalls = accumulatedTools

	// If token usage wasn't reported in the stream, estimate it.
	if !usageReported {
		pLen := 0
		for _, m := range msgs {
			pLen += len(m.Content)
			for _, tc := range m.ToolCalls {
				pLen += len(tc.Function.Name) + len(tc.Function.Arguments)
			}
		}
		for _, tc := range result.ToolCalls {
			pLen += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
		estIn := pLen / 4
		if estIn == 0 {
			estIn = 1
		}
		estOut := len(result.Content) / 4
		if estOut == 0 {
			estOut = 1
		}
		b.accumulateTokens(estIn, estOut)
	}

	return result, nil
}

func (b *CloudBackend) accumulateTokens(prompt, completion int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if prompt > 0 {
		b.tokensIn += prompt
	}
	if completion > 0 {
		b.tokensOut += completion
	}
	total := b.tokensIn + b.tokensOut
	b.totalCost = (float64(total) / 1000.0) * b.costPer1K
}

// --- Helper types for OpenAI compatibility ---

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIChunk struct {
	Choices []struct {
		Delta struct {
			Content   string            `json:"content"`
			ToolCalls []openAIDeltaCall `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type openAIDeltaCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// --- Helper types for Anthropic ---

type anthropicEvent struct {
	Type         string                 `json:"type"`
	Index        int                    `json:"index"`
	ContentBlock *anthropicContentBlock `json:"content_block,omitempty"`
	Delta        *anthropicDelta        `json:"delta,omitempty"`
	Message      *anthropicMessageInfo  `json:"message,omitempty"`
	Usage        *anthropicUsage        `json:"usage,omitempty"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type anthropicDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

type anthropicMessageInfo struct {
	Usage *anthropicUsage `json:"usage,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}
