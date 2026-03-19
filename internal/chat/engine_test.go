package chat

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOllamaClient_GenerateStream(t *testing.T) {
	var pullCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/api/show" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/api/pull" {
			atomic.AddInt32(&pullCalls, 1)
			w.WriteHeader(http.StatusOK)
			return
		}
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
	if got := atomic.LoadInt32(&pullCalls); got != 0 {
		t.Fatalf("expected no pull calls, got %d", got)
	}
}

func TestOllamaClient_GenerateStream_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/api/show" {
			w.WriteHeader(http.StatusOK)
			return
		}
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

func TestOllamaClient_GenerateStream_ModelMissingDoesNotPull(t *testing.T) {
	var pullCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
			return
		case "/api/show":
			w.WriteHeader(http.StatusNotFound)
			return
		case "/api/pull":
			atomic.AddInt32(&pullCalls, 1)
			w.WriteHeader(http.StatusOK)
			return
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewOllamaClient(server.URL, "llama3")

	var buf bytes.Buffer
	err := client.GenerateStream(context.Background(), "test prompt", &buf)
	if err == nil {
		t.Fatal("expected missing-model error, got nil")
	}
	if !strings.Contains(err.Error(), "ollama pull llama3") {
		t.Fatalf("expected pull guidance, got %v", err)
	}
	if got := atomic.LoadInt32(&pullCalls); got != 0 {
		t.Fatalf("expected no pull calls, got %d", got)
	}
}

func TestHybridEngine_GenerateStream_Fallback(t *testing.T) {
	// Create a dummy server that returns 404 to trigger fallback
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/api/show" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == "/api/pull" {
			t.Fatalf("unexpected auto-pull request")
		}
		if r.URL.Path == "/api/generate" {
			w.WriteHeader(http.StatusOK)
			return
		}
		t.Fatalf("unexpected request path: %s", r.URL.Path)
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
	if !strings.Contains(got, "ollama pull llama3") {
		t.Errorf("expected missing-model guidance, got %q", got)
	}
}
