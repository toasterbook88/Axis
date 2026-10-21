package console

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// dismissed returns a start-path model whose splash is already gone.
func dismissed(t *testing.T, opts Options) Model {
	t.Helper()
	m := newStartModel(opts)
	m = typeText(m, "/usage")
	m, _ = press(m, tea.KeyEnter)
	if m.startVisible() {
		t.Fatal("splash still visible after submit")
	}
	return m
}

// A failure that lands after the splash is gone must still reach the
// operator; only progress and info lines may be dropped.
func TestApplyStatus_FailedAfterDismissalIsCommitted(t *testing.T) {
	m := dismissed(t, Options{Unicode: true})
	m, cmd := sendMsg(m, StartupStatusMsg{Line: StatusLine{Key: "mcp", State: StatusFailed, Text: "MCP: 1 of 2 failed (srv-b)"}})
	if n := countContaining(printed(cmd), "MCP: 1 of 2 failed (srv-b)"); n != 1 {
		t.Fatalf("failed status after dismissal printed %d times, want 1", n)
	}
	_, cmd = sendMsg(m, StartupStatusMsg{Line: StatusLine{Key: "mcp", State: StatusDone, Text: "2 MCP servers"}})
	if n := countContaining(printed(cmd), "2 MCP servers"); n != 0 {
		t.Fatalf("non-persist progress line printed after dismissal")
	}
}

// While the model is still being found the header must not claim there is
// no model.
func TestStartScreen_HeaderPendingSaysFinding(t *testing.T) {
	for _, c := range []struct {
		unicode bool
		want    string
	}{
		{true, "▲ AXIS agent · v0.19.6 · finding a model…"},
		{false, "^ AXIS agent - v0.19.6 - finding a model..."},
	} {
		for _, name := range []string{"", "example-model"} {
			m := newStartModel(Options{Unicode: c.unicode, InitialModelState: ModelPending, HeaderModel: func() string { return name }})
			m = typeText(m, "/usage")
			_, cmd := press(m, tea.KeyEnter)
			if countContaining(printed(cmd), c.want) != 1 {
				t.Errorf("unicode=%v name=%q: header %q missing in %q", c.unicode, name, c.want, printed(cmd))
			}
		}
	}
	m := newStartModel(Options{Unicode: true, InitialModelState: ModelNone, HeaderModel: func() string { return "example-model" }})
	m = typeText(m, "/usage")
	_, cmd := press(m, tea.KeyEnter)
	if countContaining(printed(cmd), "· no model") != 1 {
		t.Errorf("ModelNone header must read no model: %q", printed(cmd))
	}
}

// The waiting line obeys the width-1 rule like every other rendered line.
func TestHeldLines_FitWidth(t *testing.T) {
	for _, unicode := range []bool{true, false} {
		for w := 1; w <= 25; w++ {
			h := newHeld(ModelPending)
			h.m.unicode = unicode
			h.send(tea.WindowSizeMsg{Width: w, Height: 40})
			h.enter("first")
			if !h.m.waitingForModel() {
				t.Fatal("prompt not held")
			}
			for _, l := range h.m.heldLines() {
				if got := visibleWidth(l.Text); got > w-1 {
					t.Errorf("unicode=%v width %d: waiting line %q is %d columns", unicode, w, l.Text, got)
				}
			}
		}
	}
}

// The footer's pending frame keeps turning after the splash is dismissed.
func TestSpinner_FooterPendingAnimatesAfterDismissal(t *testing.T) {
	m := dismissed(t, Options{Unicode: true, InitialModelState: ModelPending, Footer: stubFooter{}})
	if !m.animating() {
		t.Fatal("pending model with a footer must keep the spinner animating")
	}
	m = dismissed(t, Options{Unicode: true, InitialModelState: ModelReady, Footer: stubFooter{}})
	if m.animating() {
		t.Fatal("a ready, idle console must not animate")
	}
}

// Cancelling held prompts also disarms a pending esc-esc gesture.
func TestCancelHeld_ResetsEscPending(t *testing.T) {
	h := newHeld(ModelPending)
	h.enter("first")
	h.m.escPending = true
	next, _, ok := h.m.cancelHeld()
	if !ok || next.escPending {
		t.Fatalf("cancelHeld ok=%v escPending=%v, want true/false", ok, next.escPending)
	}
}

type stubFooter struct{}

func (stubFooter) Render(width int, _ FooterState) []Line {
	return []Line{{Text: strings.Repeat("f", max(width-1, 0))}}
}
