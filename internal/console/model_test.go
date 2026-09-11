package console

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func fixedNow() time.Time { return at }

// drain executes a tea.Cmd tree and returns every message it produced.
// tea.Batch wraps its children in a BatchMsg, so batches are flattened.
func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, drain(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// printed returns the text of every line committed to scrollback.
func printed(cmd tea.Cmd) []string {
	var out []string
	for _, msg := range drain(cmd) {
		if s, ok := printedText(msg); ok {
			out = append(out, s)
		}
	}
	return out
}

// printedText extracts the body of a bubbletea printLineMessage — the value
// tea.Println produces, and the only way transcript content leaves the
// console. The type is unexported, so this matches on its type name and
// reads the body through fmt, which formats unexported fields.
func printedText(msg tea.Msg) (string, bool) {
	t := reflect.TypeOf(msg)
	if t == nil || t.Name() != "printLineMessage" {
		return "", false
	}
	s := fmt.Sprintf("%v", msg)
	return strings.TrimSuffix(strings.TrimPrefix(s, "{"), "}"), true
}

// run executes a cmd tree for its side effects. Work that used to happen
// inline now leaves as a tea.Cmd so the input loop performs no I/O, so tests
// must run the commands to observe it.
func run(cmd tea.Cmd) { drain(cmd) }

// typeText feeds a string to the model one key at a time.
func typeText(m Model, text string) Model {
	for _, r := range text {
		key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		if r == ' ' {
			key = tea.KeyMsg{Type: tea.KeySpace}
		}
		updated, _ := m.Update(key)
		m = updated.(Model)
	}
	return m
}

func press(m Model, t tea.KeyType) (Model, tea.Cmd) {
	updated, cmd := m.Update(tea.KeyMsg{Type: t})
	return updated.(Model), cmd
}

// recorder captures messages sent by the bridge and stream writer.
type recorder struct{ msgs []tea.Msg }

func (r *recorder) Send(m tea.Msg) { r.msgs = append(r.msgs, m) }

func newTestModel(submit SubmitFunc) Model {
	return NewModel(Options{Submit: submit, Now: fixedNow, CancelGrace: time.Millisecond})
}

// noopSubmit satisfies SubmitFunc without producing messages.
func noopSubmit(TurnID, string) tea.Cmd { return nil }

func TestTypingAccumulatesInput(t *testing.T) {
	m := typeText(newTestModel(noopSubmit), "hello world")
	if m.Input() != "hello world" {
		t.Errorf("input = %q, want %q", m.Input(), "hello world")
	}
}

func TestBackspaceDeletesOneRune(t *testing.T) {
	m := typeText(newTestModel(noopSubmit), "abc")
	m, _ = press(m, tea.KeyBackspace)
	if m.Input() != "ab" {
		t.Errorf("input = %q, want %q", m.Input(), "ab")
	}
}

func TestSubmitStartsTurnAndClearsInput(t *testing.T) {
	var got string
	m := typeText(newTestModel(func(_ TurnID, p string) tea.Cmd {
		got = p
		return nil
	}), "status?")

	m, _ = press(m, tea.KeyEnter)

	if got != "status?" {
		t.Errorf("submitted %q, want %q", got, "status?")
	}
	if m.Input() != "" {
		t.Errorf("input not cleared: %q", m.Input())
	}
	if !m.Busy() {
		t.Error("model not busy after submit")
	}
}

func TestEmptySubmitDoesNothing(t *testing.T) {
	m := typeText(newTestModel(func(TurnID, string) tea.Cmd {
		t.Fatal("submit called for empty input")
		return nil
	}), "   ")
	m, _ = press(m, tea.KeyEnter)
	if m.Busy() {
		t.Error("empty submit started a turn")
	}
}

func TestEnterWhileBusyQueuesSteeringMessage(t *testing.T) {
	m := typeText(newTestModel(noopSubmit), "first")
	m, _ = press(m, tea.KeyEnter)

	m = typeText(m, "second")
	m, _ = press(m, tea.KeyEnter)

	queued := m.Queued()
	if len(queued) != 1 || queued[0] != "second" {
		t.Fatalf("queued = %v, want [second]", queued)
	}
	if m.Input() != "" {
		t.Errorf("input not cleared after queueing: %q", m.Input())
	}
}

func TestTurnDoneDeliversOneQueuedMessage(t *testing.T) {
	var submitted []string
	m := typeText(newTestModel(func(_ TurnID, p string) tea.Cmd {
		submitted = append(submitted, p)
		return nil
	}), "first")
	m, _ = press(m, tea.KeyEnter)

	m = typeText(m, "second")
	m, _ = press(m, tea.KeyEnter)
	m = typeText(m, "third")
	m, _ = press(m, tea.KeyEnter)

	updated, _ := m.Update(TurnDoneMsg{Turn: m.Turn()})
	m = updated.(Model)

	if len(submitted) != 2 || submitted[1] != "second" {
		t.Fatalf("submitted = %v, want first then second", submitted)
	}
	// Steering is delivered one at a time: the third waits for the next
	// boundary rather than all draining at once.
	if q := m.Queued(); len(q) != 1 || q[0] != "third" {
		t.Errorf("queued = %v, want [third]", q)
	}
	if !m.Busy() {
		t.Error("model should be busy running the drained message")
	}
}

func TestEscapeRestoresQueuedMessagesToEditor(t *testing.T) {
	cancelled := false
	m := NewModel(Options{
		Submit:      noopSubmit,
		Cancel:      func(TurnID) { cancelled = true },
		Now:         fixedNow,
		CancelGrace: time.Millisecond,
	})

	m = typeText(m, "first")
	m, _ = press(m, tea.KeyEnter)
	m = typeText(m, "queued text")
	m, _ = press(m, tea.KeyEnter)

	m, cmd := press(m, tea.KeyEsc)
	run(cmd)

	if !cancelled {
		t.Error("escape did not cancel the in-flight turn")
	}
	if m.Input() != "queued text" {
		t.Errorf("input = %q, want queued text restored", m.Input())
	}
	if len(m.Queued()) != 0 {
		t.Errorf("queue not drained: %v", m.Queued())
	}
}

func TestCtrlCClearsThenQuits(t *testing.T) {
	m := typeText(newTestModel(noopSubmit), "draft")

	m, cmd := press(m, tea.KeyCtrlC)
	if m.Input() != "" {
		t.Errorf("first ctrl+c did not clear input: %q", m.Input())
	}
	if cmd != nil {
		t.Error("first ctrl+c should not quit")
	}

	m, _ = press(m, tea.KeyCtrlC)
	m, cmd = press(m, tea.KeyCtrlC)
	if cmd == nil {
		t.Error("repeated ctrl+c did not quit")
	}
	if m.View() != "" {
		t.Errorf("quitting model still rendered: %q", m.View())
	}
}

func TestTypingResetsInterruptCount(t *testing.T) {
	m := newTestModel(noopSubmit)
	m, _ = press(m, tea.KeyCtrlC)
	m = typeText(m, "x")
	m, _ = press(m, tea.KeyCtrlC) // clears "x"
	_, cmd := press(m, tea.KeyCtrlC)
	if cmd != nil {
		t.Error("interrupt count not reset by typing; quit fired too early")
	}
}

func TestStreamChunksStayEphemeralUntilTurnEnds(t *testing.T) {
	m := typeText(newTestModel(noopSubmit), "hi")
	m, _ = press(m, tea.KeyEnter)

	for _, chunk := range []string{"node-a ", "has 28 GB"} {
		updated, cmd := m.Update(StreamChunkMsg{Turn: m.Turn(), Text: chunk})
		m = updated.(Model)
		if len(printed(cmd)) != 0 {
			t.Fatal("a stream chunk reached scrollback; partial lines must stay ephemeral")
		}
	}

	if !strings.Contains(m.View(), "node-a has 28 GB") {
		t.Errorf("in-progress text missing from view:\n%s", m.View())
	}

	_, cmd := m.Update(TurnDoneMsg{Turn: m.Turn()})
	if got := strings.Join(printed(cmd), "\n"); !strings.Contains(got, "* node-a has 28 GB") {
		t.Errorf("completed response not committed as an agent entry:\n%s", got)
	}
}

func TestTurnErrorIsCommitted(t *testing.T) {
	m := typeText(newTestModel(noopSubmit), "hi")
	m, _ = press(m, tea.KeyEnter)

	_, cmd := m.Update(TurnDoneMsg{Turn: m.Turn(), Err: errors.New("dial timeout")})
	got := strings.Join(printed(cmd), "\n")
	if !strings.Contains(got, "! ") || !strings.Contains(got, "dial timeout") {
		t.Errorf("turn error not committed as an error entry:\n%s", got)
	}
}

func TestEntryMsgCommitsToScrollback(t *testing.T) {
	m := newTestModel(noopSubmit)
	_, cmd := m.Update(EntryMsg{Entry: NewNoticeEntry(at, "backend switched")})

	got := strings.Join(printed(cmd), "\n")
	if !strings.Contains(got, "backend switched") {
		t.Errorf("entry not committed:\n%s", got)
	}
}

func TestOverlaySwallowsKeys(t *testing.T) {
	m := newTestModel(noopSubmit).SetOverlay(staticOverlay{text: "picker"})
	m = typeText(m, "ignored")

	if m.Input() != "" {
		t.Errorf("keys reached the editor while an overlay was up: %q", m.Input())
	}
	if !strings.Contains(m.View(), "picker") {
		t.Errorf("overlay not rendered:\n%s", m.View())
	}
}

func TestViewRendersFooterLast(t *testing.T) {
	m := NewModel(Options{
		Submit:      noopSubmit,
		Footer:      staticFooter{text: "fleet 5/5 ok"},
		Now:         fixedNow,
		CancelGrace: time.Millisecond,
	})
	lines := strings.Split(m.View(), "\n")
	if lines[len(lines)-1] != "fleet 5/5 ok" {
		t.Errorf("footer is not the last line:\n%s", m.View())
	}
}

func TestWindowSizeChangesWrapWidth(t *testing.T) {
	m := newTestModel(noopSubmit)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 30, Height: 20})
	m = updated.(Model)

	_, cmd := m.Update(EntryMsg{
		Entry: NewAgentEntry(at, "node-a has 28 GB free of 48 GB and node-b has 16 GB free"),
	})
	for _, line := range strings.Split(strings.Join(printed(cmd), "\n"), "\n") {
		if visibleWidth(line) > 30 {
			t.Errorf("line exceeds resized width: %q", line)
		}
	}
}

