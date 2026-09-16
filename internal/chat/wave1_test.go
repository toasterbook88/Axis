package chat

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientChatStreamError400WithTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/api/show":
			w.WriteHeader(http.StatusOK)
		case "/api/chat":
			w.WriteHeader(http.StatusBadRequest)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "gemma3n:e2b")
	var buf bytes.Buffer
	msgs := []Message{{Role: RoleUser, Content: "hi"}}
	tools := []ToolDef{{
		Type: "function",
		Function: ToolDefFunction{
			Name: "axis_status",
		},
	}}

	_, err := client.ChatStream(context.Background(), msgs, tools, &buf)
	if err == nil {
		t.Fatal("expected error on 400 with tools")
	}
	if !strings.Contains(err.Error(), "400 Bad Request") {
		t.Errorf("expected 400 mention, got %v", err)
	}
	if !strings.Contains(err.Error(), "may not support tool calling") {
		t.Errorf("expected tool-calling guidance, got %v", err)
	}
	if !strings.Contains(err.Error(), "gemma3n:e2b") {
		t.Errorf("expected model name in error, got %v", err)
	}
}

func TestClientChatStreamError400WithoutTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/api/show":
			w.WriteHeader(http.StatusOK)
		case "/api/chat":
			w.WriteHeader(http.StatusBadRequest)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "testmodel")
	var buf bytes.Buffer
	msgs := []Message{{Role: RoleUser, Content: "hi"}}

	_, err := client.ChatStream(context.Background(), msgs, nil, &buf)
	if err == nil {
		t.Fatal("expected error on 400 without tools")
	}
	if !strings.Contains(err.Error(), "400 Bad Request") {
		t.Errorf("expected 400 mention, got %v", err)
	}
	if strings.Contains(err.Error(), "tool calling") {
		t.Errorf("should NOT mention tool calling when no tools were sent")
	}
}

// ---------------------------------------------------------------------------
// message.go tests
// ---------------------------------------------------------------------------

func TestBuildClusterSummaryNilSnapshot(t *testing.T) {
	s := BuildClusterSummary(nil)
	if s != nil {
		t.Fatalf("expected nil for nil snapshot, got %+v", s)
	}
}

func TestClientChatStreamErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/api/show":
			w.WriteHeader(http.StatusOK)
		case "/api/chat":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "testmodel")
	var buf bytes.Buffer
	msgs := []Message{{Role: RoleUser, Content: "hi"}}

	_, err := client.ChatStream(context.Background(), msgs, nil, &buf)
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention 500, got %v", err)
	}
}

func TestClientChatStreamNilWriter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/api/show":
			w.WriteHeader(http.StatusOK)
		case "/api/chat":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"message":{"role":"assistant","content":"ok"},"done":true}` + "\n"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "testmodel")
	msgs := []Message{{Role: RoleUser, Content: "hi"}}

	// nil writer should not panic
	result, err := client.ChatStream(context.Background(), msgs, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	if result.Content != "ok" {
		t.Errorf("content = %q, want ok", result.Content)
	}
}
