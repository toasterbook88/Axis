package console

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/agent"
)

func TestToolCellCollapsesLongOutput(t *testing.T) {
	e := NewToolEntry(time.Unix(0, 0), "1", "axis_status", "cluster")
	e.Elapsed = 4 * time.Millisecond
	e.Badge = "cached"
	var lines []string
	for i := 0; i < 12; i++ {
		lines = append(lines, "row")
	}
	e.Result = strings.Join(lines, "\n")
	got := strings.Join(plain(e, 80), "\n")
	if !strings.Contains(got, "4ms") || !strings.Contains(got, "cached") {
		t.Fatalf("missing elapsed or badge:\n%s", got)
	}
	if !strings.Contains(got, "more lines") {
		t.Fatalf("long output was not collapsed:\n%s", got)
	}
	e.Expanded = true
	full := strings.Join(plain(e, 80), "\n")
	if strings.Contains(full, "more lines") {
		t.Fatalf("expanded cell still collapsed:\n%s", full)
	}
}

func TestPlacementCellIsAdvisory(t *testing.T) {
	e := NewToolEntry(time.Unix(0, 0), "1", "axis_place", "9b")
	e.Result = "node cachyos fit 80"
	got := strings.Join(plain(e, 80), "\n")
	if !strings.Contains(got, "advisory — not reserved") {
		t.Fatalf("placement cell missing advisory line:\n%s", got)
	}
}

func TestFooterOmitsFleetFraction(t *testing.T) {
	text := NewStatusFooter(StatusFooterConfig{
		Model: func() string { return "qwen" },
		Mode:  func() string { return "default" },
	}).Render(100)[0].Text
	if strings.Contains(text, "fleet:") || strings.Contains(text, "10/10") {
		t.Fatalf("footer leaked a fleet fraction: %s", text)
	}
	if !strings.Contains(text, "observe") || !strings.Contains(text, "no snapshot") {
		t.Fatalf("footer chrome: %s", text)
	}
}

func TestSpinnerFrozenWhileOverlayOpen(t *testing.T) {
	m := NewModel(Options{})
	m.state = turnRunning
	m.pendingTools = []pendingTool{{name: "run_shell"}}
	m.overlay = NewApprovalOverlay("run_shell", "echo hi", 22, nil)
	view := m.View()
	if strings.Contains(view, "working") {
		t.Fatalf("spinner kept running under the overlay:\n%s", view)
	}
	if !strings.Contains(view, "run_shell") || !strings.Contains(view, "safety 22") {
		t.Fatalf("overlay content missing:\n%s", view)
	}
}

func TestQuestionMarkCommitsKeymap(t *testing.T) {
	m := NewModel(Options{Now: func() time.Time { return time.Unix(0, 0) }})
	m.editor.SetText("?")
	updated, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	model := updated.(Model)
	if model.editor.Text() != "" {
		t.Fatalf("composer not cleared: %q", model.editor.Text())
	}
	if cmd == nil {
		t.Fatal("expected a commit command")
	}
	if !strings.Contains(KeymapText(), "esc stop turn") || !strings.Contains(SlashPaletteText(), "/plan") {
		t.Fatal("keymap or palette missing wired commands")
	}
}

func TestEnterAndLastExpandCollapsedTool(t *testing.T) {
	m := NewModel(Options{Now: func() time.Time { return time.Unix(0, 0) }})
	var lines []string
	for i := 0; i < 12; i++ {
		lines = append(lines, "row")
	}
	e := NewToolEntry(time.Unix(0, 0), "1", "axis_status", "cluster")
	e.Result = strings.Join(lines, "\n")
	updated, _ := m.Update(EntryMsg{Entry: e})
	m = updated.(Model)
	if m.lastTool == nil || m.lastTool.Expanded {
		t.Fatal("last tool was not stored collapsed")
	}
	updated, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	pager, ok := m.overlay.(*PagerOverlay)
	if !ok {
		t.Fatalf("Enter did not open a pager, overlay=%T", m.overlay)
	}
	body := strings.Join(plainLines(pager.Render(80)), "\n")
	if strings.Contains(body, "more lines") || !strings.Contains(body, "row") {
		t.Fatalf("pager did not show the tool body:\n%s", body)
	}
	if pager.lines[0] != "row" || pager.lines[len(pager.lines)-1] != "row" || len(pager.lines) != 12 {
		t.Fatalf("pager lines = %d %q", len(pager.lines), pager.lines)
	}
	m.overlay = nil
	m.lastTool.Expanded = false
	m.editor.SetText("/last")
	updated, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if _, ok := m.overlay.(*PagerOverlay); !ok {
		t.Fatalf("/last did not open a pager, overlay=%T", m.overlay)
	}
}

func TestApprovalCountdownFailsClosed(t *testing.T) {
	reply := make(chan agent.ConfirmResult, 1)
	overlay := NewApprovalOverlay("run_shell", "echo hi", 22, reply)
	start := time.Unix(1_700_000_000, 0)
	cur := start
	overlay.now = func() time.Time { return cur }
	overlay.deadline = start.Add(approvalFailClosed)

	view := strings.Join(plainLines(overlay.Render(80)), "\n")
	if !strings.Contains(view, "timeout 10m0s fail-closed") {
		t.Fatalf("fresh countdown:\n%s", view)
	}
	cur = cur.Add(time.Second)
	view = strings.Join(plainLines(overlay.Render(80)), "\n")
	if !strings.Contains(view, "timeout 9m59s fail-closed") {
		t.Fatalf("countdown did not decrease:\n%s", view)
	}
	cur = start.Add(approvalFailClosed)
	updated, _ := overlay.Update(approvalTickMsg{deadline: overlay.deadline})
	if updated != nil {
		t.Fatal("expired box stayed open")
	}
	select {
	case res := <-reply:
		if res != agent.ConfirmNo {
			t.Fatalf("timeout result = %v, want deny", res)
		}
	default:
		t.Fatal("timeout did not reply")
	}
}

func plainLines(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Plain()
	}
	return out
}

func TestApprovalCellKeepsOutcome(t *testing.T) {
	e := NewApprovalEntry(time.Unix(0, 0), "run_shell", "", 22, "write in workspace", DecisionOnce)
	e.Elapsed = 1200 * time.Millisecond
	got := strings.Join(plain(e, 80), "\n")
	if !strings.Contains(got, "safety 22/100") || !strings.Contains(got, "allowed 1.2s") {
		t.Fatalf("approval cell:\n%s", got)
	}
}
