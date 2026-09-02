package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/daemon"
)

type restartHarness struct {
	metas      []daemon.Metadata
	metaErrs   []error
	fetches    int32
	terminates int32
	spawns     int32
	pid        int
}

// install wires the harness into the restart seams and shrinks the grace and
// readiness windows so tests do not sleep for real seconds.
func (h *restartHarness) install(t *testing.T) {
	t.Helper()
	prevFetch, prevFind := restartFetchMeta, restartFindPID
	prevTerm, prevSpawn := restartTerminate, restartSpawnDaemon
	prevPoll, prevDeadline := restartPollInterval, restartReadyDeadline
	prevGrace := restartGraceWindow

	restartFetchMeta = func(context.Context, string) (daemon.Metadata, error) {
		i := int(atomic.AddInt32(&h.fetches, 1)) - 1
		if i >= len(h.metas) {
			i = len(h.metas) - 1
		}
		var err error
		if i < len(h.metaErrs) {
			err = h.metaErrs[i]
		}
		return h.metas[i], err
	}
	restartFindPID = func(string) (int, error) { return h.pid, nil }
	restartTerminate = func(int, io.Writer) error { atomic.AddInt32(&h.terminates, 1); return nil }
	restartSpawnDaemon = func(string) (int, error) { atomic.AddInt32(&h.spawns, 1); return 4242, nil }
	restartPollInterval = 2 * time.Millisecond
	restartReadyDeadline = 60 * time.Millisecond
	restartGraceWindow = 30 * time.Millisecond

	t.Cleanup(func() {
		restartFetchMeta, restartFindPID = prevFetch, prevFind
		restartTerminate, restartSpawnDaemon = prevTerm, prevSpawn
		restartPollInterval, restartReadyDeadline = prevPoll, prevDeadline
		restartGraceWindow = prevGrace
	})
}

func current() string { return daemon.Version }

func serving() daemon.Metadata {
	return daemon.Metadata{Version: current(), Ready: true, Stale: false, LastError: ""}
}

// A daemon that is current, ready, not stale and error-free is left alone.
func TestDaemonRestartAlreadyServingIsUntouched(t *testing.T) {
	h := &restartHarness{metas: []daemon.Metadata{serving()}, pid: 111}
	h.install(t)

	var out bytes.Buffer
	if err := restartDaemon(context.Background(), "127.0.0.1:1", &out); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if h.terminates != 0 || h.spawns != 0 {
		t.Fatalf("healthy daemon must not be restarted: terminates=%d spawns=%d", h.terminates, h.spawns)
	}
}

// The live failure: Ready=false with a LastError was reported "already fresh".
// Readiness must consider Ready and LastError, not just version and staleness.
func TestDaemonRestartUnreadyWithLastErrorIsNotFresh(t *testing.T) {
	unready := daemon.Metadata{Version: current(), Ready: false, Stale: false, LastError: "refresh exploded"}
	h := &restartHarness{metas: []daemon.Metadata{unready}, pid: 111}
	h.install(t)

	var out bytes.Buffer
	_ = restartDaemon(context.Background(), "127.0.0.1:1", &out)
	if strings.Contains(out.String(), "already fresh") {
		t.Fatalf("a daemon with Ready=false and a LastError must not be called fresh: %q", out.String())
	}
}

// A current-version daemon that is merely still starting gets a bounded grace
// period and must not be terminated if it becomes ready within it.
func TestDaemonRestartUnreadyThenReadyAvoidsTermination(t *testing.T) {
	starting := daemon.Metadata{Version: current(), Ready: false, Stale: false}
	h := &restartHarness{metas: []daemon.Metadata{starting, starting, serving()}, pid: 111}
	h.install(t)

	var out bytes.Buffer
	if err := restartDaemon(context.Background(), "127.0.0.1:1", &out); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if h.terminates != 0 || h.spawns != 0 {
		t.Fatalf("a daemon that became ready in grace must not be restarted: terminates=%d spawns=%d", h.terminates, h.spawns)
	}
	// It must reach this outcome through the grace poll, not by being wrongly
	// short-circuited as already fresh while Ready was still false.
	if !strings.Contains(out.String(), "became ready") {
		t.Fatalf("expected the grace path, got %q", out.String())
	}
	if h.fetches < 2 {
		t.Fatalf("grace path must re-poll, only %d fetch(es)", h.fetches)
	}
}

// If the grace period expires, exactly one terminate/start cycle happens.
func TestDaemonRestartPersistentlyUnreadyRestartsExactlyOnce(t *testing.T) {
	starting := daemon.Metadata{Version: current(), Ready: false, Stale: false}
	h := &restartHarness{metas: []daemon.Metadata{starting}, pid: 111}
	h.install(t)

	var out bytes.Buffer
	_ = restartDaemon(context.Background(), "127.0.0.1:1", &out)
	if h.terminates != 1 {
		t.Fatalf("terminate count = %d, want exactly 1", h.terminates)
	}
	if h.spawns != 1 {
		t.Fatalf("spawn count = %d, want exactly 1", h.spawns)
	}
}

// Cancellation during the grace period aborts without touching the daemon.
func TestDaemonRestartContextCancelledDuringGrace(t *testing.T) {
	starting := daemon.Metadata{Version: current(), Ready: false, Stale: false}
	h := &restartHarness{metas: []daemon.Metadata{starting}, pid: 111}
	h.install(t)
	restartGraceWindow = 5 * time.Second // long enough that cancellation wins

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()

	var out bytes.Buffer
	err := restartDaemon(ctx, "127.0.0.1:1", &out)
	if err == nil {
		t.Fatal("expected a context error")
	}
	if h.terminates != 0 || h.spawns != 0 {
		t.Fatalf("cancellation must not restart: terminates=%d spawns=%d", h.terminates, h.spawns)
	}
}

// GUD-004: a ready daemon running a different binary version gets no grace and
// is restarted. This is the split-binary guard.
func TestDaemonRestartReadyButWrongVersionGetsNoGrace(t *testing.T) {
	wrong := daemon.Metadata{Version: "0.0.1-other", Ready: true, Stale: false}
	h := &restartHarness{metas: []daemon.Metadata{wrong, wrong, serving()}, pid: 111}
	h.install(t)

	var out bytes.Buffer
	_ = restartDaemon(context.Background(), "127.0.0.1:1", &out)
	if strings.Contains(out.String(), "already fresh") {
		t.Fatalf("a wrong-version daemon must never be called fresh: %q", out.String())
	}
	if h.terminates != 1 || h.spawns != 1 {
		t.Fatalf("wrong version must restart once without grace: terminates=%d spawns=%d", h.terminates, h.spawns)
	}
}

// A stale daemon also gets no grace.
func TestDaemonRestartStaleGetsNoGrace(t *testing.T) {
	stale := daemon.Metadata{Version: current(), Ready: true, Stale: true}
	h := &restartHarness{metas: []daemon.Metadata{stale, stale, serving()}, pid: 111}
	h.install(t)

	var out bytes.Buffer
	_ = restartDaemon(context.Background(), "127.0.0.1:1", &out)
	if h.terminates != 1 || h.spawns != 1 {
		t.Fatalf("stale must restart once without grace: terminates=%d spawns=%d", h.terminates, h.spawns)
	}
}
