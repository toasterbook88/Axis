package console

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestLiveTurnShowsToolInsteadOfWorking(t *testing.T) {
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }})
	m = startTurnFor(t, m)
	m = apply(m, ToolPendingMsg{Turn: 1, ID: "c1", Name: "axis_facts", Args: "node=\"lab-1\"\nsecond"})

	view := m.View()
	if strings.Contains(view, "working") {
		t.Fatalf("pending tool also says working:\n%s", view)
	}
	if !strings.Contains(view, spinnerFrame(0, false)+" axis_facts") {
		t.Fatalf("tool row missing:\n%s", view)
	}
	if !strings.Contains(view, "node=\"lab-1\" second") {
		t.Fatalf("tool args did not collapse onto one row:\n%s", view)
	}
	lines := m.liveActivity()
	if len(lines) != 1 {
		t.Fatalf("newline in args injected rows: %d", len(lines))
	}
}

func TestLiveTurnSaysWorkingOnlyWhenNothingElseDoes(t *testing.T) {
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }})
	m = startTurnFor(t, m)
	view := m.View()
	if !strings.Contains(view, spinnerFrame(0, false)+" working") {
		t.Fatalf("bare turn missing working:\n%s", view)
	}

	m = apply(m, StreamChunkMsg{Turn: 1, Text: "hello there"})
	view = m.View()
	if strings.Contains(view, "working") || !strings.Contains(view, "hello there") {
		t.Fatalf("streamed answer should replace working:\n%s", view)
	}
}

func TestLiveTurnNamesThinking(t *testing.T) {
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }})
	m = startTurnFor(t, m)
	m = apply(m, StreamChunkMsg{Turn: 1, Text: "<think>secret plan"})
	view := m.View()
	if strings.Contains(view, "secret") || strings.Contains(view, "working") || !strings.Contains(view, "thinking") {
		t.Fatalf("open thought:\n%s", view)
	}

	closed := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }})
	closed = startTurnFor(t, closed)
	closed = apply(closed, StreamChunkMsg{Turn: 1, Text: "<think>secret plan</think>"})
	view = closed.View()
	if strings.Contains(view, "secret") || strings.Contains(view, "working") || !strings.Contains(view, "thinking") {
		t.Fatalf("closed thought with no answer yet:\n%s", view)
	}
}

func TestLiveTurnCancellingIsThePhase(t *testing.T) {
	m := NewModel(Options{Submit: func(TurnID, string) tea.Cmd { return nil }})
	m = startTurnFor(t, m)
	m = apply(m, ToolPendingMsg{Turn: 1, ID: "c1", Name: "axis_facts"}, tea.KeyMsg{Type: tea.KeyEsc})
	view := m.View()
	if !strings.Contains(view, "cancelling") || strings.Contains(view, "working") {
		t.Fatalf("cancel phase:\n%s", view)
	}
	if !strings.Contains(view, "axis_facts") {
		t.Fatalf("in-flight tool hidden while cancelling:\n%s", view)
	}
}

func TestPendingToolRowStopsBeforeTheEdge(t *testing.T) {
	m := NewModel(Options{})
	m.width = 24
	m.state = turnRunning
	m.pendingTools = []pendingTool{{name: "axis_facts", args: "日本語日本語日本語"}}

	lines := m.liveActivity()
	if len(lines) != 1 {
		t.Fatalf("want 1 row, got %d", len(lines))
	}
	budget := m.ephemeralBudget()
	if got := visibleWidth(lines[0].Text); got > budget {
		t.Fatalf("row width %d > budget %d: %q", got, budget, lines[0].Text)
	}
	if !strings.Contains(lines[0].Text, spinnerFrame(0, false)+" axis_facts") {
		t.Fatalf("name clipped away: %q", lines[0].Text)
	}
	if lines[0].Style != StyleAccent {
		t.Fatalf("in-flight tool style = %v, want accent", lines[0].Style)
	}
}
