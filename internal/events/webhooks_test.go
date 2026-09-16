package events

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/netutil"
)

func TestSetWebhooksRejectsInvalidURL(t *testing.T) {
	if err := SetWebhooks([]string{"file:///tmp/events"}); err == nil {
		t.Fatal("expected invalid webhook URL error")
	}
}

func TestWebhookDispatchSuccess(t *testing.T) {
	tempDir := t.TempDir()
	_ = isolateEventBus(t, tempDir)

	var called int32
	var mu sync.Mutex
	var receivedEvent Event

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var ev Event
		_ = json.Unmarshal(body, &ev)
		mu.Lock()
		receivedEvent = ev
		mu.Unlock()
		atomic.AddInt32(&called, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	netutil.AllowInternalHost("127.0.0.1")
	defer netutil.ResetInternalAllowlist()
	SetWebhooks([]string{server.URL})
	defer SetWebhooks(nil)

	EmitToBuffer(nil, "test.webhook.event", map[string]any{"status": "dispatched"})

	// Wait for async dispatch
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&called) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if atomic.LoadInt32(&called) != 1 {
		t.Errorf("expected webhook to be called once, got %d", called)
	}
	mu.Lock()
	evtCopy := receivedEvent
	mu.Unlock()
	if evtCopy.Name != "test.webhook.event" {
		t.Errorf("expected received event name test.webhook.event, got %s", evtCopy.Name)
	}
	if evtCopy.Payload["status"] != "dispatched" {
		t.Errorf("expected status dispatched, got %v", evtCopy.Payload["status"])
	}
}

func TestWebhookDispatchRetry(t *testing.T) {
	// Temporarily shorten backoff for testing speed
	originalBackoff := backoffBase
	backoffBase = 1 * time.Millisecond
	defer func() { backoffBase = originalBackoff }()

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	// Direct call to postWithRetry to verify it performs 4 attempts (1 initial + 3 retries)
	err := postWithRetry(server.URL, []byte(`{}`))
	if err == nil {
		t.Error("expected error from failing webhook post, got nil")
	}

	expectedAttempts := int32(4)
	if atomic.LoadInt32(&calls) != expectedAttempts {
		t.Errorf("expected %d post attempts, got %d", expectedAttempts, calls)
	}
}
