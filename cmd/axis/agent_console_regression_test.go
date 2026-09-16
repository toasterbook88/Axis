package main

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/console"
)

var testClock = func() time.Time { return time.Date(2026, 8, 25, 21, 35, 0, 0, time.UTC) }

// The console approval gate must fail closed (ConfirmNo) when the operator
// does not respond before the timeout. It must never read stdin synchronously,
// which would corrupt Bubble Tea's raw-mode input loop.
func TestConsoleApprovalFailsClosedWithoutReadingStdin(t *testing.T) {
	rec := &messageRecorder{}
	confirm := consoleConfirmWithTimeout(context.Background(), rec.Send, testClock, 10*time.Millisecond)

	for _, score := range []int{0, 35, 74, 95} {
		if got := confirm("bash", "rm -rf /tmp/x", score); got != agent.ConfirmNo {
			t.Errorf("safety %d: confirm returned %v, want ConfirmNo", score, got)
		}
	}

	var entries []console.EntryMsg
	for _, m := range rec.all() {
		if e, ok := m.(console.EntryMsg); ok {
			entries = append(entries, e)
		}
	}
	if len(entries) != 4 {
		t.Fatalf("got %d approval notices, want 4", len(entries))
	}
	for _, e := range entries {
		rendered := strings.Join(console.PlainAll(e.Entry.Render(100)), " ")
		if !strings.Contains(rendered, "denied") {
			t.Errorf("denial was silent: %q", rendered)
		}
	}
}

// The console confirmation must never auto-approve; even with safety score 0
// or low-risk commands it must return ConfirmNo unless the operator explicitly
// presses y/a.
func TestConsoleApprovalNeverAutoApproves(t *testing.T) {
	confirm := consoleConfirm(context.Background(), nil, testClock)
	for _, r := range []agent.ConfirmResult{agent.ConfirmYes, agent.ConfirmAlways} {
		if confirm("bash", "anything", 0) == r {
			t.Fatalf("console confirm returned %v; it must always deny", r)
		}
	}
}

type messageRecorder struct {
	msgs []tea.Msg
}

func (r *messageRecorder) Send(m tea.Msg) {
	r.msgs = append(r.msgs, m)
}

func (r *messageRecorder) all() []tea.Msg {
	return append([]tea.Msg(nil), r.msgs...)
}
