package console

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/ui"
)

// logoPresence counts how many of the logo's rows appear in the frame.
func logoPresence(frame string) (found, total int) {
	logo := ui.LogoLines(ui.ColorNone)
	plain := ui.StripANSIAndControls(frame)
	for _, row := range logo {
		r := strings.TrimSpace(row)
		if r != "" && strings.Contains(plain, r) {
			found++
		}
	}
	return found, len(logo)
}

func heightModel(t *testing.T, w, h int) Model {
	t.Helper()
	m := newStartModel(Options{Unicode: true, Footer: staticFooter{text: "fleet 5/5 ok"}})
	m, _ = sendMsg(m, tea.WindowSizeMsg{Width: w, Height: h})
	for _, l := range []StatusLine{
		{Key: "fleet", State: StatusDone, Text: "fleet 5/5 nodes"},
		{Key: "mcp", State: StatusDone, Text: "2 MCP servers"},
		{Key: "model", State: StatusRunning, Text: "finding a model…"},
	} {
		m, _ = sendMsg(m, StartupStatusMsg{Line: l})
	}
	if !m.startVisible() {
		t.Fatal("splash not visible")
	}
	return m
}

// The splash shares the window with the rows View draws below it (the prompt
// rule, the input, the footer). It must fit in what is left, so the terminal
// never scrolls the top of the logo away.
func TestStartScreen_FitsRowsLeftAbovePrompt(t *testing.T) {
	for h := 10; h <= 40; h++ {
		frame := heightModel(t, 120, h).View()
		if n := len(strings.Split(frame, "\n")); n > h {
			t.Errorf("h=%d: frame has %d rows", h, n)
		}
		if found, total := logoPresence(frame); found != 0 && found != total {
			t.Errorf("h=%d: logo clipped, %d of %d rows shown", h, found, total)
		}
	}
}

// Unknown height (0) still means tall: the full splash with the whole logo.
func TestStartScreen_UnknownHeightRendersFull(t *testing.T) {
	m := heightModel(t, 120, 0)
	if found, total := logoPresence(m.View()); found != total {
		t.Fatalf("height 0: %d of %d logo rows, want all", found, total)
	}
}