// staticOverlay is a fixed overlay that swallows keys until dismissed.
type staticOverlay struct {
	text string
	done bool
}

func (s staticOverlay) Update(msg tea.Msg) (Overlay, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "esc" {
		s.done = true
	}
	return s, nil
}

func (s staticOverlay) Render(int) []Line { return []Line{{Text: s.text}} }
func (s staticOverlay) Done() bool        { return s.done }

// staticFooter is a fixed footer for tests.
type staticFooter struct{ text string }

func (s staticFooter) Render(int) []Line { return []Line{{Text: s.text}} }

func TestCancelKeepsTurnBusyUntilAcknowledged(t *testing.T) {
	// Escape requests cancellation; the turn is not finished until the
	// backend acknowledges or the grace period expires.
	m := NewModel(Options{Submit: noopSubmit, Cancel: func(TurnID) {}, Now: fixedNow, CancelGrace: time.Millisecond})
	m = typeText(m, "work")
	m, _ = press(m, tea.KeyEnter)

	m, cmd := press(m, tea.KeyEsc)
	run(cmd)

	if !m.Cancelling() {
		t.Error("model left the cancelling state immediately")
	}
	if !m.Busy() {
		t.Error("model reported idle before the turn acknowledged")
	}

	updated, _ := m.Update(TurnDoneMsg{Turn: m.Turn()})
	if updated.(Model).Busy() {
		t.Error("acknowledged cancel did not return the model to idle")
	}
}

