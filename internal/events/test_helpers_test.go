package events

import (
	"path/filepath"
	"testing"
	"time"
)

// isolateEventBus drains global webhook/cortex/queue state, then points the
// event log at an isolated empty file under dir. Call at the start of tests
// that assert on log contents or emit large bursts so they do not contend on
// ~/.axis or leak into later tests via path switches mid-flight.
func isolateEventBus(t *testing.T, dir string) (logPath string) {
	t.Helper()
	_ = SetWebhooks(nil)
	SetCortexClient(nil)

	// Drain before switching the path so in-flight writers finish on the old path.
	if err := FlushEvents(15 * time.Second); err != nil {
		t.Fatalf("pre-isolate FlushEvents: %v", err)
	}

	logPath = filepath.Join(dir, "events.jsonl")
	if err := ResetTestLog(logPath); err != nil {
		t.Fatalf("ResetTestLog: %v", err)
	}

	t.Cleanup(func() {
		if err := FlushEvents(15 * time.Second); err != nil {
			t.Errorf("cleanup FlushEvents: %v", err)
		}
		_ = SetWebhooks(nil)
		SetCortexClient(nil)
		SetLogPath("")
	})
	return logPath
}
