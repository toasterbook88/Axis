package events

import (
	"os"
	"sync"
	"testing"
	"time"
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
