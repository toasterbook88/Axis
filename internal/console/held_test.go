package console

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const wantNoModelNotice = "no model available — start one with axis model start <name>, or configure ~/.axis/ai.yaml"

type heldHarness struct {
	m      Model
	calls  []string
	output []string // everything committed to scrollback, in order
}

func newHeld(state ModelState) *heldHarness {
	h := &heldHarness{}
	h.m = NewModel(Options{
		Now:               fixedNow,
		CancelGrace:       time.Millisecond,
		Unicode:           true,
		InitialModelState: state,
		Submit: func(_ TurnID, p string) tea.Cmd {
			h.calls = append(h.calls, p)
			return nil
		},
	})
	return h
}

func (h *heldHarness) send(msg tea.Msg) {
	updated, cmd := h.m.Update(msg)
	h.m = updated.(Model)
	for _, l := range printed(cmd) {
		// Entries wrap at the console width; compare on single-spaced text.
		h.output = append(h.output, strings.Join(strings.Fields(l), " "))
	}
}

func (h *heldHarness) enter(text string) {
	h.m = typeText(h.m, text)
	h.send(tea.KeyMsg{Type: tea.KeyEnter})
}

func (h *heldHarness) count(sub string) int { return countContaining(h.output, sub) }

func TestHeldPrompts_OrderAndOnce(t *testing.T) {
	h := newHeld(ModelPending)
	h.enter("first")
	h.enter("second")
	if len(h.calls) != 0 {
		t.Fatalf("Submit called while pending: %v", h.calls)
	}
	if !strings.Contains(h.m.View(), "waiting for model…") {
		t.Fatalf("no waiting line:\n%s", h.m.View())
	}
	if len(h.m.Queued()) != 0 {
		t.Fatalf("held prompt leaked into steering queue: %v", h.m.Queued())
	}
	h.send(ModelStateMsg{State: ModelReady})
	if len(h.calls) != 1 || h.calls[0] != "first" {
		t.Fatalf("calls after ready = %v", h.calls)
	}
	h.send(TurnDoneMsg{Turn: h.m.Turn()})
	if len(h.calls) != 2 || h.calls[1] != "second" {
		t.Fatalf("calls after done = %v", h.calls)
	}
	h.send(TurnDoneMsg{Turn: h.m.Turn()})
	if len(h.calls) != 2 {
		t.Fatalf("prompt resent: %v", h.calls)
	}
	if h.count("first") != 1 || h.count("second") != 1 {
		t.Fatalf("user entries not committed exactly once: %q", h.output)
	}
	if strings.Contains(h.m.View(), "waiting for model…") {
		t.Fatal("waiting line persists after release")
	}
}

func TestHeldPrompts_CommandsBypass(t *testing.T) {
	h := newHeld(ModelPending)
	h.enter("/status")
	h.send(TurnDoneMsg{Turn: h.m.Turn()})
	h.enter("!ls")
	if len(h.calls) != 2 || h.calls[0] != "/status" || h.calls[1] != "!ls" {
		t.Fatalf("calls = %v", h.calls)
	}
}

func TestHeldPrompts_ReleasedAfterBusyTurn(t *testing.T) {
	h := newHeld(ModelPending)
	h.enter("/status")
	h.enter("hello")
	if len(h.m.Queued()) != 0 {
		t.Fatalf("held prompt queued as steering: %v", h.m.Queued())
	}
	h.send(ModelStateMsg{State: ModelReady})
	if len(h.calls) != 1 {
		t.Fatalf("held prompt sent during busy turn: %v", h.calls)
	}
	h.send(TurnDoneMsg{Turn: h.m.Turn()})
	if len(h.calls) != 2 || h.calls[1] != "hello" {
		t.Fatalf("calls = %v", h.calls)
	}
	h.send(TurnDoneMsg{Turn: h.m.Turn()})
	if len(h.calls) != 2 || h.count("hello") != 1 {
		t.Fatalf("calls = %v output = %q", h.calls, h.output)
	}
}

