package modellife

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestQueryHTTP_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.Messages) == 0 {
			http.Error(w, "no messages", http.StatusBadRequest)
			return
		}

		resp := chatCompletionResponse{
			ID:    "chatcmpl-test-123",
			Model: req.Model,
			Choices: []chatCompletionChoice{
				{
					Index: 0,
					Message: chatCompletionMessage{
						Role:    "assistant",
						Content: "Hello from mock model!",
					},
					FinishReason: "stop",
				},
			},
			Usage: chatCompletionUsage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	temp := 0.7
	req := QueryRequest{
		Model:        "mock-model",
		Prompt:       "Say hello",
		SystemPrompt: "You are a test assistant.",
		MaxTokens:    128,
		Temperature:  &temp,
	}

	result, err := QueryHTTP(context.Background(), srv.URL, req, nil)
	if err != nil {
		t.Fatalf("QueryHTTP failed: %v", err)
	}
	if result.Content != "Hello from mock model!" {
		t.Fatalf("content = %q, want 'Hello from mock model!'", result.Content)
	}
	if result.PromptTokens != 10 {
		t.Fatalf("prompt_tokens = %d, want 10", result.PromptTokens)
	}
	if result.CompletionTokens != 5 {
		t.Fatalf("completion_tokens = %d, want 5", result.CompletionTokens)
	}
	if result.TotalTokens != 15 {
		t.Fatalf("total_tokens = %d, want 15", result.TotalTokens)
	}
	if result.Model != "mock-model" {
		t.Fatalf("model = %q, want mock-model", result.Model)
	}
}

func TestQueryHTTP_ZeroTemperature(t *testing.T) {
	var receivedTemp *float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		receivedTemp = req.Temperature

		resp := chatCompletionResponse{
			ID:    "chatcmpl-zero-temp",
			Model: req.Model,
			Choices: []chatCompletionChoice{
				{Message: chatCompletionMessage{Role: "assistant", Content: "deterministic output"}},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	zero := 0.0
	req := QueryRequest{
		Model:       "mock-model",
		Prompt:      "Deterministic prompt",
		Temperature: &zero,
	}

	result, err := QueryHTTP(context.Background(), srv.URL, req, nil)
	if err != nil {
		t.Fatalf("QueryHTTP failed: %v", err)
	}
	if receivedTemp == nil {
		t.Fatal("expected temperature to be sent in JSON payload, got nil")
	}
	if *receivedTemp != 0.0 {
		t.Fatalf("received temperature = %v, want 0.0", *receivedTemp)
	}
	if result.Content != "deterministic output" {
		t.Fatalf("content = %q, want deterministic output", result.Content)
	}
}

func TestQueryHTTP_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"internal engine failure","type":"server_error"}}`))
	}))
	defer srv.Close()

	req := QueryRequest{
		Model:  "mock-model",
		Prompt: "Say hello",
	}

	result, err := QueryHTTP(context.Background(), srv.URL, req, nil)
	if err == nil {
		t.Fatal("expected error from 500 status, got nil")
	}
	if result.DurationMS < 0 {
		t.Fatalf("duration_ms = %d, want >= 0", result.DurationMS)
	}
}

func TestParseQueryResponse(t *testing.T) {
	raw := []byte(`{
		"id": "chatcmpl-456",
		"model": "qwen3.8-27b",
		"choices": [
			{"index": 0, "message": {"role": "assistant", "content": "42"}, "finish_reason": "stop"}
		],
		"usage": {"prompt_tokens": 8, "completion_tokens": 2, "total_tokens": 10}
	}`)

	result, err := ParseQueryResponse(raw, 120*time.Millisecond, "http://127.0.0.1:8082")
	if err != nil {
		t.Fatalf("ParseQueryResponse failed: %v", err)
	}
	if result.Content != "42" {
		t.Fatalf("content = %q, want '42'", result.Content)
	}
	if result.PromptTokens != 8 || result.CompletionTokens != 2 || result.TotalTokens != 10 {
		t.Fatalf("unexpected token counts: %+v", result)
	}
	if result.DurationMS != 120 {
		t.Fatalf("duration_ms = %d, want 120", result.DurationMS)
	}
}

func TestNormalizeChatCompletionsEndpoint(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080/v1/chat/completions"},
		{"127.0.0.1:8080", "http://127.0.0.1:8080/v1/chat/completions"},
		{"http://127.0.0.1:8080/v1", "http://127.0.0.1:8080/v1/chat/completions"},
		{"http://127.0.0.1:8080/v1/chat/completions", "http://127.0.0.1:8080/v1/chat/completions"},
		{"https://qwen.lan.axismcp.org/v1", "https://qwen.lan.axismcp.org/v1/chat/completions"},
	}

	for _, tc := range tests {
		got := NormalizeChatCompletionsEndpoint(tc.input)
		if got != tc.want {
			t.Errorf("NormalizeChatCompletionsEndpoint(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