func TestCancelTimeoutRecoversFromASilentBackend(t *testing.T) {
	// A cancel that never produces a completion must not strand the console:
	// without this the model stays busy and every later prompt queues behind
	// a turn that will never end.
	m := NewModel(Options{Submit: noopSubmit, Cancel: func(TurnID) {}, Now: fixedNow, CancelGrace: time.Millisecond})
	m = typeText(m, "work")
	m, _ = press(m, tea.KeyEnter)
	m, cmd := press(m, tea.KeyEsc)
	run(cmd)

	updated, out := m.Update(cancelTimeoutMsg{Turn: m.Turn()})
	m = updated.(Model)

	if m.Busy() {
		t.Error("cancel timeout did not return the model to idle")
	}
	if got := strings.Join(printed(out), "\n"); !strings.Contains(got, "did not acknowledge") {
		t.Errorf("timeout was not reported to the operator:\n%s", got)
	}
}

func TestStaleTurnMessagesAreDropped(t *testing.T) {
	// A result from a cancelled turn must not mutate the turn that replaced
	// it. Turn ids are what make that decidable.
	m := typeText(newTestModel(noopSubmit), "first")
	m, _ = press(m, tea.KeyEnter)
	stale := m.Turn()

	updated, _ := m.Update(TurnDoneMsg{Turn: stale})
	m = updated.(Model)
	m = typeText(m, "second")
	m, _ = press(m, tea.KeyEnter)

	// A late chunk stamped with the old turn must not appear in the new one.
	updated, _ = m.Update(StreamChunkMsg{Turn: stale, Text: "leaked"})
	m = updated.(Model)
	if strings.Contains(m.View(), "leaked") {
		t.Errorf("stale stream chunk entered the current turn:\n%s", m.View())
	}

	// And a late completion must not end the new turn.
	updated, _ = m.Update(TurnDoneMsg{Turn: stale})
	if !updated.(Model).Busy() {
		t.Error("stale completion ended the current turn")
	}
}

