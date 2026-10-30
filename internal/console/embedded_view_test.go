package console

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Before the first prompt, the embedded console shows the start screen with
// the input pinned to the bottom of the pane, in exactly the pane height.
func TestEmbeddedStartScreenPinsInput(t *testing.T) {
	m := NewModel(Options{Embedded: true, Start: HeroScreen{}, Version: "0.0.0", Unicode: true})
	m = feed(t, m, tea.WindowSizeMsg{Width: 80, Height: 30})
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 30 {
		t.Fatalf("start frame has %d lines, want 30", len(lines))
	}
	if !strings.Contains(lines[len(lines)-1], "❯") {
		t.Fatalf("input not on the last line: %q", lines[len(lines)-1])
	}
}

// The first prompt dismisses the start screen; from then on the transcript
// is drawn in-frame.
func TestEmbeddedStartScreenGivesWayToTranscript(t *testing.T) {
	m := NewModel(Options{
		Embedded: true, Start: HeroScreen{}, Version: "0.0.0",
		Submit: func(TurnID, string) tea.Cmd { return nil },
	})
	m = feed(t, m, tea.WindowSizeMsg{Width: 80, Height: 20}, SetInputMsg{Text: "hello cluster"}, tea.KeyMsg{Type: tea.KeyEnter})
	if m.startVisible() {
		t.Fatal("start screen still visible after the first prompt")
	}
	view := m.View()
	if !strings.Contains(view, "hello cluster") {
		t.Fatalf("submitted prompt not in the in-frame transcript:\n%s", view)
	}
	if got := len(strings.Split(view, "\n")); got != 20 {
		t.Fatalf("frame has %d lines, want 20", got)
	}
}

func TestEntryBarPerKind(t *testing.T) {
	cases := []struct {
		kind          Kind
		unicode, asci string
	}{
		{KindUser, "┃ ", "| "},
		{KindTool, "│ ", "| "},
		{KindThinking, "┊ ", ": "},
		{KindApproval, "┃ ", "| "},
		{KindError, "┃ ", "| "},
		{KindAgent, "  ", "  "},
		{KindNotice, "  ", "  "},
	}
	for _, tc := range cases {
		if got := entryBar(tc.kind, true).plain; got != tc.unicode {
			t.Errorf("%s unicode bar = %q, want %q", tc.kind, got, tc.unicode)
		}
		if got := entryBar(tc.kind, false).plain; got != tc.asci {
			t.Errorf("%s ascii bar = %q, want %q", tc.kind, got, tc.asci)
		}
	}
}

// Every transcript line carries its bar and fits the pane width.
func TestEmbeddedTranscriptLinesCarryBars(t *testing.T) {
	l := &transcriptLog{}
	l.add(NewUserEntry(time.Time{}, "a question that is long enough to wrap across more than one line of the pane"))
	l.add(NewAgentEntry(time.Time{}, "an answer"))
	lines := l.lines(40, true)
	sawUser := false
	for _, line := range lines {
		if line.Text == "" {
			continue
		}
		if strings.HasPrefix(line.Text, "┃ ") {
			sawUser = true
		}
		if n := len([]rune(line.Text)); n > 40 {
			t.Errorf("line wider than pane (%d): %q", n, line.Text)
		}
	}
	if !sawUser {
		t.Fatalf("user entry lost its bar: %+v", lines)
	}
}

func TestEmbeddedLineScroll(t *testing.T) {
	m := embeddedModel(t, 60, 6)
	for i := 0; i < 20; i++ {
		m = feed(t, m, EntryMsg{Entry: NewNoticeEntry(m.now(), "n")})
	}
	m = feed(t, m, tea.KeyMsg{Type: tea.KeyShiftUp}, tea.KeyMsg{Type: tea.KeyShiftUp})
	if m.log.scroll != 2 {
		t.Fatalf("shift+up twice: scroll = %d, want 2", m.log.scroll)
	}
	m = feed(t, m, tea.KeyMsg{Type: tea.KeyShiftDown}, tea.KeyMsg{Type: tea.KeyShiftDown}, tea.KeyMsg{Type: tea.KeyShiftDown})
	if m.log.scroll != 0 {
		t.Fatalf("scroll below zero: %d", m.log.scroll)
	}
}

// Scroll keys are ordinary keys on the standalone console.
func TestStandaloneIgnoresEmbeddedScroll(t *testing.T) {
	m := NewModel(Options{})
	if m.embeddedScrollKey("pgup") {
		t.Fatal("standalone console consumed pgup")
	}
	if m.TranscriptEntries() != 0 {
		t.Fatal("standalone console reports transcript entries")
	}
	m.scrollBy(3) // must not panic without a log
}

// A frame shorter than the live region shows the live region's tail.
func TestEmbeddedFrameShorterThanLiveRegion(t *testing.T) {
	m := embeddedModel(t, 60, 1)
	m = feed(t, m, EntryMsg{Entry: NewNoticeEntry(m.now(), "x")})
	if got := len(strings.Split(m.View(), "\n")); got != 1 {
		t.Fatalf("1-line pane drew %d lines", got)
	}
}
