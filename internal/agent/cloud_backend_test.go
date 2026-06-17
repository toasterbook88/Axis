package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

func TestCloudBackend_OpenAI(t *testing.T) {
	// Mock OpenAI SSE stream server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Stream chunks:
		// 1. Content chunk
		// 2. Tool call start
		// 3. Tool call arguments delta
		// 4. Usage stats chunk
		// 5. DONE signal
		fmt.Fprint(w, "data: {\"choices\": [{\"delta\": {\"content\": \"Let me find that file. \"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\": [{\"delta\": {\"tool_calls\": [{\"index\": 0, \"id\": \"call_123\", \"type\": \"function\", \"function\": {\"name\": \"read_file\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\": [{\"delta\": {\"tool_calls\": [{\"index\": 0, \"function\": {\"arguments\": \"{\\\"path\\\":\\\"\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\": [{\"delta\": {\"tool_calls\": [{\"index\": 0, \"function\": {\"arguments\": \"foo.txt\\\"}\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\": [], \"usage\": {\"prompt_tokens\": 10, \"completion_tokens\": 20}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	backend := NewCloudBackendWithKey("openai", server.URL, "mock-key", "gpt-4o", 0.002)

	var streamOut bytes.Buffer
	msgs := []chat.Message{{Role: chat.RoleUser, Content: "read foo.txt"}}
	resp, err := backend.ChatStream(context.Background(), msgs, nil, &streamOut)
	if err != nil {
		t.Fatalf("unexpected ChatStream error: %v", err)
	}

	if streamOut.String() != "Let me find that file. " {
		t.Errorf("expected streamed text to be 'Let me find that file. ', got %q", streamOut.String())
	}
	if resp.Content != "Let me find that file. " {
		t.Errorf("expected final message content to be 'Let me find that file. ', got %q", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_123" {
		t.Errorf("expected tool call ID to be 'call_123', got %q", tc.ID)
	}
	if tc.Function.Name != "read_file" {
		t.Errorf("expected tool call name to be 'read_file', got %q", tc.Function.Name)
	}
	if string(tc.Function.Arguments) != `{"path":"foo.txt"}` {
		t.Errorf("expected tool call arguments to be '{\"path\":\"foo.txt\"}', got %q", string(tc.Function.Arguments))
	}

	tokensIn, tokensOut, cost := backend.Stats()
	if tokensIn != 10 {
		t.Errorf("expected 10 prompt tokens, got %d", tokensIn)
	}
	if tokensOut != 20 {
		t.Errorf("expected 20 completion tokens, got %d", tokensOut)
	}
	expectedCost := (30.0 / 1000.0) * 0.002
	if cost != expectedCost {
		t.Errorf("expected cost %f, got %f", expectedCost, cost)
	}
}

func TestCloudBackend_Anthropic(t *testing.T) {
	// Mock Anthropic SSE stream server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Stream events:
		// 1. message_start (tokens)
		// 2. content_block_start (text)
		// 3. content_block_delta (text content)
		// 4. content_block_start (tool_use)
		// 5. content_block_delta (tool input delta)
		// 6. message_delta (output tokens)
		fmt.Fprint(w, "event: message_start\ndata: {\"type\": \"message_start\", \"message\": {\"usage\": {\"input_tokens\": 15}}}\n\n")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"type\": \"content_block_start\", \"index\": 0, \"content_block\": {\"type\": \"text\", \"text\": \"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\": \"content_block_delta\", \"index\": 0, \"delta\": {\"type\": \"text_delta\", \"text\": \"Searching... \"}}\n\n")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"type\": \"content_block_start\", \"index\": 1, \"content_block\": {\"type\": \"tool_use\", \"id\": \"tool_xyz\", \"name\": \"grep_search\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\": \"content_block_delta\", \"index\": 1, \"delta\": {\"type\": \"input_json_delta\", \"partial_json\": \"{\\\"qu\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\": \"content_block_delta\", \"index\": 1, \"delta\": {\"type\": \"input_json_delta\", \"partial_json\": \"ery\\\": \\\"go\\\"}\"}}\n\n")
		fmt.Fprint(w, "event: message_delta\ndata: {\"type\": \"message_delta\", \"usage\": {\"output_tokens\": 25}}\n\n")
	}))
	defer server.Close()

	backend := NewCloudBackendWithKey("anthropic", server.URL, "mock-key", "claude-3-5-sonnet", 0.015)

	var streamOut bytes.Buffer
	msgs := []chat.Message{
		{Role: chat.RoleSystem, Content: "sys"},
		{Role: chat.RoleUser, Content: "search go"},
	}
	resp, err := backend.ChatStream(context.Background(), msgs, nil, &streamOut)
	if err != nil {
		t.Fatalf("unexpected ChatStream error: %v", err)
	}

	if streamOut.String() != "Searching... " {
		t.Errorf("expected streamed text to be 'Searching... ', got %q", streamOut.String())
	}
	if resp.Content != "Searching... " {
		t.Errorf("expected final message content to be 'Searching... ', got %q", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "tool_xyz" {
		t.Errorf("expected tool call ID to be 'tool_xyz', got %q", tc.ID)
	}
	if tc.Function.Name != "grep_search" {
		t.Errorf("expected tool call name to be 'grep_search', got %q", tc.Function.Name)
	}
	if string(tc.Function.Arguments) != `{"query": "go"}` {
		t.Errorf("expected tool call arguments to be '{\"query\": \"go\"}', got %q", string(tc.Function.Arguments))
	}

	tokensIn, tokensOut, cost := backend.Stats()
	if tokensIn != 15 {
		t.Errorf("expected 15 prompt tokens, got %d", tokensIn)
	}
	if tokensOut != 25 {
		t.Errorf("expected 25 completion tokens, got %d", tokensOut)
	}
	expectedCost := (40.0 / 1000.0) * 0.015
	if cost != expectedCost {
		t.Errorf("expected cost %f, got %f", expectedCost, cost)
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

	backend := NewCloudBackendWithKey("openai", server.URL, "mock-key", "gpt-4o", 0.002)
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

	backend := NewCloudBackendWithKey("anthropic", server.URL, "mock-key", "claude-3-5-sonnet", 0.015)
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