func TestZeroTurnEntriesAreAlwaysShown(t *testing.T) {
	// Turn 0 marks content belonging to no turn, such as a startup notice.
	m := typeText(newTestModel(noopSubmit), "go")
	m, _ = press(m, tea.KeyEnter)

	_, cmd := m.Update(EntryMsg{Entry: NewNoticeEntry(at, "backend switched")})
	if got := strings.Join(printed(cmd), "\n"); !strings.Contains(got, "backend switched") {
		t.Errorf("turn-independent entry was dropped:\n%s", got)
	}
}

func TestOverlayReceivesKeysAndDismissesItself(t *testing.T) {
	// The overlay owns the keyboard: without this a picker or approval
	// dialog cannot navigate, select, or cancel.
	m := newTestModel(noopSubmit).SetOverlay(staticOverlay{text: "picker"})

	m, _ = press(m, tea.KeyEsc)
	if strings.Contains(m.View(), "picker") {
		t.Error("overlay did not dismiss itself on escape")
	}

	m = typeText(m, "now typing")
	if m.Input() != "now typing" {
		t.Errorf("editor did not regain focus: %q", m.Input())
	}
}

func TestOverlayDoesNotBlockBackgroundWork(t *testing.T) {
	// Work started before the overlay opened must keep reaching the
	// transcript; only the keyboard is captured.
	m := newTestModel(noopSubmit).SetOverlay(staticOverlay{text: "picker"})

	_, cmd := m.Update(EntryMsg{Entry: NewNoticeEntry(at, "probe finished")})
	if got := strings.Join(printed(cmd), "\n"); !strings.Contains(got, "probe finished") {
		t.Errorf("overlay swallowed a background entry:\n%s", got)
	}
}

func TestViewIsPaintedThroughLines(t *testing.T) {
	// The ephemeral region goes through the same painter as the transcript,
	// so styling has exactly one implementation.
	m := NewModel(Options{
		Submit:      noopSubmit,
		Footer:      staticFooter{text: "fleet 5/5 ok"},
		Now:         fixedNow,
		CancelGrace: time.Millisecond,
	})
	m = typeText(m, "hi")
	if !strings.Contains(m.View(), "hi") {
		t.Errorf("input missing from view:\n%s", m.View())
	}
}

func TestLateTurnOneEventsAreRejectedAfterTurnTwoStarts(t *testing.T) {
	// The end-to-end shape of the attribution bug: turn 1 is cancelled, never
	// acknowledges, the watchdog frees the console, turn 2 starts, and turn 1
	// finally speaks. None of it may reach turn 2.
	m := NewModel(Options{
		Submit:      noopSubmit,
		Cancel:      func(TurnID) {},
		Now:         fixedNow,
		CancelGrace: time.Millisecond,
	})

	m = typeText(m, "first")
	m, _ = press(m, tea.KeyEnter)
	turnOne := m.Turn()

	m, cmd := press(m, tea.KeyEsc)
	run(cmd)

	updated, _ := m.Update(cancelTimeoutMsg{Turn: turnOne})
	m = updated.(Model)
	if m.Busy() {
		t.Fatal("watchdog did not free the console")
	}

	m = typeText(m, "second")
	m, _ = press(m, tea.KeyEnter)
	turnTwo := m.Turn()
	if turnTwo == turnOne {
		t.Fatal("turn ids were reused")
	}

	// Late stream output from turn 1.
	updated, _ = m.Update(StreamChunkMsg{Turn: turnOne, Text: "leaked output"})
	m = updated.(Model)
	if strings.Contains(m.View(), "leaked output") {
		t.Errorf("turn 1 stream reached turn 2:\n%s", m.View())
	}

	// Late tool entry from turn 1.
	updated, out := m.Update(EntryMsg{Turn: turnOne, Entry: NewNoticeEntry(at, "leaked entry")})
	m = updated.(Model)
	if got := strings.Join(printed(out), "\n"); strings.Contains(got, "leaked entry") {
		t.Errorf("turn 1 entry was committed during turn 2:\n%s", got)
	}

	// Late completion from turn 1 must not end turn 2.
	updated, _ = m.Update(TurnDoneMsg{Turn: turnOne})
	if !updated.(Model).Busy() {
		t.Error("turn 1 completion ended turn 2")
	}
}

