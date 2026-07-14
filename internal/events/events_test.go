package events

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/cortex"
)

func TestEventsBufferAndRetrieve(t *testing.T) {
	logFile := isolateEventBus(t, t.TempDir())

	// Reset buffer size and content for clean test
	SetEventBufferSize(3)

	EmitToBuffer(nil, "test.event.1", map[string]any{"val": 1})
	EmitToBuffer(nil, "test.event.2", map[string]any{"val": 2})
	EmitToBuffer(nil, "test.event.3", map[string]any{"val": 3})
	EmitToBuffer(nil, "test.event.4", map[string]any{"val": 4})

	if err := FlushEvents(15 * time.Second); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	// 1. File log should contain exactly these 4 events (no eviction on file appends).
	// Exact count detects contamination from the process-global event bus.
	fileEvs, err := getRecentEventsFromFile(10)
	if err != nil {
		t.Fatalf("failed to read from file log: %v", err)
	}
	if len(fileEvs) != 4 {
		names := make([]string, len(fileEvs))
		for i, e := range fileEvs {
			names[i] = e.Name
		}
		t.Fatalf("expected 4 events in file, got %d names=%v", len(fileEvs), names)
	}
	wantNames := []string{"test.event.1", "test.event.2", "test.event.3", "test.event.4"}
	for i, want := range wantNames {
		if fileEvs[i].Name != want {
			t.Fatalf("file event %d: want %q, got %q", i, want, fileEvs[i].Name)
		}
	}

	// 2. Delete file to force fallback to the in-memory ring buffer
	_ = os.Remove(logFile)

	evs := GetRecentEvents(10)
	if len(evs) != 3 {
		t.Fatalf("expected 3 events in fallback buffer, got %d", len(evs))
	}

	if evs[0].Name != "test.event.2" || evs[1].Name != "test.event.3" || evs[2].Name != "test.event.4" {
		t.Errorf("unexpected event sequence in fallback: %v", evs)
	}

	limited := GetRecentEvents(2)
	if len(limited) != 2 {
		t.Fatalf("expected 2 events in fallback, got %d", len(limited))
	}
	if limited[0].Name != "test.event.3" || limited[1].Name != "test.event.4" {
		t.Errorf("unexpected limited event sequence in fallback: %v", limited)
	}
}

func TestEventsListenerRegistry(t *testing.T) {
	tempDir := t.TempDir()
	_ = isolateEventBus(t, tempDir)

	var wg sync.WaitGroup
	wg.Add(1)

	var mu sync.Mutex
	var received Event
	cancel := RegisterListener(func(e Event) {
		if e.Name == "test.listener.event" {
			mu.Lock()
			received = e
			mu.Unlock()
			wg.Done()
		}
	})
	defer cancel()

	EmitToBuffer(nil, "test.listener.event", map[string]any{"hello": "world"})
	if err := FlushEvents(15 * time.Second); err != nil {
		t.Fatalf("FlushEvents: %v", err)
	}

	// Wait for listener callback with timeout
	c := make(chan struct{})
	go func() {
		wg.Wait()
		close(c)
	}()

	select {
	case <-c:
		// success
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for listener notification")
	}

	mu.Lock()
	evtCopy := received
	mu.Unlock()

	if evtCopy.Name != "test.listener.event" {
		t.Errorf("expected received event test.listener.event, got %s", evtCopy.Name)
	}
	if evtCopy.Payload["hello"] != "world" {
		t.Errorf("expected payload hello: world, got %v", evtCopy.Payload)
	}
}

