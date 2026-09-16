package events

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestEventBusBoundedDispatch(t *testing.T) {
	// Own temp log + sequence dir so flocked writes do not contend on the
	// shared ~/.axis path used by other packages under go test ./...
	_ = isolateEventBus(t, t.TempDir())

	const eventCount = 100
	var processed atomic.Int64
	unregister := RegisterListener(func(Event) {
		// Small amount of work per listener so the burst exercises the pool
		// without making the test timing-sensitive.
		time.Sleep(time.Millisecond)
		processed.Add(1)
	})
	defer unregister()

	before := runtime.NumGoroutine()
	for range eventCount {
		EmitToBuffer(NoopEmitter{}, EventTaskExecutionStarted, nil)
	}
	time.Sleep(100 * time.Millisecond)
	if got := runtime.NumGoroutine() - before; got > 32 {
		t.Fatalf("event dispatch spawned too many goroutines: %d", got)
	}

	// Require a successful drain before the test returns so the next test's
	// ResetTestLog cannot capture late task.execution.started writes.
	if err := FlushEvents(30 * time.Second); err != nil {
		t.Fatalf("drain after burst: %v", err)
	}

	// The bounded pool intentionally drops overflow events when the queue is
	// full; verify the drain completed and that bounded dispatch did not spawn
	// a goroutine per event. We allow for dropped events from queue pressure.
	if processed.Load() > eventCount {
		t.Fatalf("processed %d events, expected at most %d", processed.Load(), eventCount)
	}
	if processed.Load() == 0 {
		t.Fatal("no listener callbacks were processed")
	}
}