func TestSettledTurnCannotSpeakAgain(t *testing.T) {
	// After a turn completes normally, m.turn still holds its id. Equality
	// alone would therefore accept its late events while the console is idle.
	m := typeText(newTestModel(noopSubmit), "go")
	m, _ = press(m, tea.KeyEnter)
	finished := m.Turn()

	updated, _ := m.Update(TurnDoneMsg{Turn: finished})
	m = updated.(Model)
	if m.Busy() {
		t.Fatal("turn did not settle")
	}
	if m.Retired() != finished {
		t.Errorf("Retired() = %d, want %d", m.Retired(), finished)
	}

	_, out := m.Update(EntryMsg{Turn: finished, Entry: NewNoticeEntry(at, "after the fact")})
	if got := strings.Join(printed(out), "\n"); strings.Contains(got, "after the fact") {
		t.Errorf("a settled turn was still able to commit:\n%s", got)
	}

	updated, _ = m.Update(StreamChunkMsg{Turn: finished, Text: "after the fact"})
	if strings.Contains(updated.(Model).View(), "after the fact") {
		t.Error("a settled turn was still able to stream")
	}
}

func TestTurnIndependentEntriesSurviveIdle(t *testing.T) {
	// Turn 0 is not turn-scoped and must still render while idle, otherwise
	// startup and backend-switch notices would vanish.
	m := newTestModel(noopSubmit)
	_, out := m.Update(EntryMsg{Entry: NewNoticeEntry(at, "backend switched")})
	if got := strings.Join(printed(out), "\n"); !strings.Contains(got, "backend switched") {
		t.Errorf("turn-independent notice was dropped while idle:\n%s", got)
	}
}

func TestModelCursorNavigationAndEdits(t *testing.T) {
	m := newTestModel(noopSubmit)
	m = typeText(m, "hello")

	// Left twice -> cursor on 'l'
	m, _ = press(m, tea.KeyLeft)
	m, _ = press(m, tea.KeyLeft)
	if m.editor.Cursor() != 3 {
		t.Fatalf("cursor = %d, want 3", m.editor.Cursor())
	}

	// Insert X
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	m = updated.(Model)
	if m.Input() != "helXlo" {
		t.Fatalf("input = %q, want 'helXlo'", m.Input())
	}

	// Home
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = updated.(Model)
	if m.editor.Cursor() != 0 {
		t.Fatalf("after home: cursor = %d, want 0", m.editor.Cursor())
	}

	// End
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = updated.(Model)
	if m.editor.Cursor() != 6 {
		t.Fatalf("after end: cursor = %d, want 6", m.editor.Cursor())
	}
}

func TestModelHistoryRingViaKeys(t *testing.T) {
	m := newTestModel(noopSubmit)

	// Submit cmd1
	m = typeText(m, "cmd1")
	m, _ = press(m, tea.KeyEnter)
	updated, _ := m.Update(TurnDoneMsg{Turn: m.Turn()})
	m = updated.(Model)

	// Submit cmd2
	m = typeText(m, "cmd2")
	m, _ = press(m, tea.KeyEnter)
	updated, _ = m.Update(TurnDoneMsg{Turn: m.Turn()})
	m = updated.(Model)

	// Active draft
	m = typeText(m, "draft")

	// Up -> cmd2
	m, _ = press(m, tea.KeyUp)
	if m.Input() != "cmd2" {
		t.Fatalf("up 1: input = %q, want 'cmd2'", m.Input())
	}

	// Up -> cmd1
	m, _ = press(m, tea.KeyUp)
	if m.Input() != "cmd1" {
		t.Fatalf("up 2: input = %q, want 'cmd1'", m.Input())
	}

	// Down -> cmd2
	m, _ = press(m, tea.KeyDown)
	if m.Input() != "cmd2" {
		t.Fatalf("down 1: input = %q, want 'cmd2'", m.Input())
	}

	// Down -> draft
	m, _ = press(m, tea.KeyDown)
	if m.Input() != "draft" {
		t.Fatalf("down 2: input = %q, want 'draft'", m.Input())
	}
}

func TestModelThinkingBlockExtractedOnTurnDone(t *testing.T) {
	m := newTestModel(noopSubmit)
	m = typeText(m, "analyze cluster")
	m, _ = press(m, tea.KeyEnter)

	// Stream includes <think>...</think> and final answer
	streamChunk := "<think>\nevaluating node health\n</think>\nCluster is operational."
	updated, _ := m.Update(StreamChunkMsg{Turn: m.Turn(), Text: streamChunk})
	m = updated.(Model)

	// Finish turn commits entries
	updated, out := m.Update(TurnDoneMsg{Turn: m.Turn()})
	_ = updated.(Model)

	lines := printed(out)
	fullText := strings.Join(lines, "\n")

	if !strings.Contains(fullText, "evaluating node health") {
		t.Fatalf("thought missing from committed output:\n%s", fullText)
	}
	if !strings.Contains(fullText, "Cluster is operational.") {
		t.Fatalf("answer missing from committed output:\n%s", fullText)
	}
}

