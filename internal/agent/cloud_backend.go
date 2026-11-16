package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/llmrouter"
)

// defaultStreamIdleTimeout bounds how long an HTTP stream may stay silent between
// response headers or SSE reads before aborting as stalled.
const defaultStreamIdleTimeout = 120 * time.Second

type disableThinkingKey struct{}

// WithDisableThinking returns a context requesting non-thinking output on
// compatible chat-template backends (e.g. Qwen3/3.5/3.6 servers). Backends that
// reject chat_template_kwargs automatically retry without it.
func WithDisableThinking(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, disableThinkingKey{}, true)
}

func thinkingDisabledFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	disabled, _ := ctx.Value(disableThinkingKey{}).(bool)
	return disabled
}

// CloudBackend implements ChatBackend for cloud and local OpenAI-compatible/Anthropic servers.
type CloudBackend struct {
	providerName      string
	providerKind      string
	endpoint          string
	apiKey            string
	model             string
	costPer1K         float64
	client            *http.Client
	streamIdleTimeout time.Duration
	thinking          *bool

	mu        sync.RWMutex
	tokensIn  int
	tokensOut int
	totalCost float64

	// noStreamUsage is set once a server rejects stream_options, so later
	// requests skip it. Without stream_options.include_usage, OpenAI-style
	// servers (LiteLLM, OpenAI) send no usage in a streamed reply.
	noStreamUsage atomic.Bool
	// noChatTemplateKwargs is set once a server rejects chat_template_kwargs,
	// so later requests skip it instead of tripping strict parameter validators.
	noChatTemplateKwargs atomic.Bool
}

// NewCloudBackendWithKey creates a new CloudBackend with a pre-resolved API key.
func NewCloudBackendWithKey(providerKind, providerName, endpoint, apiKey, model string, costPer1K float64) (*CloudBackend, error) {
	resolved, err := llmrouter.ResolveEndpoint(providerKind, endpoint)
	if err != nil {
		return nil, err
	}
	return &CloudBackend{
		providerName:      providerName,
		providerKind:      providerKind,
		endpoint:          resolved,
		apiKey:            apiKey,
		model:             model,
		costPer1K:         costPer1K,
		client:            &http.Client{Timeout: 0}, // overall deadline comes from ctx + streamIdleTimeout
		streamIdleTimeout: defaultStreamIdleTimeout,
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
		providerName:      "openai-compatible",
		providerKind:      "openai",
		endpoint:          base,
		apiKey:            apiKey,
		model:             model,
		client:            &http.Client{Timeout: 0},
		streamIdleTimeout: defaultStreamIdleTimeout,
	}, nil
}

func (b *CloudBackend) applyOptions(opts CloudBackendOptions) {
	if opts.StreamIdleTimeout < 0 {
		b.streamIdleTimeout = 0
	} else if opts.StreamIdleTimeout > 0 {
		b.streamIdleTimeout = opts.StreamIdleTimeout
	}
	if opts.Thinking != nil {
		v := *opts.Thinking
		b.thinking = &v
	}
}

