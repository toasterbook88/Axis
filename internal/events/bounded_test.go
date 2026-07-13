package events

import (
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestEventBusBoundedDispatch(t *testing.T) {
	t.Skip("RED: pending fix #2 — see EXECUTION-PLAN.md")

	const eventCount = 1000
	var release sync.WaitGroup
	release.Add(eventCount)
	RegisterListener(func(Event) {
		time.Sleep(time.Millisecond)
		release.Done()
	})
	before := runtime.NumGoroutine()
	for i := 0; i < eventCount; i++ {
		EmitToBuffer(NoopEmitter{}, EventTaskExecutionStarted, nil)
	}
	time.Sleep(100 * time.Millisecond)
	if got := runtime.NumGoroutine() - before; got > 32 {
		t.Fatalf("event dispatch spawned too many goroutines: %d", got)
	}
}