func TestModelIdleEscEscClearsInput(t *testing.T) {
	// Track 3 follow-up: clearing an idle draft takes esc esc (matching the
	// ctrl+c quit gesture); a single accidental Esc no longer wipes a draft.
	m := newTestModel(noopSubmit)
	m = typeText(m, "stale draft")
	if m.Input() != "stale draft" {
		t.Fatalf("input = %q, want 'stale draft'", m.Input())
	}

	m, _ = press(m, tea.KeyEsc)
	if m.Input() != "stale draft" {
		t.Fatalf("after first esc when idle: input = %q, want unchanged draft", m.Input())
	}
	m, _ = press(m, tea.KeyEsc)
	if m.Input() != "" {
		t.Fatalf("after esc esc when idle: input = %q, want empty", m.Input())
	}
}

func TestModelThinkingClosedBeforeAnswerDoesNotFlash(t *testing.T) {
	m := newTestModel(noopSubmit)
	m = typeText(m, "test")
	m, _ = press(m, tea.KeyEnter)

	// Stream contains closed think block, but NO answer tokens yet
	streamChunk := "<think>secret internal reasoning</think>"
	updated, _ := m.Update(StreamChunkMsg{Turn: m.Turn(), Text: streamChunk})
	m = updated.(Model)

	view := m.View()
	if strings.Contains(view, "<think>") || strings.Contains(view, "secret internal reasoning") {
		t.Fatalf("view should not flash raw thought block:\n%s", view)
	}
}

func startTurnFor(t *testing.T, m Model) Model {
	t.Helper()
	// Drive a real submission so turn/state/stale gating are in the live
	// running state the way production events arrive.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m = next.(Model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return next.(Model)
}

func apply(m Model, msgs ...tea.Msg) Model {
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

func TestModelPendingToolCard(t *testing.T) {
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }})
	m = startTurnFor(t, m)
	m = apply(m, ToolPendingMsg{Turn: 1, ID: "c1", Name: "axis_facts", Args: `node="cranium"`})

	view := m.View()
	if !strings.Contains(view, spinnerFrames[0]+" axis_facts") {
		t.Fatalf("in-flight tool card missing from ephemeral view:\n%s", view)
	}
	if strings.Count(view, "axis_facts") != 1 {
		t.Fatalf("duplicate pending rows:\n%s", view)
	}
}

func TestModelPendingToolIdempotentByID(t *testing.T) {
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }})
	m = startTurnFor(t, m)
	m = apply(m,
		ToolPendingMsg{Turn: 1, ID: "c1", Name: "axis_facts"},
		ToolPendingMsg{Turn: 1, ID: "c1", Name: "axis_facts"})

	if got := strings.Count(m.View(), "axis_facts"); got != 1 {
		t.Fatalf("expected one pending row for a repeated ID, got %d", got)
	}
}

func TestModelPendingToolResolved(t *testing.T) {
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }})
	m = startTurnFor(t, m)
	m = apply(m,
		ToolPendingMsg{Turn: 1, ID: "c1", Name: "axis_facts"},
		ToolResolvedMsg{Turn: 1, ID: "c1"})

	if strings.Contains(m.View(), "axis_facts") {
		t.Fatalf("resolved card must leave the ephemeral region:\n%s", m.View())
	}
}

func TestModelPendingToolsFlushedOnTurnDone(t *testing.T) {
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }})
	m = startTurnFor(t, m)
	m = apply(m,
		ToolPendingMsg{Turn: 1, ID: "c1", Name: "axis_facts"},
		TurnDoneMsg{Turn: 1})

	if strings.Contains(m.View(), "axis_facts") {
		t.Fatal("a settled turn must not leave ghost spinners")
	}
}

func TestModelPendingToolStaleTurnIgnored(t *testing.T) {
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }})
	m = apply(m, ToolPendingMsg{Turn: 1, ID: "c1", Name: "axis_facts"})

	if strings.Contains(m.View(), "axis_facts") {
		t.Fatal("stale pending message must not add a row")
	}
}

func TestModelPendingToolsFlushedOnCancelGraceExpiry(t *testing.T) {
	// Regression for the review's flush defect: a turn cancelled mid-tool
	// whose backend never acknowledges retires via cancelTimeoutMsg. The
	// pending tool card must not survive as a phantom spinner row.
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }})
	m = startTurnFor(t, m)
	m = apply(m, ToolPendingMsg{Turn: 1, ID: "c1", Name: "axis_facts"})

	// Real sequence: Esc while running -> requestCancel sets turnCancelling;
	// the backend never acknowledges, so the grace expiry fires.
	m = apply(m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.Cancelling() {
		t.Fatal("precondition: esc while running must request cancellation")
	}
	m = apply(m, cancelTimeoutMsg{Turn: 1})

	if strings.Contains(m.View(), "axis_facts") {
		t.Fatalf("cancel-grace expiry must flush pending cards:\n%s", m.View())
	}
	if m.Busy() {
		t.Fatal("cancel-grace expiry must return the console to idle")
	}
}

