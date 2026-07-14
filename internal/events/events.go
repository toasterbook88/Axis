// Package events provides an in-process event bus for AXIS lifecycle events.
// These events allow observablity and logging of placement decisions, task execution,
// daemon refreshes, and reservations.
package events

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/toasterbook88/axis/internal/cortex"
)

var (
	cortexClient *cortex.Client
	cortexMu     sync.Mutex
)

// SetCortexClient registers a Cortex client for publishing events cluster-wide.
func SetCortexClient(client *cortex.Client) {
	cortexMu.Lock()
	defer cortexMu.Unlock()
	cortexClient = client
}

// =============================================================================
// Event Name Constants
// =============================================================================

// Task execution lifecycle
const (
	// EventTaskPlacementRequested is emitted when a placement decision is about
	// to be made for a task.
	EventTaskPlacementRequested = "task.placement.requested"

	// EventTaskExecutionPre is emitted just before a task begins execution.
	EventTaskExecutionPre = "task.execution.pre"

	// EventTaskExecutionReserved is emitted when resources have been reserved for a task.
	EventTaskExecutionReserved = "task.execution.reserved"

	// EventTaskExecutionStarted can be used for the point where actual command execution begins.
	EventTaskExecutionStarted = "task.execution.started"

	// EventTaskExecutionPost is emitted after a task completes (success or failure).
	EventTaskExecutionPost = "task.execution.post"

	// EventTaskExecutionFinished is the final completion event.
	EventTaskExecutionFinished = "task.execution.finished"
)

// Reservation / Advisory Lease lifecycle
const (
	// EventReservationRequested is emitted when an advisory reservation/lease is being requested.
	EventReservationRequested = "reservation.requested"

	// EventReservationGranted is emitted when a reservation has been successfully recorded.
	EventReservationGranted = "reservation.granted"

	// EventReservationReleased is emitted when a reservation is released.
	EventReservationReleased = "reservation.released"
)

// Daemon & Snapshot lifecycle
const (
	// EventDaemonRefreshPre is emitted before a daemon snapshot refresh begins.
	EventDaemonRefreshPre = "daemon.refresh.pre"

	// EventDaemonRefreshPost is emitted after a daemon snapshot refresh completes.
	EventDaemonRefreshPost = "daemon.refresh.post"

	// EventSnapshotCollected is a general event when a cluster snapshot has been collected.
	EventSnapshotCollected = "snapshot.collected"
)

// =============================================================================
// Event Type
// =============================================================================

// Event represents a single lifecycle event emitted by AXIS.
type Event struct {
	ID        string         `json:"id"`
	Sequence  uint64         `json:"sequence"`
	Version   int            `json:"version"`
	Name      string         `json:"name"`
	Timestamp time.Time      `json:"timestamp"`
	Payload   map[string]any `json:"payload,omitempty"`
}

// PublishEnvelope wraps events published to the Cortex coordination layer.
type PublishEnvelope struct {
	EventID  string `json:"event_id"`
	Sequence uint64 `json:"sequence"`
	Event    Event  `json:"event"`
}

// =============================================================================
// Emission Helpers
// =============================================================================

// Emitter is the interface for components that can emit events.
type Emitter interface {
	Emit(event Event)
}

// NoopEmitter is a no-op implementation of Emitter.
type NoopEmitter struct{}

// Emit does nothing.
func (NoopEmitter) Emit(Event) {}

// Listener is a function that receives events (for in-process use).
type Listener func(Event)

type listenerEntry struct {
	id      int64
	fn      Listener
	filters []string
}

var (
	listeners      []listenerEntry
	nextListenerID int64
	listenerMu     sync.Mutex
)

// RegisterListener adds a listener that will be invoked for every emitted event and returns an unregister function.
// Optional filters can be provided (e.g. "task.*", "reservation.released", "*").
func RegisterListener(l Listener, filters ...string) func() {
	listenerMu.Lock()
	defer listenerMu.Unlock()
	id := nextListenerID
	nextListenerID++
	listeners = append(listeners, listenerEntry{id: id, fn: l, filters: filters})
	return func() {
		listenerMu.Lock()
		defer listenerMu.Unlock()
		for i, entry := range listeners {
			if entry.id == id {
				copy(listeners[i:], listeners[i+1:])
				listeners[len(listeners)-1] = listenerEntry{}
				listeners = listeners[:len(listeners)-1]
				break
			}
		}
	}
}

