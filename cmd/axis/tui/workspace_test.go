package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/toasterbook88/axis/internal/console"
)

// fakeChat records what the workspace forwards to the conversation pane.
type fakeChat struct {
	got           []tea.Msg
	width, height int
	input         string
	commands      []string
}

func (f *fakeChat) Init() tea.Cmd { return nil }
func (f *fakeChat) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	f.got = append(f.got, msg)
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		f.width, f.height = m.Width, m.Height
	case console.SetInputMsg:
		f.input = m.Text
	case console.RunCommandMsg:
		f.commands = append(f.commands, m.Text)
	case tea.KeyMsg:
		f.input += m.String()
	}
	return f, nil
}
func (f *fakeChat) View() string { return "CHAT-PANE" }

func newTestWorkspace(t *testing.T, w, h int) (Workspace, *fakeChat) {
	t.Helper()
	chat := &fakeChat{}
	ws := NewWorkspace(chat)
	ws.dash.now = func() time.Time { return time.Date(2026, 1, 2, 15, 6, 0, 0, time.UTC) }
	next, _ := ws.Update(tea.WindowSizeMsg{Width: w, Height: h})
	next, _ = next.(Workspace).Update(snapshotLoadedMsg{Snapshot: redesignFixture(), Timestamp: "3:04PM", Source: "daemon-cache"})
	return next.(Workspace), chat
}

func wsKeys(t *testing.T, ws Workspace, keys ...tea.KeyMsg) Workspace {
	t.Helper()
	for _, k := range keys {
		next, _ := ws.Update(k)
		ws = next.(Workspace)
	}
	return ws
}

func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

var (
	keyCtrlX = tea.KeyMsg{Type: tea.KeyCtrlX}
	keyCtrlO = tea.KeyMsg{Type: tea.KeyCtrlO}
	keyF2    = tea.KeyMsg{Type: tea.KeyF2}
	keyEsc   = tea.KeyMsg{Type: tea.KeyEsc}
	keyEnter = tea.KeyMsg{Type: tea.KeyEnter}
	keyDown  = tea.KeyMsg{Type: tea.KeyDown}
)

func TestWorkspaceSizesChatBesideSidebar(t *testing.T) {
	_, chat := newTestWorkspace(t, 140, 40)
	if chat.width != 140-sidebarWidth || chat.height != 39 {
		t.Fatalf("chat pane = %dx%d, want %dx39", chat.width, chat.height, 140-sidebarWidth)
	}
	_, narrow := newTestWorkspace(t, 80, 24)
	if narrow.width != 80 {
		t.Fatalf("narrow chat width = %d, want full 80 (sidebar hidden)", narrow.width)
	}
}

func TestWorkspaceRoutesKeysToChat(t *testing.T) {
	ws, chat := newTestWorkspace(t, 140, 40)
	wsKeys(t, ws, runes("h"), runes("i"))
	if chat.input != "hi" {
		t.Fatalf("chat input = %q, want typed keys forwarded", chat.input)
	}
}

func TestWorkspaceDashboardToggleAndAsk(t *testing.T) {
	ws, chat := newTestWorkspace(t, 140, 40)
	ws = wsKeys(t, ws, keyF2)
	if ws.view != viewDashboard {
		t.Fatal("f2 did not open the dashboard")
	}
	// Dashboard keys do not leak into the conversation.
	ws = wsKeys(t, ws, keyDown, runes("j"))
	if chat.input != "" {
		t.Fatalf("dashboard keys leaked to chat: %q", chat.input)
	}
	ws = wsKeys(t, ws, runes("a"))
	if ws.view != viewChat {
		t.Fatal("a did not return to the conversation")
	}
	if chat.input != "@node-c " {
		t.Fatalf("prefilled question = %q, want %q", chat.input, "@node-c ")
	}
	ws = wsKeys(t, ws, keyF2, keyEsc)
	if ws.view != viewChat {
		t.Fatal("esc did not leave the dashboard")
	}
}

func TestWorkspaceLeaderRunsSlashCommands(t *testing.T) {
	ws, chat := newTestWorkspace(t, 140, 40)
	ws = wsKeys(t, ws, keyCtrlX)
	if !ws.leader {
		t.Fatal("ctrl+x did not arm the leader")
	}
	if !strings.Contains(stripANSI(ws.View()), "dashboard") {
		t.Fatal("leader menu not shown")
	}
	ws = wsKeys(t, ws, runes("m"))
	if ws.leader || len(chat.commands) != 1 || chat.commands[0] != "/model" {
		t.Fatalf("ctrl+x m: leader=%v commands=%v", ws.leader, chat.commands)
	}
	ws = wsKeys(t, ws, keyCtrlX, runes("d"))
	if ws.view != viewDashboard {
		t.Fatal("ctrl+x d did not open the dashboard")
	}
	// An unbound second key disarms the leader and does nothing else.
	ws = wsKeys(t, ws, keyCtrlX, runes("z"))
	if ws.leader {
		t.Fatal("unbound leader key left the leader armed")
	}
}