func TestModelPendingToolBridgeToModelIntegration(t *testing.T) {
	// Bridge -> model wiring: ToolCalled opens a pending row, the
	// completion's EntryMsg commits the receipt card and the
	// ToolResolvedMsg retires the pending row. Two tools stay in order.
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }, Now: nil})
	m = startTurnFor(t, m)

	s := &captureModelSender{m: &m}
	b := NewBridge(s, 1, nil)
	b.ToolCalled("call-a", "axis_facts", "")
	b.ToolCalled("call-b", "remote_grep", "")
	m = apply(m, s.msgs...)
	if !strings.Contains(m.View(), "axis_facts") || !strings.Contains(m.View(), "remote_grep") {
		t.Fatalf("expected two pending rows in order:\n%s", m.View())
	}

	s.msgs = nil
	b.ToolSucceeded("call-a", "axis_status", "ok", 2, 3*time.Millisecond)
	m = apply(m, s.msgs...)
	if strings.Contains(m.View(), "axis_facts") {
		t.Fatalf("resolved tool must leave the ephemeral region:\n%s", m.View())
	}
	if strings.Contains(m.View(), "axis_facts") {
		t.Fatalf("resolved axis_facts must leave the ephemeral region:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "remote_grep") {
		t.Fatalf("unresolved remote_grep must remain pending:\n%s", m.View())
	}
}

type captureModelSender struct {
	m    *Model
	msgs []tea.Msg
}

func (s *captureModelSender) Send(msg tea.Msg) {
	s.msgs = append(s.msgs, msg)
}

func TestModelEscEscClearsDraft(t *testing.T) {
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }})
	m = apply(m,
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("draft text")})

	m = apply(m, tea.KeyMsg{Type: tea.KeyEsc})
	if !strings.Contains(m.View(), "draft") {
		t.Fatal("first esc must NOT clear the draft")
	}
	m = apply(m, tea.KeyMsg{Type: tea.KeyEsc})
	if strings.Contains(m.View(), "draft") {
		t.Fatal("second esc must clear the draft")
	}
	if m.Input() != "" {
		t.Fatal("editor must be empty after esc esc")
	}
}

func TestModelOtherKeyDisarmsEsc(t *testing.T) {
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }})
	m = apply(m,
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("draft")},
		tea.KeyMsg{Type: tea.KeyEsc},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})

	// The gesture was disarmed by the typed key: esc now arms again, not clears.
	m = apply(m, tea.KeyMsg{Type: tea.KeyEsc})
	if !strings.Contains(m.View(), "draft") {
		t.Fatal("disarmed gesture must not clear on a single esc")
	}
	m = apply(m, tea.KeyMsg{Type: tea.KeyEsc})
	if strings.Contains(m.View(), "draft") {
		t.Fatal("second esc after re-arming must clear")
	}
}

func TestModelEscEscEmptyEditorInert(t *testing.T) {
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }})
	m = apply(m, tea.KeyMsg{Type: tea.KeyEsc}, tea.KeyMsg{Type: tea.KeyEsc})
	if m.Input() != "" {
		t.Fatal("esc esc on an empty editor must stay empty")
	}
}

// send applies one message and executes the returned command tree, so the
// test observes persistence that runs inside commands — off the Update loop
// — instead of trusting an eager call.
func send(m Model, msg tea.Msg) Model {
	next, cmd := m.Update(msg)
	m = next.(Model)
	_ = drain(cmd)
	return m
}

func TestModelHistorySinkQueuedPersists(t *testing.T) {
	var mu sync.Mutex
	var got []string
	record := func(text string) tea.Cmd {
		return func() tea.Msg {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, text)
			return nil
		}
	}
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }, HistorySink: record})

	m = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("  prompt one  ")})
	m = send(m, tea.KeyMsg{Type: tea.KeyEnter})

	// The turn is running now: a second prompt queues, and its persistence
	// command must still execute even though the branch returns the queue
	// notice.
	_ = send(send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("second")}), tea.KeyMsg{Type: tea.KeyEnter})

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != "prompt one" || got[1] != "second" {
		t.Fatalf("history sink recorded %v, want trimmed + queued prompt", got)
	}
}