func TestCortexEventPublishing(t *testing.T) {
	var called bool
	var receivedRequest map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		receivedRequest = req

		// Return standard JSON-RPC success response
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]any{"status": "ok"},
		})
	}))
	defer server.Close()

	// Parse host and port from server URL
	u, _ := url.Parse(server.URL)
	host, portStr, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portStr)

	// Isolate log first (clears any prior cortex client), then attach the test client.
	tempDir := t.TempDir()
	_ = isolateEventBus(t, tempDir)

	cClient := cortex.NewClientWithOptions(host, "test-token", port, 6333, 1*time.Second)
	SetCortexClient(cClient)
	t.Cleanup(func() { SetCortexClient(nil) })

	EmitToBuffer(nil, "test.cortex.event", map[string]any{"data": "test"})
	if err := FlushEvents(15 * time.Second); err != nil {
		t.Fatalf("FlushEvents: %v", err)
	}

	// Wait up to 1s for async publish goroutine to call the test server
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if called {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !called {
		t.Fatal("expected test server to be called by Cortex client")
	}

	// Verify request method and payload
	method := receivedRequest["method"].(string)
	if method != "tools/call" {
		t.Errorf("expected JSON-RPC method tools/call, got %s", method)
	}

	params := receivedRequest["params"].(map[string]any)
	toolName := params["name"].(string)
	if toolName != "publish_event" {
		t.Errorf("expected tool name publish_event, got %s", toolName)
	}
}

func TestEventFiltering(t *testing.T) {
	tempDir := t.TempDir()
	_ = isolateEventBus(t, tempDir)

	var mu sync.Mutex
	var taskEvents []Event
	var allEvents []Event
	var wg sync.WaitGroup
	wg.Add(2) // 1 for task.started, 1 for reservation.released (received by allEvents)

	// Register listener with filter
	cancelTask := RegisterListener(func(e Event) {
		mu.Lock()
		taskEvents = append(taskEvents, e)
		mu.Unlock()
	}, "task.*")
	defer cancelTask()

	cancelAll := RegisterListener(func(e Event) {
		mu.Lock()
		allEvents = append(allEvents, e)
		mu.Unlock()
		wg.Done()
	}, "*")
	defer cancelAll()

	EmitToBuffer(nil, "task.started", map[string]any{"id": 1})
	EmitToBuffer(nil, "reservation.released", map[string]any{"id": 2})
	if err := FlushEvents(15 * time.Second); err != nil {
		t.Fatalf("FlushEvents: %v", err)
	}

	// Wait for callbacks
	c := make(chan struct{})
	go func() {
		wg.Wait()
		close(c)
	}()
	select {
	case <-c:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for listener callbacks")
	}

	mu.Lock()
	taskCopy := make([]Event, len(taskEvents))
	copy(taskCopy, taskEvents)
	allCopy := make([]Event, len(allEvents))
	copy(allCopy, allEvents)
	mu.Unlock()

	if len(taskCopy) != 1 {
		t.Errorf("expected 1 task event, got %d: %+v", len(taskCopy), taskCopy)
	} else if taskCopy[0].Name != "task.started" {
		t.Errorf("expected task.started, got %s", taskCopy[0].Name)
	}

	if len(allCopy) != 2 {
		t.Errorf("expected 2 all events, got %d: %+v", len(allCopy), allCopy)
	}
}

func TestEventSchema(t *testing.T) {
	tempDir := t.TempDir()
	_ = isolateEventBus(t, tempDir)

	var mu sync.Mutex
	var received Event
	var wg sync.WaitGroup
	wg.Add(1)

	cancel := RegisterListener(func(e Event) {
		if e.Name == "test.schema.event" {
			mu.Lock()
			received = e
			mu.Unlock()
			wg.Done()
		}
	}, "*")
	defer cancel()

	EmitToBuffer(nil, "test.schema.event", map[string]any{"foo": "bar"})
	if err := FlushEvents(15 * time.Second); err != nil {
		t.Fatalf("FlushEvents: %v", err)
	}

	// Wait for callback
	c := make(chan struct{})
	go func() {
		wg.Wait()
		close(c)
	}()
	select {
	case <-c:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for schema event callback")
	}

	mu.Lock()
	evtCopy := received
	mu.Unlock()

	if evtCopy.ID == "" {
		t.Error("expected non-empty Event ID (UUID)")
	}
	if evtCopy.Version != 1 {
		t.Errorf("expected Event schema version 1, got %d", evtCopy.Version)
	}
	if evtCopy.Sequence == 0 {
		t.Error("expected positive monotonic Sequence number")
	}
	if evtCopy.Timestamp.IsZero() {
		t.Error("expected non-zero Timestamp")
	}
}
