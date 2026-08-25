package console

import (
	"fmt"
	"io"
	"sync"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/agent"
)

// The bridge adapts internal/agent to the console's message loop.
//
// The agent has two output shapes and each gets its own adapter:
//
//	streamWriter  io.Writer      the assistant's token stream (unstructured)
//	Bridge        agent.Observer tool calls, results, turns (structured)
//
// Both only send messages. Neither writes to the terminal: a surface that owns
// the screen is corrupted by any write bypassing its renderer, and the agent
// reports tool results from parallel goroutines.

// sender delivers a message to a running program. *tea.Program satisfies it;
// tests substitute a recorder.
type sender interface {
	Send(tea.Msg)
}

// flushInterval is how long the stream writer accumulates before emitting.
// ColorWriter writes a single byte at a time, so without coalescing a routine
// response would produce thousands of messages and force a repaint for each.
const flushInterval = 30 * time.Millisecond

// maxPendingBytes forces a flush regardless of the timer, so a fast producer
// cannot let the buffer grow without bound between ticks.
const maxPendingBytes = 8 << 10

// streamWriter coalesces writes into StreamChunkMsg.
//
// It buffers bytes rather than strings: the agent writes one byte at a time,
// so converting each write to a string would split multi-byte runes into
// separate messages and corrupt any non-ASCII output. The buffer is flushed
// only up to the last complete rune; a trailing partial rune is carried over.
type streamWriter struct {
	to   sender
	turn TurnID

	mu      sync.Mutex
	pending []byte
	timer   *time.Timer
	closed  bool
}

// NewStreamWriter returns the writer to install as the agent's output sink for
// one turn. The turn id is captured here and never re-read, so a writer whose
// turn was abandoned keeps stamping that turn and its late flush is rejected
// rather than attributed to whatever turn is running when it lands.
//
// Callers must Close it at the end of a turn to flush the tail.
func NewStreamWriter(to sender, turn TurnID) io.WriteCloser {
	return &streamWriter{to: to, turn: turn}
}

// Write never fails and never blocks on the terminal.
func (w *streamWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return len(p), nil
	}

	w.pending = append(w.pending, p...)
	if len(w.pending) >= maxPendingBytes {
		w.flushLocked(false)
		return len(p), nil
	}
	if w.timer == nil {
		w.timer = time.AfterFunc(flushInterval, w.onTimer)
	}
	return len(p), nil
}

// Close flushes any buffered text, including a trailing partial rune, and
// stops further batching.
func (w *streamWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	w.flushLocked(true)
	return nil
}

func (w *streamWriter) onTimer() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.timer = nil
	w.flushLocked(false)
}

// flushLocked emits the buffer. Unless final, it stops at the last complete
// rune and carries the remainder forward so no message contains a half
// character. The caller must hold w.mu.
func (w *streamWriter) flushLocked(final bool) {
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	if len(w.pending) == 0 {
		return
	}

	cut := len(w.pending)
	if !final {
		cut = completeRuneBoundary(w.pending)
		if cut == 0 {
			return // only a partial rune buffered; wait for the rest
		}
	}

	text := string(w.pending[:cut])
	w.pending = append([]byte(nil), w.pending[cut:]...)

	if w.to != nil {
		w.to.Send(StreamChunkMsg{Turn: w.turn, Text: text})
	}
}

// completeRuneBoundary returns the length of the longest prefix of b that
// ends on a complete UTF-8 rune.
func completeRuneBoundary(b []byte) int {
	// A rune is at most 4 bytes, so a complete boundary is within 3 of the end.
	for i := len(b); i > 0 && i > len(b)-utf8.UTFMax; i-- {
		if r, size := utf8.DecodeLastRune(b[:i]); r != utf8.RuneError || size > 1 {
			return i
		}
	}
	if utf8.Valid(b) {
		return len(b)
	}
	return 0
}

// Bridge implements agent.Observer, converting loop events into transcript
// entries. Safe for concurrent use: it holds no mutable state after
// construction, and Send is safe from any goroutine.
type Bridge struct {
	to   sender
	turn TurnID
	now  func() time.Time

	// Verbose surfaces tool arguments and result sizes. Unlike the agent's own
	// verbose flag this is purely a rendering choice; the observer always
	// receives the full event.
	Verbose bool
}

// NewBridge returns an agent.Observer for one turn. Like the stream writer it
// captures its turn id at construction: a bridge belonging to an abandoned
// turn keeps stamping that turn, so its late events stay recognisable as
// stale. Construct one per turn and pass it to Agent.RunWithSinks.
//
// now may be nil, in which case time.Now is used.
func NewBridge(to sender, turn TurnID, now func() time.Time) *Bridge {
	if now == nil {
		now = time.Now
	}
	return &Bridge{to: to, turn: turn, now: now}
}

// compile-time check that the bridge satisfies the agent's observer contract.
var _ agent.Observer = (*Bridge)(nil)

func (b *Bridge) emit(e Entry) {
	if b.to != nil {
		b.to.Send(EntryMsg{Turn: b.turn, Entry: e})
	}
}

// TurnStarted renders a turn boundary. Only the agent's verbose mode emits
// these at all, so they are always shown when they arrive.
func (b *Bridge) TurnStarted(turn, max int) {
	b.emit(NewNoticeEntry(b.now(), fmt.Sprintf("turn %d/%d", turn, max)))
}

// CompactionSkipped reports that context compaction did not run.
func (b *Bridge) CompactionSkipped(err error) {
	b.emit(NewNoticeEntry(b.now(), "compaction skipped: "+err.Error()))
}

// ToolCalled opens a tool entry. Arguments are shown only in verbose mode: an
// argument blob can be large, and the transcript is a reading surface before
// it is a debugging one. The text is already redacted by internal/agent.
func (b *Bridge) ToolCalled(id, name, args string) {
	summary := ""
	if b.Verbose && args != "" {
		summary = args
	}
	b.emit(NewToolEntry(b.now(), id, name, summary))
}

// ToolSkipped records a call not executed, such as under --dry-run.
func (b *Bridge) ToolSkipped(id, name, reason string) {
	e := NewToolEntry(b.now(), id, name, "")
	e.Result = "skipped: " + reason
	b.emit(e)
}

// ToolSucceeded records a completed call.
func (b *Bridge) ToolSucceeded(id, name, summary string, resultLen int) {
	e := NewToolEntry(b.now(), id, name, "")
	e.Result = summary
	if b.Verbose {
		e.Result = fmt.Sprintf("%s (%d bytes)", summary, resultLen)
	}
	b.emit(e)
}

// ToolFailed records a call that returned an error.
func (b *Bridge) ToolFailed(id, name string, err error) {
	e := NewToolEntry(b.now(), id, name, "")
	e.Err = err
	b.emit(e)
}

// ShellExecuting records a command about to run. The command has passed the
// safety gate, been approved, and been redacted by internal/agent; showing it
// is what makes the transcript an audit trail.
func (b *Bridge) ShellExecuting(id, node, cwd, command string) {
	target := "local"
	if node != "" {
		target = node
	}
	if cwd != "" {
		target = fmt.Sprintf("%s:%s", target, cwd)
	}
	b.emit(NewToolEntry(b.now(), id, "shell", fmt.Sprintf("%s %s", target, command)))
}

// MaxTurnsReached reports the loop stopping at its ceiling. It is an error
// rather than a notice: the answer the operator asked for did not arrive.
func (b *Bridge) MaxTurnsReached(max int) {
	b.emit(NewErrorEntry(b.now(), fmt.Sprintf("stopped at maximum turns (%d)", max)))
}