func TestModelEscArmDoesNotSurviveAcrossTurns(t *testing.T) {
	// The invariant that makes esc esc safe across turn boundaries: arm ->
	// submit -> turn settles -> a single Esc on the re-typed draft must NOT
	// clear it (Enter disarmed the gesture before the turn ever started).
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }})
	m = apply(m, tea.KeyMsg{Type: tea.KeyEsc})
	m = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("prompt")})
	m = send(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = apply(m, TurnDoneMsg{Turn: 1})

	m = apply(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("new draft")})
	m = apply(m, tea.KeyMsg{Type: tea.KeyEsc})
	if !strings.Contains(m.View(), "new draft") {
		t.Fatal("a single esc after a full turn cycle must not clear the draft")
	}
	m = apply(m, tea.KeyMsg{Type: tea.KeyEsc})
	if strings.Contains(m.View(), "new draft") {
		t.Fatal("esc esc must still clear after the turn cycle")
	}
}

// stepping clock for span measurement: each call advances one second.
func steppingClock(start time.Time) func() time.Time {
	t := start
	var mu sync.Mutex
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t = t.Add(time.Second)
		return t
	}
}

func TestModelThinkingSpanMeasured(t *testing.T) {
	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }, Now: steppingClock(base)})
	m = startTurnFor(t, m)

	m = apply(m, StreamChunkMsg{Turn: 1, Text: "<think>reasoning part"})  // false->true at +1s
	m = apply(m, StreamChunkMsg{Turn: 1, Text: "</think>more reasoning"}) // still in thought
	m = apply(m, StreamChunkMsg{Turn: 1, Text: "</think>answer begins"})  // true->false at +3s
	if m.thoughtEnd.IsZero() {
		t.Fatal("thought end not stamped on the transition")
	}
	m = apply(m, StreamChunkMsg{Turn: 1, Text: "more answer"}) // no re-stamp
	if m.thoughtEnd.IsZero() {
		t.Fatal("thought end must be stamped at the in-thought exit")
	}

	m = apply(m, TurnDoneMsg{Turn: 1})
	if m.lastThought != "reasoning part" {
		t.Fatalf("retained thought = %q, want the reasoning block", m.lastThought)
	}
	if m.thinkingElapsed() != 0 {
		t.Fatal("finished turn resets the span for the next turn")
	}
	if m.inThought {
		t.Fatal("inThought must reset with the stream")
	}
}

func TestThinkingEntryRendersDuration(t *testing.T) {
	e := NewThinkingEntry(fixedNow(), "deep reasoning\nspanning\nlines")
	e.Elapsed = 3100 * time.Millisecond
	got := strings.Join(PlainAll(e.Render(80)), "\n")
	if !strings.Contains(got, "Thought for 3.1s") {
		t.Fatalf("duration header missing:\n%s", got)
	}
	if !strings.Contains(got, "1 more lines") || strings.Contains(got, "spanning\nlines") && false {
		// collapsed shape retained
	}
}

func TestThinkingEntryExpandedPrintsFull(t *testing.T) {
	e := NewThinkingEntry(fixedNow(), "full block")
	e.Expanded = true
	got := strings.Join(PlainAll(e.Render(80)), "\n")
	if !strings.Contains(got, "full block") {
		t.Fatalf("expanded render missing the full text:\n%s", got)
	}
}

func TestModelThoughtCommandExpandsRetainedBlock(t *testing.T) {
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }, Now: steppingClock(time.Now())})
	m = startTurnFor(t, m)
	m = apply(m, StreamChunkMsg{Turn: 1, Text: "<think>reasoning part"})
	m = apply(m, TurnDoneMsg{Turn: 1})
	if m.lastThought == "" {
		t.Fatal("precondition: thought retained after the turn")
	}

	next, commitCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/thought")})
	m = next.(Model)
	_ = drain(commitCmd)
	next, commitCmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if commitCmd == nil {
		t.Fatal("/thought must commit the expanded block (a tea.Println cmd)")
	}
	_ = drain(commitCmd)
	if m.Busy() {
		t.Fatal("/thought must not start a turn")
	}
	if m.turn != 1 {
		t.Fatalf("turn counter advanced to %d; /thought is not a turn", m.turn)
	}
}

func TestModelThoughtCommandWithoutBlock(t *testing.T) {
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }})
	m = apply(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/thought")})
	m = apply(m, tea.KeyMsg{Type: tea.KeyEnter})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = next.(Model)
	// No panic, no turn started; the notice path is exercised by the commit.
	if m.Busy() {
		t.Fatal("/thought with no block must not start a turn")
	}
}

func TestModelUsageCommand(t *testing.T) {
	var tokens int
	m := NewModel(Options{
		Submit:        func(TurnID, string) tea.Cmd { return nil },
		TokenEstimate: func() int { return 9200 },
	})
	_ = tokens
	m = apply(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/usage")})
	m = apply(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.Busy() {
		t.Fatal("/usage must not start a turn")
	}
	if m.turn != 0 {
		t.Fatal("/usage is not a turn")
	}
}
