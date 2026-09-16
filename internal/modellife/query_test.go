package modellife

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
		{"https://qwen.example.com/v1", "https://qwen.example.com/v1/chat/completions"},
	}

	for _, tc := range tests {
		got := NormalizeChatCompletionsEndpoint(tc.input)
		if got != tc.want {
			t.Errorf("NormalizeChatCompletionsEndpoint(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
