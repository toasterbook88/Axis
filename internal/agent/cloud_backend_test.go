package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

func TestCloudBackend_OpenAI_HermesThoughtActionPromoted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\": [{\"delta\": {\"content\": \"{\\\"thought\\\": \\\"Reading the config.\\\", \\\"action\\\": {\\\"name\\\": \\\"read_file\\\", \\\"arguments\\\": {\\\"path\\\": \\\"~/.axis/ai.yaml\\\"}}}\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	backend, err := NewCloudBackendWithKey("local-hub", "openai", server.URL, "mock-key", "qwen3.8-9b", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tools := []chat.ToolDef{{Type: "function", Function: chat.ToolDefFunction{Name: "read_file", Description: "read file"}}}
	resp, err := backend.ChatStream(context.Background(), []chat.Message{{Role: chat.RoleUser, Content: "cat ~/.axis/ai.yaml"}}, tools, io.Discard)
	if err != nil {
		t.Fatalf("unexpected ChatStream error: %v", err)
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("expected read_file, got %q", resp.ToolCalls[0].Function.Name)
	}
	if args := resp.ToolCalls[0].Function.Arguments; string(args) != `{"path": "~/.axis/ai.yaml"}` {
		t.Errorf("unexpected arguments: %s", string(args))
	}
	if resp.Content != "Reading the config." {
		t.Errorf("thought should remain as content, got %q", resp.Content)
	}
}

func TestCloudBackend_Anthropic_MultiMessageDeltaDoesNotInflateStats(t *testing.T) {
	// Anthropic may emit several message_delta events with a running
	// output_tokens total. Stats must end at the final total, not the sum.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\": \"message_start\", \"message\": {\"usage\": {\"input_tokens\": 10}}}\n\n")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"type\": \"content_block_start\", \"index\": 0, \"content_block\": {\"type\": \"text\", \"text\": \"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\": \"content_block_delta\", \"index\": 0, \"delta\": {\"type\": \"text_delta\", \"text\": \"hi\"}}\n\n")
		fmt.Fprint(w, "event: message_delta\ndata: {\"type\": \"message_delta\", \"usage\": {\"output_tokens\": 10}}\n\n")
		fmt.Fprint(w, "event: message_delta\ndata: {\"type\": \"message_delta\", \"usage\": {\"output_tokens\": 25}}\n\n")
	}))
	defer server.Close()

	backend, err := NewCloudBackendWithKey("anthropic", "anthropic", server.URL, "mock-key", "claude-3-5-sonnet", 0.015)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, err := backend.ChatStream(context.Background(), []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, io.Discard)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if resp.UsageTokensIn != 10 || resp.UsageTokensOut != 25 {
		t.Fatalf("stamp = (%d, %d), want (10, 25)", resp.UsageTokensIn, resp.UsageTokensOut)
	}
	tokensIn, tokensOut, cost := backend.Stats()
	if tokensIn != 10 {
		t.Fatalf("Stats tokensIn = %d, want 10", tokensIn)
	}
	if tokensOut != 25 {
		t.Fatalf("Stats tokensOut = %d, want 25 (got inflated sum if deltas were added whole)", tokensOut)
	}
	wantCost := (35.0 / 1000.0) * 0.015
	if abs := cost - wantCost; abs > 1e-12 || abs < -1e-12 {
		t.Fatalf("Stats cost = %v, want %v", cost, wantCost)
	}
}

func TestCloudBackend_OpenAI_MultiTurnEstimate(t *testing.T) {
	// Mock provider that never reports usage, forcing estimation each turn.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\": [{\"delta\": {\"content\": \"hi\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	backend, err := NewCloudBackendWithKey("openrouter", "openai", server.URL, "mock-key", "gpt-4o", 0.002)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 0; i < 2; i++ {
		_, err := backend.ChatStream(context.Background(), []chat.Message{{Role: chat.RoleUser, Content: "hello"}}, nil, io.Discard)
		if err != nil {
			t.Fatalf("turn %d: unexpected error: %v", i, err)
		}
	}
	tokensIn, tokensOut, cost := backend.Stats()
	if tokensIn <= 0 {
		t.Errorf("expected positive prompt tokens after two turns, got %d", tokensIn)
	}
	if tokensOut <= 0 {
		t.Errorf("expected positive completion tokens after two turns, got %d", tokensOut)
	}
	if cost <= 0 {
		t.Errorf("expected positive cost after two turns, got %f", cost)
	}
}

func TestCloudBackend_Anthropic_MultiTurnEstimate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"type\": \"content_block_start\", \"index\": 0, \"content_block\": {\"type\": \"text\", \"text\": \"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\": \"content_block_delta\", \"index\": 0, \"delta\": {\"type\": \"text_delta\", \"text\": \"hi\"}}\n\n")
	}))
	defer server.Close()

	backend, err := NewCloudBackendWithKey("anthropic", "anthropic", server.URL, "mock-key", "claude-3-5-sonnet", 0.015)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 0; i < 2; i++ {
		_, err := backend.ChatStream(context.Background(), []chat.Message{{Role: chat.RoleUser, Content: "hello"}}, nil, io.Discard)
		if err != nil {
			t.Fatalf("turn %d: unexpected error: %v", i, err)
		}
	}
	tokensIn, tokensOut, cost := backend.Stats()
	if tokensIn <= 0 {
		t.Errorf("expected positive prompt tokens after two turns, got %d", tokensIn)
	}
	if tokensOut <= 0 {
		t.Errorf("expected positive completion tokens after two turns, got %d", tokensOut)
	}
	if cost <= 0 {
		t.Errorf("expected positive cost after two turns, got %f", cost)
	}
}
