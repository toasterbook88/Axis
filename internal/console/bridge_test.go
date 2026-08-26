package console

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// entries returns the rendered entries a recorder captured.
func (r *recorder) entries(width int) []string {
	var out []string
	for _, msg := range r.msgs {
		if e, ok := msg.(EntryMsg); ok {
			out = append(out, strings.Join(PlainAll(e.Entry.Render(width)), "\n"))
		}
	}
	return out
}

func TestStreamWriterCoalescesByteWrites(t *testing.T) {
	// ColorWriter emits one byte at a time. Without coalescing, a routine
	// response would produce thousands of messages and a repaint for each.
	rec := &concurrentSender{}
	w := NewStreamWriter(rec, 1)

	for _, b := range []byte("node-a has 28 GB free") {
		if n, err := w.Write([]byte{b}); err != nil || n != 1 {
			t.Fatalf("Write = (%d, %v), want (1, nil)", n, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close returned %v", err)
	}

	msgs := rec.chunks()
	if len(msgs) == 0 {
		t.Fatal("no chunk emitted")
	}
	if len(msgs) > 2 {
		t.Errorf("21 byte writes produced %d messages, want them coalesced", len(msgs))
	}
	if got := strings.Join(msgs, ""); got != "node-a has 28 GB free" {
		t.Errorf("reassembled stream = %q", got)
	}
}

func TestStreamWriterNeverSplitsAMultiByteRune(t *testing.T) {
	// The agent writes bytes, not runes. Emitting each write as a string
	// would cut multi-byte characters into separate messages.
	rec := &concurrentSender{}
	w := NewStreamWriter(rec, 1)

	const text = "日本語のテキスト"
	for _, b := range []byte(text) {
		if _, err := w.Write([]byte{b}); err != nil {
			t.Fatalf("Write returned %v", err)
		}
		for _, chunk := range rec.chunks() {
			if !utf8.ValidString(chunk) {
				t.Fatalf("emitted invalid UTF-8: %q", chunk)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close returned %v", err)
	}
	if got := strings.Join(rec.chunks(), ""); got != text {
		t.Errorf("reassembled = %q, want %q", got, text)
	}
}

func TestStreamWriterFlushesWhenBufferIsLarge(t *testing.T) {
	// A fast producer must not let the buffer grow unbounded between ticks.
	rec := &concurrentSender{}
	w := NewStreamWriter(rec, 1)

	big := strings.Repeat("a", maxPendingBytes+16)
	if _, err := w.Write([]byte(big)); err != nil {
		t.Fatalf("Write returned %v", err)
	}
	if len(rec.chunks()) == 0 {
		t.Error("oversized write did not force a flush")
	}
}

func TestStreamWriterStampsTheActiveTurn(t *testing.T) {
	rec := &concurrentSender{}
	w := NewStreamWriter(rec, 7)

	if _, err := w.Write([]byte("hi")); err != nil {
		t.Fatalf("Write returned %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close returned %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, msg := range rec.msgs {
		if c, ok := msg.(StreamChunkMsg); ok && c.Turn != 7 {
			t.Errorf("chunk turn = %d, want 7", c.Turn)
		}
	}
}

func TestStreamWriterIsSilentAfterClose(t *testing.T) {
	rec := &concurrentSender{}
	w := NewStreamWriter(rec, 1)
	if err := w.Close(); err != nil {
		t.Fatalf("Close returned %v", err)
	}
	if _, err := w.Write([]byte("late")); err != nil {
		t.Fatalf("Write returned %v", err)
	}
	if got := strings.Join(rec.chunks(), ""); got != "" {
		t.Errorf("writer emitted %q after close", got)
	}
}

func TestStreamWriterIgnoresEmptyWrites(t *testing.T) {
	rec := &recorder{}
	if _, err := NewStreamWriter(rec, 1).Write(nil); err != nil {
		t.Fatalf("Write returned %v", err)
	}
	if len(rec.msgs) != 0 {
		t.Errorf("empty write produced %d messages", len(rec.msgs))
	}
}

func TestStreamWriterToleratesNilSender(t *testing.T) {
	// The writer is installed before the program starts in some paths; a nil
	// sender must degrade to a discard rather than panic mid-turn.
	if _, err := NewStreamWriter(nil, 1).Write([]byte("x")); err != nil {
		t.Fatalf("Write returned %v", err)
	}
}

func TestBridgeRendersToolLifecycle(t *testing.T) {
	rec := &recorder{}
	b := NewBridge(rec, 1, fixedNow)

	b.ToolCalled("call-1", "axis_status", `{"cached":true}`)
	b.ToolSucceeded("call-1", "axis_status", "5 nodes", 42)
	b.ToolFailed("call-2", "remote_grep", errors.New("dial timeout"))

	got := rec.entries(80)
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3:\n%s", len(got), strings.Join(got, "\n"))
	}
	if !strings.HasPrefix(got[0], "$ axis_status") {
		t.Errorf("tool call entry = %q", got[0])
	}
	if !strings.Contains(got[1], "5 nodes") {
		t.Errorf("success entry missing summary: %q", got[1])
	}
	if !strings.Contains(got[2], "error: dial timeout") {
		t.Errorf("failure entry missing error: %q", got[2])
	}
}

func TestBridgeHidesToolArgumentsUnlessVerbose(t *testing.T) {
	quiet := &recorder{}
	NewBridge(quiet, 1, fixedNow).ToolCalled("call-1", "axis_status", `{"cached":true}`)
	if strings.Contains(strings.Join(quiet.entries(80), ""), "cached") {
		t.Error("non-verbose bridge leaked tool arguments into the transcript")
	}

	loud := &recorder{}
	b := NewBridge(loud, 1, fixedNow)
	b.Verbose = true
	b.ToolCalled("call-1", "axis_status", `{"cached":true}`)
	if !strings.Contains(strings.Join(loud.entries(80), ""), "cached") {
		t.Error("verbose bridge dropped tool arguments")
	}
}

func TestBridgeShellTargets(t *testing.T) {
	cases := []struct {
		name           string
		node, cwd, cmd string
		want           string
	}{
		{"local", "", "", "ls", "local ls"},
		{"local with cwd", "", "/srv", "ls", "local:/srv ls"},
		{"remote", "node-a", "", "ls", "node-a ls"},
		{"remote with cwd", "node-a", "/srv", "ls", "node-a:/srv ls"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			NewBridge(rec, 1, fixedNow).ShellExecuting("call-1", tc.node, tc.cwd, tc.cmd)
			got := strings.Join(rec.entries(80), "")
			if !strings.Contains(got, tc.want) {
				t.Errorf("got %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestBridgeMaxTurnsIsAnError(t *testing.T) {
	// Hitting the ceiling means the operator's question went unanswered, so
	// it is surfaced as an error rather than a quiet notice.
	rec := &recorder{}
	NewBridge(rec, 1, fixedNow).MaxTurnsReached(25)

	for _, msg := range rec.msgs {
		if e, ok := msg.(EntryMsg); ok {
			if e.Entry.Kind() != KindError {
				t.Errorf("max turns rendered as %s, want %s", e.Entry.Kind(), KindError)
			}
			return
		}
	}
	t.Fatal("no entry emitted")
}

func TestBridgeNoticesAreTimestamped(t *testing.T) {
	rec := &recorder{}
	b := NewBridge(rec, 1, fixedNow)
	b.TurnStarted(1, 25)
	b.CompactionSkipped(errors.New("budget"))

	for _, line := range rec.entries(80) {
		if !strings.Contains(line, "21:35:00") {
			t.Errorf("runtime notice missing timestamp: %q", line)
		}
	}
}

func TestBridgeToleratesNilSender(t *testing.T) {
	b := NewBridge(nil, 1, fixedNow)
	b.ToolCalled("call-1", "axis_status", "")
	b.MaxTurnsReached(1) // must not panic
}

func TestBridgeDefaultsToWallClock(t *testing.T) {
	rec := &recorder{}
	NewBridge(rec, 1, nil).TurnStarted(1, 2)
	if len(rec.msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(rec.msgs))
	}
}

// concurrentSender is a Send implementation safe for parallel use, matching
// what a running tea.Program provides.
type concurrentSender struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (s *concurrentSender) Send(m tea.Msg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, m)
}

// chunks returns the stream fragments captured so far.
func (s *concurrentSender) chunks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, msg := range s.msgs {
		if c, ok := msg.(StreamChunkMsg); ok {
			out = append(out, c.Text)
		}
	}
	return out
}

func TestBridgeIsSafeUnderParallelToolResults(t *testing.T) {
	// internal/agent reports tool results from its parallel dispatch
	// goroutines, so the observer is called concurrently.
	s := &concurrentSender{}
	b := NewBridge(s, 1, fixedNow)

	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				b.ToolSucceeded("call", "axis_status", "ok", i)
				return
			}
			b.ToolFailed("call", "remote_grep", errors.New("boom"))
		}(i)
	}
	wg.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.msgs) != 32 {
		t.Errorf("got %d messages, want 32", len(s.msgs))
	}
}

func TestBridgeStampsEntriesWithTheActiveTurn(t *testing.T) {
	rec := &recorder{}
	NewBridge(rec, 3, fixedNow).ToolCalled("call-1", "axis_status", "")

	for _, msg := range rec.msgs {
		if e, ok := msg.(EntryMsg); ok && e.Turn != 3 {
			t.Errorf("entry turn = %d, want 3", e.Turn)
		}
	}
}

func TestBridgePreservesToolCallIDs(t *testing.T) {
	// Parallel dispatch means a completion must be traceable to its call.
	rec := &recorder{}
	b := NewBridge(rec, 1, fixedNow)
	b.ToolCalled("call-a", "axis_status", "")
	b.ToolSucceeded("call-a", "axis_status", "ok", 2)
	b.ToolFailed("call-b", "remote_grep", errors.New("boom"))

	var ids []string
	for _, msg := range rec.msgs {
		e, ok := msg.(EntryMsg)
		if !ok {
			continue
		}
		tool, ok := e.Entry.(*ToolEntry)
		if !ok {
			t.Fatalf("expected a ToolEntry, got %T", e.Entry)
		}
		ids = append(ids, tool.ID)
	}
	want := []string{"call-a", "call-a", "call-b"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("id %d = %q, want %q", i, ids[i], want[i])
		}
	}
}