func TestHeldPrompts_QueuedBeforeHeldInFinishTurn(t *testing.T) {
	// Ready: "a" runs, "b" is queued steering. Flip to pending and hold "c";
	// finishing "a" must start only "b", never both.
	h := newHeld(ModelReady)
	h.enter("a")
	h.enter("b")
	h.send(ModelStateMsg{State: ModelPending})
	h.enter("c")
	h.send(ModelStateMsg{State: ModelReady})
	h.send(TurnDoneMsg{Turn: h.m.Turn()})
	if got := strings.Join(h.calls, ","); got != "a,b" {
		t.Fatalf("calls = %s, want a,b", got)
	}
	h.send(TurnDoneMsg{Turn: h.m.Turn()})
	if got := strings.Join(h.calls, ","); got != "a,b,c" {
		t.Fatalf("calls = %s, want a,b,c", got)
	}
}

func TestHeldPrompts_NoModel(t *testing.T) {
	h := newHeld(ModelPending)
	h.enter("hello")
	h.send(ModelStateMsg{State: ModelNone})
	if n := h.count(wantNoModelNotice); n != 1 {
		t.Fatalf("notice count = %d in %q", n, h.output)
	}
	if strings.Contains(h.m.View(), "waiting for model…") {
		t.Fatal("held not cleared")
	}
	h.send(ModelStateMsg{State: ModelNone})
	if n := h.count(wantNoModelNotice); n != 1 {
		t.Fatalf("repeated None re-noticed: %d", n)
	}
	h.enter("again")
	if h.count("again") != 1 || h.count(wantNoModelNotice) != 2 {
		t.Fatalf("output = %q", h.output)
	}
	if len(h.calls) != 0 {
		t.Fatalf("Submit called without a model: %v", h.calls)
	}
}

func TestHeldPrompts_CancelClears(t *testing.T) {
	h := newHeld(ModelPending)
	h.enter("one")
	h.enter("two")
	h.send(tea.KeyMsg{Type: tea.KeyCtrlC})
	if h.count("cancelled: 2 waiting prompt(s)") != 1 {
		t.Fatalf("output = %q", h.output)
	}
	if h.m.quitting {
		t.Fatal("cancel counted toward quit")
	}
	h.send(tea.KeyMsg{Type: tea.KeyCtrlC})
	if h.m.quitting {
		t.Fatal("second ctrl+c quit: the cancel must not count toward the gesture")
	}
	h.send(ModelStateMsg{State: ModelReady})
	if len(h.calls) != 0 {
		t.Fatalf("cancelled prompts sent: %v", h.calls)
	}
}

func TestHeldPrompts_EscCancelsWhenIdle(t *testing.T) {
	h := newHeld(ModelPending)
	h.enter("one")
	h.send(tea.KeyMsg{Type: tea.KeyEsc})
	if h.count("cancelled: 1 waiting prompt(s)") != 1 {
		t.Fatalf("output = %q", h.output)
	}
}

func TestHeldPrompts_PickerFixesNoModel(t *testing.T) {
	h := newHeld(ModelNone)
	h.enter("first")
	h.send(ModelStateMsg{State: ModelReady})
	h.enter("hello")
	if len(h.calls) != 1 || h.calls[0] != "hello" {
		t.Fatalf("calls = %v", h.calls)
	}
}

func TestHeldPrompts_AnimatesWhileWaiting(t *testing.T) {
	h := newHeld(ModelPending)
	if h.m.animating() {
		t.Fatal("animating with nothing held")
	}
	h.m = typeText(h.m, "hi")
	updated, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	h.m = updated.(Model)
	if !h.m.animating() {
		t.Fatal("not animating while a prompt is held")
	}
	if !h.m.ticking {
		t.Fatal("tick chain not started when a prompt became held")
	}
	_ = cmd
	h.send(ModelStateMsg{State: ModelNone})
	if h.m.animating() {
		t.Fatal("still animating after held cleared")
	}
}

func TestRunCommandMsg(t *testing.T) {
	h := newHeld(ModelPending)
	h.send(RunCommandMsg{Text: "/model"})
	if len(h.calls) != 1 || h.calls[0] != "/model" {
		t.Fatalf("calls = %v", h.calls)
	}
	if h.count("/model") != 1 {
		t.Fatalf("user entry not committed once: %q", h.output)
	}
}