func notifyListeners(evt Event) {
	listenerMu.Lock()
	var active []Listener
	for _, entry := range listeners {
		if matchesFilters(evt.Name, entry.filters) {
			active = append(active, entry.fn)
		}
	}
	listenerMu.Unlock()

	if len(active) == 0 {
		return
	}
	startDispatchPool()
	for _, l := range active {
		enqueueDispatch(dispatchJob{fn: l, evt: evt})
	}
}

// =============================================================================
// Bounded Listener Dispatch Pool
// =============================================================================
//
// Listener callbacks run on a fixed-size worker pool fed by a buffered channel
// rather than one goroutine per (listener, event). This bounds goroutine and
// memory growth under bursty emission. When the queue is full the oldest
// pending job is dropped (listeners are advisory/observational — never on the
// critical path), so emitters never block.

const (
	dispatchWorkers   = 8
	dispatchQueueSize = 256
)

type dispatchJob struct {
	fn  Listener
	evt Event
}

var (
	dispatchQueue chan dispatchJob
	dispatchOnce  sync.Once
)

func startDispatchPool() {
	dispatchOnce.Do(func() {
		dispatchQueue = make(chan dispatchJob, dispatchQueueSize)
		for i := 0; i < dispatchWorkers; i++ {
			go dispatchWorker()
		}
	})
}

func dispatchWorker() {
	for job := range dispatchQueue {
		func() {
			defer func() { _ = recover() }()
			job.fn(job.evt)
		}()
	}
}

// enqueueDispatch offers a job to the pool without ever blocking. If the queue
// is full it drops the oldest queued job to make room; if it is still full it
// drops the incoming job.
func enqueueDispatch(job dispatchJob) {
	select {
	case dispatchQueue <- job:
		return
	default:
	}
	select {
	case <-dispatchQueue:
	default:
	}
	select {
	case dispatchQueue <- job:
	default:
	}
}

func matchesFilters(name string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, f := range filters {
		if f == "*" || f == name {
			return true
		}
		if strings.HasSuffix(f, ".*") {
			prefix := f[:len(f)-1] // e.g. "task."
			if strings.HasPrefix(name, prefix) {
				return true
			}
		}
	}
	return false
}

// =============================================================================
// Simple In-Memory Event Buffer
// =============================================================================

var (
	eventBuffer []Event
	bufferMu    sync.Mutex
	bufferSize  = 100
)

// =============================================================================
// Event Interest Registry (for MCP clients and future hooks)
// =============================================================================

var (
	interests  = make(map[string][]string) // eventName -> list of subscribers/callbacks
	interestMu sync.Mutex
)

// RegisterInterest records that a subscriber (e.g. an MCP client or callback tool)
// is interested in a particular event. This is advisory.
func RegisterInterest(eventName, subscriber string) {
	interestMu.Lock()
	defer interestMu.Unlock()
	interests[eventName] = append(interests[eventName], subscriber)
}