func TestProducersCaptureTheirTurnAndNeverRereadIt(t *testing.T) {
	// The core of turn attribution: a producer belonging to an abandoned turn
	// keeps stamping that turn, whatever is running when its output lands.
	// Reading a shared "current turn" at emit time would relabel late output
	// as belonging to the turn that replaced it.
	rec := &concurrentSender{}
	w := NewStreamWriter(rec, 1)
	b := NewBridge(rec, 1, fixedNow)

	// Turn 2 has since started and has its own producers.
	_ = NewStreamWriter(rec, 2)
	_ = NewBridge(rec, 2, fixedNow)

	// Turn 1's abandoned producers emit late.
	if _, err := w.Write([]byte("late output")); err != nil {
		t.Fatalf("Write returned %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close returned %v", err)
	}
	b.ToolSucceeded("call-1", "axis_status", "late result", 11)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.msgs) == 0 {
		t.Fatal("no messages emitted")
	}
	for _, msg := range rec.msgs {
		switch m := msg.(type) {
		case StreamChunkMsg:
			if m.Turn != 1 {
				t.Errorf("late chunk stamped turn %d, want 1", m.Turn)
			}
		case EntryMsg:
			if m.Turn != 1 {
				t.Errorf("late entry stamped turn %d, want 1", m.Turn)
			}
		}
	}
}