func TestRunCommandMsg_DismissesStart(t *testing.T) {
	m := newStartModel(Options{Unicode: true, InitialModelState: ModelPending})
	updated, cmd := m.Update(RunCommandMsg{Text: "/model"})
	m = updated.(Model)
	if m.startVisible() {
		t.Fatal("start screen still visible")
	}
	if n := countContaining(printed(cmd), "▲ AXIS agent"); n != 1 {
		t.Fatalf("header count = %d", n)
	}
}

func TestHeaderLine_NoModelWhenStateNone(t *testing.T) {
	m := newStartModel(Options{Unicode: true, InitialModelState: ModelNone})
	if got := m.headerLine(); !strings.HasSuffix(got, "· no model") {
		t.Fatalf("header = %q", got)
	}
}

func TestFooterGetsModelStateAndLayoutWidth(t *testing.T) {
	var got FooterState
	var w int
	m := newStartModel(Options{Unicode: true, InitialModelState: ModelPending, Footer: footerFunc(func(width int, st FooterState) []Line {
		got, w = st, width
		return nil
	})})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 0, Height: 0})
	_ = updated.(Model).View()
	if got.Model != ModelPending || w != defaultWidth {
		t.Fatalf("footer state = %+v width = %d", got, w)
	}
}

type footerFunc func(int, FooterState) []Line

func (f footerFunc) Render(w int, st FooterState) []Line { return f(w, st) }

func TestHeldPrompts_LaterPromptCannotOvertake(t *testing.T) {
	h := newHeld(ModelPending)
	h.enter("first")
	h.enter("second")
	h.send(ModelStateMsg{State: ModelReady})
	h.enter("third")
	if len(h.m.Queued()) != 0 {
		t.Fatalf("third queued as steering: %v", h.m.Queued())
	}
	h.send(TurnDoneMsg{Turn: h.m.Turn()})
	h.send(TurnDoneMsg{Turn: h.m.Turn()})
	h.send(TurnDoneMsg{Turn: h.m.Turn()})
	if got := strings.Join(h.calls, ","); got != "first,second,third" {
		t.Fatalf("calls = %s", got)
	}
	if h.count("third") != 1 || h.count("second") != 1 {
		t.Fatalf("entries = %q", h.output)
	}
}

func TestHeldPrompts_WaitLineOnlyWhilePending(t *testing.T) {
	h := newHeld(ModelPending)
	h.enter("first")
	h.enter("second")
	h.send(ModelStateMsg{State: ModelReady})
	if strings.Contains(h.m.View(), "waiting for model") {
		t.Fatalf("wait line shown while ready:\n%s", h.m.View())
	}
}

func TestHeldPrompts_WaitLineASCII(t *testing.T) {
	m := NewModel(Options{Now: fixedNow, InitialModelState: ModelPending, Submit: noopSubmit})
	m = typeText(m, "hi")
	m, _ = press(m, tea.KeyEnter)
	v := m.View()
	if !strings.Contains(v, "waiting for model...") || strings.Contains(v, "…") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestHeldPrompts_BusyEscClears(t *testing.T) {
	h := newHeld(ModelPending)
	h.enter("/status")
	h.enter("one")
	h.enter("two")
	h.send(ModelStateMsg{State: ModelReady})
	h.send(tea.KeyMsg{Type: tea.KeyEsc})
	if h.count("cancelled: 2 waiting prompt(s)") != 1 || len(h.m.held) != 0 {
		t.Fatalf("output = %q held = %v", h.output, h.m.held)
	}
	h.send(TurnDoneMsg{Turn: h.m.Turn()})
	h.send(cancelTimeoutMsg{Turn: h.m.Turn()})
	if len(h.calls) != 1 {
		t.Fatalf("auto-started after cancel: %v", h.calls)
	}
}

func TestHeldPrompts_BusyCtrlCClearsAndStillQuits(t *testing.T) {
	h := newHeld(ModelPending)
	h.enter("/status")
	h.enter("one")
	h.send(tea.KeyMsg{Type: tea.KeyCtrlC})
	if h.count("cancelled: 1 waiting prompt(s)") != 1 || h.m.quitting {
		t.Fatalf("output = %q quitting = %v", h.output, h.m.quitting)
	}
	h.send(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !h.m.quitting {
		t.Fatal("second busy ctrl+c did not quit")
	}
}
