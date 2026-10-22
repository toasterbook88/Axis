package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

// usageServer streams a reply and sends a usage chunk only when the request
// asks for it, like OpenAI and LiteLLM. With rejectOptions it answers 400 to
// any request carrying stream_options, like a server that does not know it.
type usageServer struct {
	rejectOptions bool
	mu            sync.Mutex
	withOptions   []bool
}

func (u *usageServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	opts, has := body["stream_options"].(map[string]any)
	u.mu.Lock()
	u.withOptions = append(u.withOptions, has)
	u.mu.Unlock()
	if has && u.rejectOptions {
		http.Error(w, `{"error":{"message":"Unrecognized request argument supplied: stream_options"}}`, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "data: {\"choices\": [{\"delta\": {\"content\": \"ok\"}}]}\n\n")
	if has && opts["include_usage"] == true {
		fmt.Fprint(w, "data: {\"choices\": [], \"usage\": {\"prompt_tokens\": 15, \"completion_tokens\": 2}}\n\n")
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func newUsageBackend(t *testing.T, srv *usageServer) *CloudBackend {
	t.Helper()
	server := httptest.NewServer(srv)
	t.Cleanup(server.Close)
	b, err := NewCloudBackendWithKey("local-hub", "openai", server.URL, "mock-key", "test-model", 0)
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	return b
}

// Streamed replies carry usage only when the request sets
// stream_options.include_usage. Without it, token counts were never logged.
func TestCloudBackendRequestsStreamUsage(t *testing.T) {
	srv := &usageServer{}
	b := newUsageBackend(t, srv)
	resp, err := b.ChatStream(context.Background(), []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, io.Discard)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if len(srv.withOptions) != 1 || !srv.withOptions[0] {
		t.Fatalf("request did not ask for stream usage: %v", srv.withOptions)
	}
	if resp.UsageTokensIn != 15 || resp.UsageTokensOut != 2 {
		t.Fatalf("usage = (%d, %d), want (15, 2)", resp.UsageTokensIn, resp.UsageTokensOut)
	}
}

// A server that rejects stream_options still works: one retry without it,
// and later requests do not send it again.
func TestCloudBackendFallsBackWhenStreamOptionsRejected(t *testing.T) {
	srv := &usageServer{rejectOptions: true}
	b := newUsageBackend(t, srv)
	for i := 0; i < 2; i++ {
		resp, err := b.ChatStream(context.Background(), []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, io.Discard)
		if err != nil {
			t.Fatalf("ChatStream %d: %v", i, err)
		}
		if resp.Content != "ok" {
			t.Fatalf("content = %q", resp.Content)
		}
	}
	want := []bool{true, false, false}
	if fmt.Sprint(srv.withOptions) != fmt.Sprint(want) {
		t.Fatalf("stream_options per request = %v, want %v", srv.withOptions, want)
	}
}

// Other errors are returned as before, with no retry.
func TestCloudBackendDoesNotRetryOtherErrors(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, `{"error":{"message":"model not found"}}`, http.StatusBadRequest)
	}))
	defer server.Close()
	b, err := NewCloudBackendWithKey("local-hub", "openai", server.URL, "mock-key", "test-model", 0)
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	if _, err := b.ChatStream(context.Background(), []chat.Message{{Role: chat.RoleUser, Content: "hi"}}, nil, io.Discard); err == nil {
		t.Fatal("want an error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}
