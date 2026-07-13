package events

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSetWebhooksRejectsInvalidURL(t *testing.T) {
	if err := SetWebhooks([]string{"file:///tmp/events"}); err == nil {
		t.Fatal("expected invalid webhook URL error")
	}
}

func TestWebhookDispatchSuccess(t *testing.T) {
	tempDir := t.TempDir()
	SetLogPath(filepath.Join(tempDir, "events.jsonl"))
	defer SetLogPath("")
	defer FlushEvents(1 * time.Second)

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

func TestWebhookDeadLetter(t *testing.T) {
	// Temporarily shorten backoff for testing speed
	originalBackoff := backoffBase
	backoffBase = 1 * time.Millisecond
	defer func() { backoffBase = originalBackoff }()

	tempDir := t.TempDir()
	SetLogPath(filepath.Join(tempDir, "events.jsonl"))
	defer SetLogPath("")
	defer FlushEvents(1 * time.Second)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	SetWebhooks([]string{server.URL})
	defer SetWebhooks(nil)

	EmitToBuffer(nil, "test.deadletter.event", map[string]any{"data": "dead"})
	FlushEvents(1 * time.Second)

	// Verify that the dead-letter log is written
	dlPath := filepath.Join(tempDir, "webhook-deadletter.jsonl")
	if _, err := os.Stat(dlPath); err != nil {
		t.Fatalf("expected dead-letter file %s to exist, got error: %v", dlPath, err)
	}

	// Read and verify dead-letter content
	f, err := os.Open(dlPath)
	if err != nil {
		t.Fatalf("failed to open dead-letter file: %v", err)
	}
	defer f.Close()

	var dl WebhookDeadLetter
	if err := json.NewDecoder(f).Decode(&dl); err != nil {
		t.Fatalf("failed to decode dead-letter entry: %v", err)
	}

	if dl.URL != server.URL {
		t.Errorf("expected URL %s, got %s", server.URL, dl.URL)
	}
	if dl.Event.Name != "test.deadletter.event" {
		t.Errorf("expected event name test.deadletter.event, got %s", dl.Event.Name)
	}
}
