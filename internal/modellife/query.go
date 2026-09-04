package modellife

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// QueryRequest encapsulates a prompt request to a resident model instance.
type QueryRequest struct {
	Model        string  `json:"model"`
	Prompt       string  `json:"prompt"`
	SystemPrompt string  `json:"system_prompt,omitempty"`
	MaxTokens    int     `json:"max_tokens,omitempty"`
	Temperature  float64 `json:"temperature,omitempty"`
}

// QueryResult contains the completion response text and execution telemetry.
type QueryResult struct {
	Model            string `json:"model"`
	Content          string `json:"content"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	DurationMS       int64  `json:"duration_ms"`
	Endpoint         string `json:"endpoint"`
}

type chatCompletionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionRequest struct {
	Model       string                  `json:"model"`
	Messages    []chatCompletionMessage `json:"messages"`
	MaxTokens   int                     `json:"max_tokens,omitempty"`
	Temperature *float64                `json:"temperature,omitempty"`
	Stream      bool                    `json:"stream"`
}

type chatCompletionChoice struct {
	Index        int                   `json:"index"`
	Message      chatCompletionMessage `json:"message"`
	Text         string                `json:"text,omitempty"`
	FinishReason string                `json:"finish_reason,omitempty"`
}

type chatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatCompletionResponse struct {
	ID      string                 `json:"id"`
	Model   string                 `json:"model"`
	Choices []chatCompletionChoice `json:"choices"`
	Usage   chatCompletionUsage    `json:"usage"`
	Error   *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// QueryHTTP sends an OpenAI-compatible POST /v1/chat/completions request to endpoint.
func QueryHTTP(ctx context.Context, endpoint string, req QueryRequest, httpClient *http.Client) (QueryResult, error) {
	endpoint = NormalizeChatCompletionsEndpoint(endpoint)

	var messages []chatCompletionMessage
	if strings.TrimSpace(req.SystemPrompt) != "" {
		messages = append(messages, chatCompletionMessage{
			Role:    "system",
			Content: req.SystemPrompt,
		})
	}
	messages = append(messages, chatCompletionMessage{
		Role:    "user",
		Content: req.Prompt,
	})

	bodyPayload := chatCompletionRequest{
		Model:    req.Model,
		Messages: messages,
		Stream:   false,
	}
	if req.MaxTokens > 0 {
		bodyPayload.MaxTokens = req.MaxTokens
	}
	if req.Temperature > 0 {
		temp := req.Temperature
		bodyPayload.Temperature = &temp
	}

	payloadBytes, err := json.Marshal(bodyPayload)
	if err != nil {
		return QueryResult{}, fmt.Errorf("marshal query request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		return QueryResult{}, fmt.Errorf("create query request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := httpClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}

	start := time.Now()
	resp, err := client.Do(httpReq)
	duration := time.Since(start)
	if err != nil {
		return QueryResult{
			Model:      req.Model,
			Endpoint:   endpoint,
			DurationMS: duration.Milliseconds(),
		}, fmt.Errorf("execute query against %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return QueryResult{
			Model:      req.Model,
			Endpoint:   endpoint,
			DurationMS: duration.Milliseconds(),
		}, fmt.Errorf("read query response from %s: %w", endpoint, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp chatCompletionResponse
		if jsonErr := json.Unmarshal(respBytes, &errResp); jsonErr == nil && errResp.Error != nil && errResp.Error.Message != "" {
			return QueryResult{
				Model:      req.Model,
				Endpoint:   endpoint,
				DurationMS: duration.Milliseconds(),
			}, fmt.Errorf("query %s returned status %d: %s", endpoint, resp.StatusCode, errResp.Error.Message)
		}
		return QueryResult{
			Model:      req.Model,
			Endpoint:   endpoint,
			DurationMS: duration.Milliseconds(),
		}, fmt.Errorf("query %s returned status %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}

	result, err := ParseQueryResponse(respBytes, duration, endpoint)
	if err != nil {
		return QueryResult{
			Model:      req.Model,
			Endpoint:   endpoint,
			DurationMS: duration.Milliseconds(),
		}, err
	}
	if result.Model == "" {
		result.Model = req.Model
	}
	return result, nil
}

// ParseQueryResponse parses an OpenAI-compatible chat completion JSON payload.
func ParseQueryResponse(rawJSON []byte, duration time.Duration, endpoint string) (QueryResult, error) {
	var parsed chatCompletionResponse
	if err := json.Unmarshal(rawJSON, &parsed); err != nil {
		return QueryResult{}, fmt.Errorf("parse query response JSON: %w (payload: %s)", err, strings.TrimSpace(string(rawJSON)))
	}

	if parsed.Error != nil && parsed.Error.Message != "" {
		return QueryResult{}, fmt.Errorf("query error: %s", parsed.Error.Message)
	}

	if len(parsed.Choices) == 0 {
		return QueryResult{}, fmt.Errorf("query returned no choices")
	}

	content := parsed.Choices[0].Message.Content
	if content == "" && parsed.Choices[0].Text != "" {
		content = parsed.Choices[0].Text
	}

	totalTokens := parsed.Usage.TotalTokens
	if totalTokens == 0 && (parsed.Usage.PromptTokens > 0 || parsed.Usage.CompletionTokens > 0) {
		totalTokens = parsed.Usage.PromptTokens + parsed.Usage.CompletionTokens
	}

	return QueryResult{
		Model:            parsed.Model,
		Content:          content,
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		TotalTokens:      totalTokens,
		DurationMS:       duration.Milliseconds(),
		Endpoint:         endpoint,
	}, nil
}

// NormalizeChatCompletionsEndpoint ensures the endpoint has http/https scheme
// and ends with /v1/chat/completions.
func NormalizeChatCompletionsEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}
	endpoint = strings.TrimRight(endpoint, "/")
	if strings.HasSuffix(endpoint, "/v1/chat/completions") {
		return endpoint
	}
	if strings.HasSuffix(endpoint, "/v1") {
		return endpoint + "/chat/completions"
	}
	return endpoint + "/v1/chat/completions"
}
