package chat

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOllamaClient_GenerateStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("expected /api/generate, got %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		// Simulate a streaming JSON response
		w.Write([]byte(`{"model":"llama3","response":"Hello","done":false}` + "\n"))
		w.Write([]byte(`{"model":"llama3","response":" World!","done":true}` + "\n"))
	}))
	defer server.Close()

	client := NewOllamaClient(server.URL, "llama3")

	var buf bytes.Buffer
	err := client.GenerateStream(context.Background(), "test prompt", &buf)
	if err != nil {
		t.Fatalf("GenerateStream returned error: %v", err)
	}

	if got := buf.String(); got != "Hello World!" {
		t.Errorf("expected 'Hello World!', got %q", got)
	}
}

func TestOllamaClient_GenerateStream_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewOllamaClient(server.URL, "llama3")

	var buf bytes.Buffer
	err := client.GenerateStream(context.Background(), "test prompt", &buf)
	if err == nil {
		t.Fatal("expected error on 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404 error, got %v", err)
	}
}

func TestHybridEngine_GenerateStream_Fallback(t *testing.T) {
	// Create a dummy server that returns 404 to trigger fallback
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewOllamaClient(server.URL, "llama3")
	engine := &HybridEngine{
		model:  "llama3",
		ollama: client,
	}

	var buf bytes.Buffer
	err := engine.GenerateStream(context.Background(), "test prompt", &buf)
	if err != nil {
		t.Fatalf("GenerateStream returned unexpected error: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "[AXIS Fallback]") {
		t.Errorf("expected fallback message, got %q", got)
	}
	if !strings.Contains(got, "llama3") {
		t.Errorf("expected model 'llama3' in fallback, got %q", got)
	}
}
