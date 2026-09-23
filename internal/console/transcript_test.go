package console

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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

func TestApprovalCellKeepsOutcome(t *testing.T) {
	e := NewApprovalEntry(time.Unix(0, 0), "run_shell", "", 22, "write in workspace", DecisionOnce)
	e.Elapsed = 1200 * time.Millisecond
	got := strings.Join(plain(e, 80), "\n")
	if !strings.Contains(got, "safety 22/100") || !strings.Contains(got, "allowed 1.2s") {
		t.Fatalf("approval cell:\n%s", got)
	}
}