func (b *CloudBackend) resolveThinking(ctx context.Context) *bool {
	if thinkingDisabledFromContext(ctx) {
		v := false
		return &v
	}
	return b.thinking
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
	// Consolidate leading system messages so Jinja chat templates (e.g. Qwen,
	// Mistral, Llama) do not reject multiple system messages with
	// "System message must be at the beginning".
	msgs = chat.ConsolidateMessages(msgs)

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

	// 2. Prepare request payload and send with compatibility fallbacks + idle stream guard.
	reqBody := map[string]any{
		"model":    b.model,
		"messages": formattedMsgs,
		"stream":   true,
	}
	if len(tools) > 0 {
		reqBody["tools"] = tools
	}

	thinking := b.resolveThinking(ctx)
	resp, guard, err := b.openOpenAIStream(ctx, reqBody, thinking)
	if err != nil {
		return chat.Message{}, err
	}
	defer guard.Close()
	defer resp.Body.Close()

	// 3. Process SSE stream.
	var result chat.Message
	result.Role = chat.RoleAssistant

	var accumulatedTools []chat.ToolCall
	indexMap := make(map[int]int)
	var reportedIn, reportedOut int

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	usageReported := false
	thinkingActive := false
	// Content chunks that begin a possible whole-message JSON tool call are
	// held back: some backends (LiteLLM-routed ollama Qwen under streaming)
	// emit the tool call as an unfenced JSON object in delta.content with
	// finish_reason stop. Streaming it eagerly would show the operator raw
	// JSON and the call would never execute. The buffer is only engaged when
	// the first content chunk starts with '{' — ordinary prose streams live.
	var jsonBuf strings.Builder
	deferredJSON := false
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

		// Usage can ride on a chunk that still has a choice: the local hub
		// sends it with choices [{"delta":{}}], not the spec's empty list.
		// Keep the last report and count it once after the stream, so a
		// server that repeats usage is not counted twice.
		if chunk.Usage != nil {
			reportedIn, reportedOut = chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens
			usageReported = true
		}
		if len(chunk.Choices) == 0 {
			continue
		}

		delta := chunk.Choices[0].Delta
		thoughtDelta := delta.ReasoningContent
		if thoughtDelta == "" {
			thoughtDelta = delta.Reasoning
		}
		if thoughtDelta != "" {
			thinkingActive = true
			writeThought(w, thoughtDelta)
		}
		if (delta.Content != "" || len(delta.ToolCalls) > 0) && thinkingActive {
			thinkingActive = false
			closeThought(w)
		}
		if delta.Content != "" {
			if !deferredJSON && jsonBuf.Len() == 0 && len(result.Content) == 0 && strings.HasPrefix(strings.TrimLeft(delta.Content, " \t\r\n"), "{") {
				deferredJSON = true
			}
			if deferredJSON {
				// Hold the possible whole-message JSON back from the operator
				// stream; it is flushed at stream end if it turns out to be
				// ordinary content (e.g. prose that merely starts with '{').
				jsonBuf.WriteString(delta.Content)
			} else {
				if w != nil {
					fmt.Fprint(w, delta.Content)
				}
				result.Content += delta.Content
			}
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
	if thinkingActive {
		closeThought(w)
	}

	if err := scanner.Err(); err != nil {
		return result, guard.wrapErr("stream read", ctx, err)
	}

	result.ToolCalls = accumulatedTools
	if len(result.ToolCalls) == 0 && len(tools) > 0 {
		candidate := result.Content
		if deferredJSON {
			// Whole-message JSON was held back: try promoting it BEFORE any
			// flush so the operator never sees raw tool-call JSON. If it is
			// not a tool call, treat the buffered bytes as ordinary content.
			if fallbackCalls, clean := chat.ExtractFallbackToolCalls(jsonBuf.String(), tools); len(fallbackCalls) > 0 {
				result.ToolCalls = fallbackCalls
				result.Content = clean
			} else {
				if w != nil {
					fmt.Fprint(w, jsonBuf.String())
				}
				result.Content += jsonBuf.String()
			}
		} else if fallbackCalls, clean := chat.ExtractFallbackToolCalls(candidate, tools); len(fallbackCalls) > 0 {
			result.ToolCalls = fallbackCalls
			result.Content = clean
		}
	} else if deferredJSON {
		// Native tool calls arrived alongside buffered JSON content; flush the
		// buffer so no bytes are lost.
		if w != nil {
			fmt.Fprint(w, jsonBuf.String())
		}
		result.Content += jsonBuf.String()
	}

	// Stamp what the provider actually reported so the session accumulator
	// totals real numbers; estimated counts stay in the backend only.
	result.UsageTokensIn = reportedIn
	result.UsageTokensOut = reportedOut
	if usageReported {
		b.accumulateTokens(reportedIn, reportedOut)
	}
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
	var systemParts []string
	var anthropicMsgs []map[string]any

	for _, m := range msgs {
		if m.Role == chat.RoleSystem {
			if trimmed := strings.TrimSpace(m.Content); trimmed != "" {
				systemParts = append(systemParts, trimmed)
			}
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
	if systemPrompt := strings.Join(systemParts, "\n\n"); systemPrompt != "" {
		reqBody["system"] = systemPrompt
	}
	if len(anthropicTools) > 0 {
		reqBody["tools"] = anthropicTools
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return chat.Message{}, fmt.Errorf("marshal request: %w", err)
	}

	resp, guard, err := b.doStreamRequest(ctx, func(reqCtx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, b.endpoint+"/messages", bytes.NewReader(reqBytes))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", b.apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		return req, nil
	})
	if err != nil {
		return chat.Message{}, err
	}
	defer guard.Close()
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
	var reportedIn, reportedOut int

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	usageReported := false
	thinkingActive := false
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
				if thinkingActive {
					thinkingActive = false
					closeThought(w)
				}
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
				if event.Delta.Type == "thinking_delta" && event.Delta.Thinking != "" {
					thinkingActive = true
					writeThought(w, event.Delta.Thinking)
				} else if event.Delta.Type == "text_delta" && event.Delta.Text != "" {
					if thinkingActive {
						thinkingActive = false
						closeThought(w)
					}
					if w != nil {
						fmt.Fprint(w, event.Delta.Text)
					}
					result.Content += event.Delta.Text
				} else if event.Delta.Type == "input_json_delta" && event.Delta.PartialJSON != "" {
					if thinkingActive {
						thinkingActive = false
						closeThought(w)
					}
					if tcID, ok := blockToToolID[event.Index]; ok {
						if buf, ok := toolInputBufs[tcID]; ok {
							buf.WriteString(event.Delta.PartialJSON)
						}
					}
				}
			}
		case "message_delta":
			if event.Usage != nil {
				// Anthropic's message_delta output_tokens is the running
				// total for the message, not an increment. Stamp assigns;
				// Stats only adds the delta since the last reported total
				// so multiple deltas cannot inflate tokensOut/cost.
				if event.Usage.OutputTokens > reportedOut {
					b.accumulateTokens(0, event.Usage.OutputTokens-reportedOut)
				}
				reportedOut = event.Usage.OutputTokens
				usageReported = true
			}
		case "message_start":
			if event.Message != nil && event.Message.Usage != nil {
				b.accumulateTokens(event.Message.Usage.InputTokens, 0)
				reportedIn = event.Message.Usage.InputTokens
				usageReported = true
			}
		}
	}
	if thinkingActive {
		closeThought(w)
	}

	if err := scanner.Err(); err != nil {
		return result, guard.wrapErr("stream read", ctx, err)
	}

	// Finalize tool inputs.
	for i, tc := range accumulatedTools {
		if buf, ok := toolInputBufs[tc.ID]; ok {
			accumulatedTools[i].Function.Arguments = json.RawMessage(buf.String())
		}
	}
	result.ToolCalls = accumulatedTools
	if len(result.ToolCalls) == 0 && len(tools) > 0 {
		if fallbackCalls, clean := chat.ExtractFallbackToolCalls(result.Content, tools); len(fallbackCalls) > 0 {
			result.ToolCalls = fallbackCalls
			result.Content = clean
		}
	}

	// Stamp what the provider actually reported so the session accumulator
	// totals real numbers; estimated counts stay in the backend only.
	result.UsageTokensIn = reportedIn
	result.UsageTokensOut = reportedOut

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

