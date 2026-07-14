package events

import (
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestEventBusBoundedDispatch(t *testing.T) {
	defer func() {
		_ = FlushEvents(5 * time.Second)
	}()

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
}
