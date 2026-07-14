package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/events"
)

// TestMain redirects the asynchronous event log to a package-level temp
// directory. Daemon tests set HOME to t.TempDir() and then cancel daemon
// goroutines on return, but the events worker can still create files under
// HOME/.axis after the test function has returned. That races with Go's
// TempDir cleanup and causes flaky "directory not empty" failures. Writing
// event logs into a dedicated directory that is removed after the full
// package run avoids that race without changing per-test behavior.
func TestMain(m *testing.M) {
	eventLogDir, err := os.MkdirTemp("", "axis-daemon-events-*")
	if err != nil {
		panic(fmt.Sprintf("failed to create temp directory: %v", err))
	}
	if err := events.ResetTestLog(filepath.Join(eventLogDir, "events.jsonl")); err != nil {
		panic(fmt.Sprintf("ResetTestLog: %v", err))
	}

	code := m.Run()
	if err := events.FlushEvents(5 * time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "warning: FlushEvents after daemon tests: %v\n", err)
	}
	_ = os.RemoveAll(eventLogDir)
	os.Exit(code)
}