// openOpenAIStream sends an OpenAI /chat/completions request under the idle
// watchdog and strips unsupported optional request fields (stream_options,
// chat_template_kwargs) once per backend when a 4xx rejects them.
func (b *CloudBackend) openOpenAIStream(ctx context.Context, reqBody map[string]any, thinking *bool) (*http.Response, *streamIdleGuard, error) {
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		includeUsage := !b.noStreamUsage.Load()
		if includeUsage {
			reqBody["stream_options"] = map[string]any{"include_usage": true}
		} else {
			delete(reqBody, "stream_options")
		}
		includeThinkingKwargs := thinking != nil && !b.noChatTemplateKwargs.Load()
		if includeThinkingKwargs {
			reqBody["chat_template_kwargs"] = map[string]any{"enable_thinking": *thinking}
		} else {
			delete(reqBody, "chat_template_kwargs")
		}

		reqBytes, err := json.Marshal(reqBody)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal request: %w", err)
		}

		resp, guard, err := b.doStreamRequest(ctx, func(reqCtx context.Context) (*http.Request, error) {
			req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, b.endpoint+"/chat/completions", bytes.NewReader(reqBytes))
			if err != nil {
				return nil, fmt.Errorf("create request: %w", err)
			}
			req.Header.Set("Content-Type", "application/json")
			if strings.TrimSpace(b.apiKey) != "" {
				req.Header.Set("Authorization", "Bearer "+b.apiKey)
			}
			return req, nil
		})
		if err != nil {
			return nil, nil, err
		}
		if resp.StatusCode == http.StatusOK {
			return resp, guard, nil
		}

		respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		guard.Close()

		if includeUsage && rejectsRequestField(resp.StatusCode, respBytes, "stream_options") {
			b.noStreamUsage.Store(true)
			continue
		}
		if includeThinkingKwargs && (rejectsRequestField(resp.StatusCode, respBytes, "chat_template_kwargs") || rejectsRequestField(resp.StatusCode, respBytes, "enable_thinking")) {
			b.noChatTemplateKwargs.Store(true)
			continue
		}
		return nil, nil, fmt.Errorf("http status %d: %s", resp.StatusCode, string(respBytes))
	}
	return nil, nil, fmt.Errorf("openAI stream failed after exhausting fallback attempts")
}

