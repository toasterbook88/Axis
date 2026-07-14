package events

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestJSONLLogger(t *testing.T) {
	tempDir := t.TempDir()
	tempLog := filepath.Join(tempDir, "test_events.jsonl")
	if err := ResetTestLog(tempLog); err != nil {
		t.Fatalf("ResetTestLog: %v", err)
	}
	t.Cleanup(func() { SetLogPath("") })

	// Verify initially empty
	evs, err := getRecentEventsFromFile(10)
	if err != nil {
		t.Fatalf("getRecentEventsFromFile failed: %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("expected 0 events, got %d", len(evs))
	}

	// Append events
	evt1 := NewEvent("test.event.a", map[string]any{"x": 1})
	evt2 := NewEvent("test.event.b", map[string]any{"x": 2})

	if err := appendEventToFile(evt1); err != nil {
		t.Fatalf("appendEventToFile failed: %v", err)
	}
	if err := appendEventToFile(evt2); err != nil {
		t.Fatalf("appendEventToFile failed: %v", err)
	}

	// Retrieve events
	evs, err = getRecentEventsFromFile(10)
	if err != nil {
		t.Fatalf("getRecentEventsFromFile failed: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("expected 2 events, got %d", len(evs))
	}
	if evs[0].Name != "test.event.a" || evs[1].Name != "test.event.b" {
		t.Errorf("unexpected retrieved events: %+v", evs)
	}

	// Test limit
	evs, err = getRecentEventsFromFile(1)
	if err != nil {
		t.Fatalf("getRecentEventsFromFile failed: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	if evs[0].Name != "test.event.b" {
		t.Errorf("expected test.event.b, got %s", evs[0].Name)
	}
}

func TestFlockSequenceAllocation(t *testing.T) {
	tempDir := t.TempDir()
	tempLog := filepath.Join(tempDir, "events.jsonl")
	if err := ResetTestLog(tempLog); err != nil {
		t.Fatalf("ResetTestLog: %v", err)
	}
	t.Cleanup(func() { SetLogPath("") })

	var wg sync.WaitGroup
	numGoroutines := 10
	numAllocations := 20
	results := make(chan uint64, numGoroutines*numAllocations)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numAllocations; j++ {
				seq, err := allocateSequence()
				if err != nil {
					t.Errorf("allocateSequence failed: %v", err)
					return
				}
				results <- seq
			}
		}()
	}

	wg.Wait()
	close(results)

	allocated := make(map[uint64]bool)
	for seq := range results {
		if allocated[seq] {
			t.Errorf("duplicate sequence number allocated: %d", seq)
		}
		allocated[seq] = true
	}

	expectedCount := numGoroutines * numAllocations
	if len(allocated) != expectedCount {
		t.Errorf("expected %d unique sequences, got %d", expectedCount, len(allocated))
	}

	for i := uint64(1); i <= uint64(expectedCount); i++ {
		if !allocated[i] {
			t.Errorf("missing sequence number: %d", i)
		}
	}
}

func TestFlockLogRotation(t *testing.T) {
	tempDir := t.TempDir()
	tempLog := filepath.Join(tempDir, "events.jsonl")
	if err := ResetTestLog(tempLog); err != nil {
		t.Fatalf("ResetTestLog: %v", err)
	}
	t.Cleanup(func() { SetLogPath("") })

	// 1. Write an initial event
	evt := NewEvent("test.event.a", map[string]any{"data": "a"})
	if err := appendEventToFile(evt); err != nil {
		t.Fatalf("failed to append initial event: %v", err)
	}

	// 2. Make events.jsonl size exceed 10MB (dummy write of newlines to avoid large tokens)
	dummyData := bytes.Repeat([]byte("\n"), 10*1024*1024+100)
	f, err := os.OpenFile(tempLog, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatalf("failed to open events.jsonl: %v", err)
	}
	if _, err := f.Write(dummyData); err != nil {
		_ = f.Close()
		t.Fatalf("failed to write dummy data: %v", err)
	}
	_ = f.Close()

	// 3. Append another event to trigger rotation
	evt2 := NewEvent("test.event.b", map[string]any{"data": "b"})
	if err := appendEventToFile(evt2); err != nil {
		t.Fatalf("failed to append event triggering rotation: %v", err)
	}

	// 4. Verify rotation
	rotatedPath := filepath.Join(tempDir, "events.1.jsonl")
	if _, err := os.Stat(rotatedPath); err != nil {
		t.Fatalf("expected events.1.jsonl to exist after rotation, got error: %v", err)
	}

	// 5. Read multi-file log
	evs, err := getRecentEventsFromFile(10)
	if err != nil {
		t.Fatalf("failed to read recent events: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("expected 2 events retrieved from multi-file log, got %d", len(evs))
	}
	if evs[0].Name != "test.event.a" || evs[1].Name != "test.event.b" {
		t.Errorf("unexpected events order/names: %+v", evs)
	}
}

func TestLargeJSONLEventReadable(t *testing.T) {
	// Regression: getRecentEventsFromFile must read events larger than
	// bufio.MaxScanTokenSize (64 KiB) and still return following events.
	tempDir := t.TempDir()
	tempLog := filepath.Join(tempDir, "events.jsonl")
	if err := ResetTestLog(tempLog); err != nil {
		t.Fatalf("ResetTestLog: %v", err)
	}
	t.Cleanup(func() { SetLogPath("") })

	bigPayload := strings.Repeat("z", 70*1024)
	big := NewEvent("test.event.large", map[string]any{"blob": bigPayload})
	small := NewEvent("test.event.small", map[string]any{"ok": true})
	if err := appendEventToFile(big); err != nil {
		t.Fatalf("append large: %v", err)
	}
	if err := appendEventToFile(small); err != nil {
		t.Fatalf("append small: %v", err)
	}

	evs, err := getRecentEventsFromFile(10)
	if err != nil {
		t.Fatalf("getRecentEventsFromFile: %v", err)
	}
	if len(evs) != 2 {
		names := make([]string, len(evs))
		for i, e := range evs {
			names[i] = e.Name
		}
		t.Fatalf("expected 2 events after large line, got %d names=%v", len(evs), names)
	}
	if evs[0].Name != "test.event.large" || evs[1].Name != "test.event.small" {
		t.Fatalf("unexpected names: %q, %q", evs[0].Name, evs[1].Name)
	}
	blob, _ := evs[0].Payload["blob"].(string)
	if len(blob) != 70*1024 {
		t.Fatalf("large payload length = %d, want %d", len(blob), 70*1024)
	}
}