func TestWorkspacePaletteFiltersAndRuns(t *testing.T) {
	ws, chat := newTestWorkspace(t, 140, 40)
	ws = wsKeys(t, ws, keyCtrlO, runes("s"), runes("k"), runes("i"))
	out := stripANSI(ws.View())
	if !strings.Contains(out, "Run a learned skill") || strings.Contains(out, "Toggle sidebar") {
		t.Fatalf("palette filter wrong:\n%s", out)
	}
	ws = wsKeys(t, ws, keyEnter)
	if ws.palette != nil || len(chat.commands) != 1 || chat.commands[0] != "/skills" {
		t.Fatalf("palette enter: open=%v commands=%v", ws.palette != nil, chat.commands)
	}
	ws = wsKeys(t, ws, keyCtrlO, keyEsc)
	if ws.palette != nil {
		t.Fatal("esc did not close the palette")
	}
	if strings.Contains(chat.input, "s") {
		t.Fatalf("palette keys leaked to chat: %q", chat.input)
	}
}

func TestWorkspaceSidebarToggleResizesChat(t *testing.T) {
	ws, chat := newTestWorkspace(t, 140, 40)
	ws = wsKeys(t, ws, keyCtrlX, runes("b"))
	if chat.width != 140 {
		t.Fatalf("chat width after hiding sidebar = %d, want 140", chat.width)
	}
	wsKeys(t, ws, keyCtrlX, runes("b"))
	if chat.width != 140-sidebarWidth {
		t.Fatalf("chat width after showing sidebar = %d", chat.width)
	}
}

func TestWorkspaceSidebarShowsObservedCluster(t *testing.T) {
	ws, _ := newTestWorkspace(t, 140, 40)
	out := stripANSI(ws.View())
	for _, want := range []string{"3/4 reachable", "2m ago", "node-a", "down", "ALERTS 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("sidebar missing %q:\n%s", want, out)
		}
	}
}

func TestWorkspaceForwardsAgentMessagesToChat(t *testing.T) {
	ws, chat := newTestWorkspace(t, 140, 40)
	before := len(chat.got)
	ws.Update(console.StreamChunkMsg{Turn: 1, Text: "hello"})
	if len(chat.got) != before+1 {
		t.Fatal("agent stream message not forwarded to chat")
	}
	// Dashboard data messages stay with the dashboard.
	ws.Update(clockMsg{})
	if len(chat.got) != before+1 {
		t.Fatal("dashboard clock leaked to chat")
	}
}

func TestWorkspaceViewFitsTerminal(t *testing.T) {
	for _, sz := range [][2]int{{200, 60}, {140, 40}, {100, 30}, {80, 24}, {50, 15}} {
		w, h := sz[0], sz[1]
		for _, keys := range [][]tea.KeyMsg{nil, {keyF2}, {keyCtrlO}, {keyCtrlX}, {keyCtrlX, runes("h")}} {
			ws, _ := newTestWorkspace(t, w, h)
			ws = wsKeys(t, ws, keys...)
			lines := strings.Split(ws.View(), "\n")
			if len(lines) != h {
				t.Errorf("%dx%d: %d lines", w, h, len(lines))
			}
			for i, l := range lines {
				if lipgloss.Width(l) != w {
					t.Errorf("%dx%d keys=%v line %d width %d", w, h, keys, i, lipgloss.Width(l))
					break
				}
			}
		}
	}
}

func TestWorkspaceASCII(t *testing.T) {
	ws, _ := newTestWorkspace(t, 140, 40)
	ws.dash.glyphs = asciiGlyphs
	for _, r := range ws.View() {
		if r > 0x7e && r != 0x1b {
			t.Fatalf("ASCII workspace emitted %q", r)
		}
	}
}

// Fast typing arrives as one multi-rune key message; each rune must still
// act as its own key outside the conversation.
func TestWorkspaceSplitsCoalescedKeys(t *testing.T) {
	ws, chat := newTestWorkspace(t, 140, 40)
	ws = wsKeys(t, ws, keyF2, runes("jja"))
	if ws.view != viewChat || chat.input != "@node-c " {
		t.Fatalf("coalesced jja: view=%v input=%q", ws.view, chat.input)
	}
	ws = wsKeys(t, ws, keyCtrlX, runes("bz"))
	if !ws.sidebarHidden || ws.leader {
		t.Fatalf("coalesced leader keys: hidden=%v leader=%v", ws.sidebarHidden, ws.leader)
	}
}

func TestDashboardSplitsCoalescedKeys(t *testing.T) {
	m := press(t, loadedModel(t, 120, 40), "jjG")
	if m.cursor != 3 {
		t.Fatalf("coalesced jjG: cursor = %d, want 3", m.cursor)
	}
}
