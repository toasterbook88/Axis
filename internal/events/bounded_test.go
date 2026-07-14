package events

import (
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestEventBusBoundedDispatch(t *testing.T) {
	// Own temp log + sequence dir so 1000 flocked writes do not contend on
	// the shared ~/.axis path used by other packages under go test ./...
	_ = isolateEventBus(t, t.TempDir())

	const eventCount = 1000
	var release sync.WaitGroup
	release.Add(eventCount)
	unregister := RegisterListener(func(Event) {
		time.Sleep(time.Millisecond)
		release.Done()
	})
	// Remove the listener so it does not fire for other tests sharing the bus.
	defer unregister()
	before := runtime.NumGoroutine()
	for i := 0; i < eventCount; i++ {
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
}