func (b *CloudBackend) doStreamRequest(ctx context.Context, buildReq func(context.Context) (*http.Request, error)) (*http.Response, *streamIdleGuard, error) {
	guard := newStreamIdleGuard(ctx, b.streamIdleTimeout)
	req, err := buildReq(guard.ctx)
	if err != nil {
		guard.Close()
		return nil, nil, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		guard.Close()
		return nil, nil, guard.wrapErr("http request", ctx, err)
	}
	guard.touch()
	resp.Body = &idleReadCloser{ReadCloser: resp.Body, touch: guard.touch}
	return resp, guard, nil
}

type streamIdleGuard struct {
	timeout time.Duration
	ctx     context.Context
	cancel  context.CancelFunc
	expired atomic.Bool

	mu      sync.Mutex
	stopped bool
	timer   *time.Timer
}

func newStreamIdleGuard(parent context.Context, timeout time.Duration) *streamIdleGuard {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	g := &streamIdleGuard{
		timeout: timeout,
		ctx:     ctx,
		cancel:  cancel,
	}
	if timeout > 0 {
		g.timer = time.AfterFunc(timeout, func() {
			g.mu.Lock()
			stopped := g.stopped
			if !stopped {
				g.expired.Store(true)
			}
			g.mu.Unlock()
			if !stopped {
				cancel()
			}
		})
	}
	return g
}

func (g *streamIdleGuard) touch() {
	if g == nil || g.timeout <= 0 {
		return
	}
	g.mu.Lock()
	if !g.stopped && g.timer != nil {
		g.timer.Reset(g.timeout)
	}
	g.mu.Unlock()
}

func (g *streamIdleGuard) Close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.stopped = true
	if g.timer != nil {
		g.timer.Stop()
	}
	g.mu.Unlock()
	g.cancel()
}

func (g *streamIdleGuard) wrapErr(prefix string, parent context.Context, err error) error {
	if g != nil && g.expired.Load() && errors.Is(err, context.Canceled) && (parent == nil || parent.Err() == nil) {
		return fmt.Errorf("stream idle timeout after %s: %w", g.timeout, context.DeadlineExceeded)
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

type idleReadCloser struct {
	io.ReadCloser
	touch func()
}

func (r *idleReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 && r.touch != nil {
		r.touch()
	}
	return n, err
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

// rejectsRequestField reports whether an error reply is a 4xx refusal naming
// field (such as stream_options or chat_template_kwargs).
func rejectsRequestField(status int, body []byte, field string) bool {
	return status >= 400 && status < 500 && strings.Contains(strings.ToLower(string(body)), field)
}

type openAIChunk struct {
	Choices []struct {
		Delta struct {
			Content          string            `json:"content"`
			ReasoningContent string            `json:"reasoning_content"`
			Reasoning        string            `json:"reasoning"`
			ToolCalls        []openAIDeltaCall `json:"tool_calls"`
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
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

type anthropicMessageInfo struct {
	Usage *anthropicUsage `json:"usage,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}
