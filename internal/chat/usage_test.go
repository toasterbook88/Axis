package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The final /api/chat chunk carries the daemon's real token counts. The
// client must capture them on the returned Message; zero when the backend
// does not report usage (e.g. older daemons).
func TestClientChatStreamCapturesUsageFromFinalChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
		case "/api/show":
			w.WriteHeader(http.StatusOK)
		case "/api/chat":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"message":{"role":"assistant","content":"Hello"},"done":false}` + "\n"))
			w.Write([]byte(`{"message":{"role":"assistant","content":" world"},"done":false}` + "\n"))
			w.Write([]byte(`{"done":true,"prompt_eval_count":12,"eval_count":34}` + "\n"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "testmodel")
	result, err := client.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	if result.UsageTokensIn != 12 {
		t.Errorf("UsageTokensIn = %d, want 12", result.UsageTokensIn)
	}
	if result.UsageTokensOut != 34 {
		t.Errorf("UsageTokensOut = %d, want 34", result.UsageTokensOut)
	}
}

func TestClientChatStreamZeroUsageWhenNotReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
		case "/api/show":
			w.WriteHeader(http.StatusOK)
		case "/api/chat":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"message":{"role":"assistant","content":"Hi"},"done":false}` + "\n"))
			w.Write([]byte(`{"done":true}` + "\n"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "testmodel")
	result, err := client.ChatStream(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	if result.UsageTokensIn != 0 || result.UsageTokensOut != 0 {
		t.Errorf("usage = (%d, %d), want zeros when backend reports none", result.UsageTokensIn, result.UsageTokensOut)
	}
}

func TestMessageUsageFieldsNotSerialized(t *testing.T) {
	// Usage is telemetry-only: persisting history must not write it, so a
	// replay never double-counts stored turns.
	m := Message{Role: RoleAssistant, Content: "x", UsageTokensIn: 10, UsageTokensOut: 20}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if strings.Contains(s, "UsageTokensIn") || strings.Contains(s, "UsageTokensOut") {
		t.Errorf("usage fields leaked into serialization: %s", s)
	}
	var back Message
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.UsageTokensIn != 0 || back.UsageTokensOut != 0 {
		t.Errorf("usage survived round-trip: (%d, %d)", back.UsageTokensIn, back.UsageTokensOut)
	}
}
