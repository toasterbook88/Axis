package console

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func embeddedModel(t *testing.T, w, h int) Model {
	t.Helper()
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	m := NewModel(Options{Embedded: true, Now: func() time.Time { return fixed }})
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return next.(Model)
}

func feed(t *testing.T, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

// Embedded, a committed entry lands in the in-frame transcript and produces
// no scrollback print command.
func TestEmbeddedCommitKeepsEntryInFrame(t *testing.T) {
	m := embeddedModel(t, 60, 12)
	next, cmd := m.Update(EntryMsg{Entry: NewNoticeEntry(m.now(), "hello from the agent")})
	m = next.(Model)
	if cmd != nil {
		t.Fatal("embedded commit returned a command (would print to scrollback)")
	}
	if m.TranscriptEntries() != 1 {
		t.Fatalf("transcript entries = %d, want 1", m.TranscriptEntries())
	}
	if !strings.Contains(m.View(), "hello from the agent") {
		t.Fatalf("committed entry not drawn in frame:\n%s", m.View())
	}
}

func TestStandaloneCommitStillPrints(t *testing.T) {
	m := NewModel(Options{})
	if m.Embedded() {
		t.Fatal("standalone console reports embedded")
	}
	_, cmd := m.Update(EntryMsg{Entry: NewNoticeEntry(time.Now(), "x")})
	if cmd == nil {
		t.Fatal("standalone commit must print to scrollback")
	}
}

func TestEmbeddedFrameIsExactlyPaneHeight(t *testing.T) {
	m := embeddedModel(t, 60, 12)
	for _, n := range []int{0, 1, 5, 40} {
		mm := m
		mm.log = &transcriptLog{}
		for i := 0; i < n; i++ {
			mm = feed(t, mm, EntryMsg{Entry: NewNoticeEntry(mm.now(), "line")})
		}
		if got := len(strings.Split(mm.View(), "\n")); got != 12 {
			t.Errorf("%d entries: frame has %d lines, want 12", n, got)
		}
	}
}

// pgup lifts the view to older entries; pgdown returns to the newest.
func TestEmbeddedScroll(t *testing.T) {
	m := embeddedModel(t, 60, 8)
	for i := 0; i < 30; i++ {
		m = feed(t, m, EntryMsg{Entry: NewNoticeEntry(m.now(), "entry-"+string(rune('A'+i%26))+string(rune('a'+i/26)))})
	}
	if !strings.Contains(m.View(), "entry-Db") {
		t.Fatalf("newest entry not visible:\n%s", m.View())
	}
	m = feed(t, m, tea.KeyMsg{Type: tea.KeyPgUp}, tea.KeyMsg{Type: tea.KeyPgUp}, tea.KeyMsg{Type: tea.KeyPgUp})
	view := m.View()
	if strings.Contains(view, "entry-Db") || !strings.Contains(view, "more line(s) below") {
		t.Fatalf("pgup did not scroll back:\n%s", view)
	}
	for i := 0; i < 50; i++ {
		m = feed(t, m, tea.KeyMsg{Type: tea.KeyPgUp})
	}
	if !strings.Contains(m.View(), "entry-Aa") {
		t.Fatalf("scrolling to the top did not reach the first entry:\n%s", m.View())
	}
	for i := 0; i < 60; i++ {
		m = feed(t, m, tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if m.log.scroll != 0 || !strings.Contains(m.View(), "entry-Db") {
		t.Fatal("pgdown did not return to the newest entry")
	}
}

func TestEmbeddedTranscriptIsBounded(t *testing.T) {
	l := &transcriptLog{}
	for i := 0; i < maxEmbeddedEntries+10; i++ {
		l.add(NewNoticeEntry(time.Time{}, "x"))
	}
	if len(l.entries) != maxEmbeddedEntries {
		t.Fatalf("entries = %d, want cap %d", len(l.entries), maxEmbeddedEntries)
	}
}

func TestSetInputMsgPrefillsWithoutSubmitting(t *testing.T) {
	var submitted []string
	m := NewModel(Options{Embedded: true, Submit: func(_ TurnID, p string) tea.Cmd {
		submitted = append(submitted, p)
		return nil
	}})
	m = feed(t, m, SetInputMsg{Text: "@node-a "})
	if m.Input() != "@node-a " {
		t.Fatalf("input = %q", m.Input())
	}
	if len(submitted) != 0 {
		t.Fatal("SetInputMsg submitted the prompt")
	}
	// Typing continues after the prefill.
	m = feed(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("why")})
	if got := m.Input(); got != "@node-a why" {
		t.Fatalf("input after typing = %q", got)
	}
}