// GetInterests returns a copy of current event interests.
func GetInterests() map[string][]string {
	interestMu.Lock()
	defer interestMu.Unlock()

	out := make(map[string][]string, len(interests))
	for k, v := range interests {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// SetEventBufferSize adjusts the maximum number of events kept in the in-memory buffer.
func SetEventBufferSize(size int) {
	if size < 1 {
		size = 1
	}
	bufferMu.Lock()
	defer bufferMu.Unlock()
	bufferSize = size
	if len(eventBuffer) > bufferSize {
		discardCount := len(eventBuffer) - bufferSize
		for i := 0; i < discardCount; i++ {
			eventBuffer[i] = Event{}
		}
		eventBuffer = eventBuffer[discardCount:]
	}
}

// =============================================================================
// Asynchronous Event Queue and Background Worker
// =============================================================================

var (
	eventQueue      = make(chan Event, 1000)
	workerOnce      sync.Once
	inflightCounter int
	inflightMu      sync.Mutex
	flushChans      []chan struct{}
)

func inflightAdd(delta int) {
	inflightMu.Lock()
	inflightCounter += delta
	if inflightCounter == 0 {
		for _, ch := range flushChans {
			close(ch)
		}
		flushChans = nil
	}
	inflightMu.Unlock()
}

func startWorker() {
	workerOnce.Do(func() {
		go eventWorker()
	})
}

func eventWorker() {
	for evt := range eventQueue {
		processEvent(evt)
	}
}

func processEvent(evt Event) {
	defer inflightAdd(-1)

	// 1. Allocate sequence number under flock
	seq, err := allocateSequence()
	if err != nil {
		slog.Error("failed to allocate event sequence", "error", err)
	}
	evt.Sequence = seq

	// 2. Append to file-backed JSONL log
	_ = appendEventToFile(evt)

	// 3. Update in-memory ring buffer
	bufferMu.Lock()
	eventBuffer = append(eventBuffer, evt)
	if len(eventBuffer) > bufferSize {
		eventBuffer[0] = Event{}
		eventBuffer = eventBuffer[1:]
	}
	bufferMu.Unlock()

	// 4. Publish to Cortex asynchronously
	cortexMu.Lock()
	cClient := cortexClient
	cortexMu.Unlock()
	if cClient != nil {
		inflightAdd(1)
		go func(ev Event) {
			defer inflightAdd(-1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			envelope := PublishEnvelope{
				EventID:  ev.ID,
				Sequence: ev.Sequence,
				Event:    ev,
			}
			_ = cClient.PublishEvent(ctx, ev.Name, envelope)
		}(evt)
	}

	// 5. Dispatch webhooks asynchronously
	dispatchWebhooks(evt)

	// 6. Notify in-process listeners
	notifyListeners(evt)
}

// FlushEvents blocks until all enqueued events and their async webhook/Cortex
// dispatches have been processed, or until the timeout is reached.
// It returns nil on success and a non-nil error on timeout. Callers that
// require a drained bus (tests asserting log contents, TestMain cleanup)
// must check the error. Best-effort process exit may discard it.
//
// FlushEvents does not wait for in-process listener pool callbacks
// (notifyListeners); file append and sequence allocation complete inside the
// worker before listeners are enqueued.
func FlushEvents(timeout time.Duration) error {
	inflightMu.Lock()
	if inflightCounter == 0 {
		inflightMu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	flushChans = append(flushChans, ch)
	inflightMu.Unlock()

	select {
	case <-ch:
		return nil
	case <-time.After(timeout):
		inflightMu.Lock()
		for i, c := range flushChans {
			if c == ch {
				flushChans[i] = flushChans[len(flushChans)-1]
				flushChans = flushChans[:len(flushChans)-1]
				break
			}
		}
		inflightMu.Unlock()
		return fmt.Errorf("FlushEvents: timeout after %s", timeout)
	}
}

// EmitToBuffer is a convenience that enqueues the event into the asynchronous
// processing worker channel. Non-blocking to the main execution hot-path.
func EmitToBuffer(e Emitter, name string, payload map[string]any) {
	evt := NewEvent(name, payload)

	if e != nil {
		e.Emit(evt)
	}

	startWorker()

	inflightAdd(1)
	select {
	case eventQueue <- evt:
	default:
		inflightAdd(-1)
		slog.Warn("event queue full, discarding event", "name", evt.Name)
	}
}

// GetRecentEvents returns a copy of the most recent events, reading from the log file first.
func GetRecentEvents(limit int) []Event {
	if evs, err := getRecentEventsFromFile(limit); err == nil && len(evs) > 0 {
		return evs
	}

	bufferMu.Lock()
	defer bufferMu.Unlock()

	if limit <= 0 || limit > len(eventBuffer) {
		limit = len(eventBuffer)
	}
	if limit == 0 {
		return nil
	}

	out := make([]Event, limit)
	copy(out, eventBuffer[len(eventBuffer)-limit:])
	return out
}

// NewEvent is a convenience constructor that sets the ID, version, and timestamp.
func NewEvent(name string, payload map[string]any) Event {
	return Event{
		ID:        uuid.NewString(),
		Version:   1,
		Name:      name,
		Payload:   payload,
		Timestamp: time.Now().UTC(),
	}
}

// Emit is a helper that can be used with any Emitter.
func Emit(e Emitter, name string, payload map[string]any) {
	if e == nil {
		return
	}
	evt := NewEvent(name, payload)
	e.Emit(evt)
}

// =============================================================================
// Common Payload Keys
// =============================================================================

const (
	PayloadKeyNode      = "node"
	PayloadKeyTaskID    = "task_id"
	PayloadKeyTrigger   = "trigger"
	PayloadKeyResult    = "result"
	PayloadKeyReason    = "reason"
	PayloadKeyTimestamp = "timestamp"
)
