package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestWorkspaceInitStartsChatAndSnapshot(t *testing.T) {
	ws := NewWorkspace(&fakeChat{})
	if ws.Init() == nil {
		t.Fatal("Init returned no command (snapshot load and title never start)")
	}
	if !ws.dash.hosted {
		t.Fatal("dashboard inside the workspace must be hosted")
	}
}

// ctrl+x p opens the placement wizard over the conversation; it owns the
// keyboard until closed, and esc returns to the conversation.
func TestWorkspaceLeaderOpensPlacement(t *testing.T) {
	ws, chat := newTestWorkspace(t, 120, 30)
	ws = wsKeys(t, ws, keyCtrlX, runes("p"))
	if !ws.dash.modalActive {
		t.Fatal("ctrl+x p did not open placement")
	}
	if out := stripANSI(ws.View()); !strings.Contains(out, "Workload Placement Wizard") {
		t.Fatalf("placement not drawn:\n%s", out)
	}
	ws = wsKeys(t, ws, runes("x"))
	if chat.input != "" {
		t.Fatalf("keys leaked to chat while placement open: %q", chat.input)
	}
	ws = wsKeys(t, ws, keyEsc)
	if ws.dash.modalActive {
		t.Fatal("esc did not close placement")
	}
}

func TestWorkspaceLeaderRefreshStartsLoad(t *testing.T) {
	ws, _ := newTestWorkspace(t, 120, 30)
	next, _ := ws.Update(keyCtrlX)
	next, cmd := next.(Workspace).Update(runes("r"))
	if !next.(Workspace).dash.loading || cmd == nil {
		t.Fatal("ctrl+x r did not start a snapshot load")
	}
}

func TestWorkspaceLeaderQuits(t *testing.T) {
	ws, _ := newTestWorkspace(t, 120, 30)
	next, _ := ws.Update(keyCtrlX)
	_, cmd := next.(Workspace).Update(runes("q"))
	if cmd == nil {
		t.Fatal("ctrl+x q returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+x q did not quit")
	}
}

func TestPaletteNavigation(t *testing.T) {
	ws, chat := newTestWorkspace(t, 120, 30)
	ws = wsKeys(t, ws, keyCtrlO)
	// Up at the top stays at the top; down moves; tab is down too.
	ws = wsKeys(t, ws, tea.KeyMsg{Type: tea.KeyUp})
	if ws.palette.cursor != 0 {
		t.Fatalf("up at top: cursor = %d", ws.palette.cursor)
	}
	ws = wsKeys(t, ws, keyDown, tea.KeyMsg{Type: tea.KeyTab})
	if ws.palette.cursor != 2 {
		t.Fatalf("down+tab: cursor = %d, want 2", ws.palette.cursor)
	}
	// Down at the bottom stays at the bottom.
	for range workspaceActions {
		ws = wsKeys(t, ws, keyDown)
	}
	if ws.palette.cursor != len(workspaceActions)-1 {
		t.Fatalf("down past end: cursor = %d", ws.palette.cursor)
	}
	// Typing resets the cursor; backspace edits the query; space is text.
	ws = wsKeys(t, ws, runes("clear"), tea.KeyMsg{Type: tea.KeyBackspace}, tea.KeyMsg{Type: tea.KeySpace})
	if ws.palette.query != "clea " || ws.palette.cursor != 0 {
		t.Fatalf("query = %q cursor = %d", ws.palette.query, ws.palette.cursor)
	}
	// No match: enter closes and runs nothing.
	ws = wsKeys(t, ws, runes("zzz"))
	if out := stripANSI(ws.View()); !strings.Contains(out, "no matching command") {
		t.Fatalf("empty palette state missing:\n%s", out)
	}
	ws = wsKeys(t, ws, keyEnter)
	if ws.palette != nil || len(chat.commands) != 0 {
		t.Fatalf("enter on no match: open=%v commands=%v", ws.palette != nil, chat.commands)
	}
	// Backspace on an empty query is harmless.
	ws = wsKeys(t, ws, keyCtrlO, tea.KeyMsg{Type: tea.KeyBackspace})
	if ws.palette == nil || ws.palette.query != "" {
		t.Fatal("backspace on empty query misbehaved")
	}
	// ctrl+o toggles it closed.
	if ws = wsKeys(t, ws, keyCtrlO); ws.palette != nil {
		t.Fatal("ctrl+o did not close the palette")
	}
}

func TestPaletteCtrlCQuits(t *testing.T) {
	ws, _ := newTestWorkspace(t, 120, 30)
	ws = wsKeys(t, ws, keyCtrlO)
	_, cmd := ws.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c in palette returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c in palette did not quit")
	}
}

func TestWorkspaceHelpClosesOnAnyKey(t *testing.T) {
	ws, chat := newTestWorkspace(t, 120, 40)
	ws = wsKeys(t, ws, keyCtrlX, runes("h"))
	if !ws.showHelp {
		t.Fatal("ctrl+x h did not open help")
	}
	ws = wsKeys(t, ws, runes("k"))
	if ws.showHelp || chat.input != "" {
		t.Fatalf("closing key: help=%v leaked=%q", ws.showHelp, chat.input)
	}
	ws = wsKeys(t, ws, keyCtrlX, runes("h"))
	_, cmd := ws.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c in help returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c in help did not quit")
	}
}

func TestWorkspaceDashboardQReturnsToChat(t *testing.T) {
	ws, _ := newTestWorkspace(t, 120, 30)
	ws = wsKeys(t, ws, keyF2, runes("q"))
	if ws.view != viewChat {
		t.Fatal("q in the hosted dashboard must return to chat, not quit")
	}
	// The dashboard's own help still takes esc first.
	ws = wsKeys(t, ws, keyF2, runes("?"), keyEsc)
	if ws.view != viewDashboard || ws.dash.showHelp {
		t.Fatalf("esc in dashboard help: view=%v help=%v", ws.view, ws.dash.showHelp)
	}
}

func TestHostedDashboardFooterHasNoQuit(t *testing.T) {
	ws, _ := newTestWorkspace(t, 120, 30)
	if footer := stripANSI(renderFooter(ws.dash, 120)); strings.Contains(footer, "quit") {
		t.Fatalf("hosted footer offers quit: %q", footer)
	}
}

func TestAskAboutSelectedWithoutSnapshot(t *testing.T) {
	chat := &fakeChat{}
	ws := NewWorkspace(chat)
	next, _ := ws.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	ws = wsKeys(t, next.(Workspace), keyF2, runes("a"))
	if ws.view != viewChat || chat.input != "" {
		t.Fatalf("ask with no snapshot: view=%v input=%q", ws.view, chat.input)
	}
}

func TestSidebarWithoutSnapshot(t *testing.T) {
	d := NewModel()
	if out := joinPlain(renderSidebar(d, sidebarWidth, 20)); !strings.Contains(out, "loading snapshot") {
		t.Fatalf("loading sidebar:\n%s", out)
	}
	d.loading = false
	out := joinPlain(renderSidebar(d, sidebarWidth, 20))
	if !strings.Contains(out, "no snapshot") || !strings.Contains(out, "ctrl+x d") {
		t.Fatalf("no-snapshot sidebar:\n%s", out)
	}
	// Too short for the key legend: it is dropped, not overflowed.
	if lines := renderSidebar(d, sidebarWidth, 4); len(lines) != 4 {
		t.Fatalf("short sidebar = %d lines", len(lines))
	}
}

func TestHealthGlyphAndSourceLabel(t *testing.T) {
	g := unicodeGlyphs
	if healthGlyph(levelOK, g) != g.ok || healthGlyph(levelWarn, g) != g.warn || healthGlyph(levelBad, g) != g.bad {
		t.Fatal("healthGlyph mapping wrong")
	}
	if sourceLabel("") != "unknown" || sourceLabel("live") != "live" {
		t.Fatal("sourceLabel wrong")
	}
}

func TestWorkspaceZeroSizeRendersNothing(t *testing.T) {
	if got := NewWorkspace(&fakeChat{}).View(); got != "" {
		t.Fatalf("pre-size view = %q, want empty", got)
	}
}

func TestOverlayClipsTallBox(t *testing.T) {
	base := []string{"a", "b", "c"}
	out := overlay(base, "1\n2\n3\n4\n5", 10)
	if len(out) != 3 {
		t.Fatalf("overlay grew the frame to %d lines", len(out))
	}
}

func TestLoadingScreen(t *testing.T) {
	m := NewModel()
	m.glyphs = unicodeGlyphs
	if out := stripANSI(renderLoading(m, 120, 20)); !strings.Contains(out, "███") || !strings.Contains(out, "Loading cluster snapshot") {
		t.Fatalf("wide loading screen missing logo:\n%s", out)
	}
	// Too narrow or too short for the logo: the message alone.
	for _, sz := range [][2]int{{20, 20}, {120, 5}} {
		out := stripANSI(renderLoading(m, sz[0], sz[1]))
		if strings.Contains(out, "███") || !strings.Contains(out, "Loading") {
			t.Fatalf("%dx%d loading = %q", sz[0], sz[1], out)
		}
	}
}

func TestAgeLevelThresholds(t *testing.T) {
	now := loadedModel(t, 80, 24).now()
	cases := map[int]level{0: levelOK, 4: levelOK, 6: levelWarn, 29: levelWarn, 31: levelBad}
	for minutes, want := range cases {
		if got := ageLevel(now.Add(-time.Duration(minutes)*time.Minute), now); got != want {
			t.Errorf("age %dm: level %v, want %v", minutes, got, want)
		}
	}
	if ageLevel(time.Time{}, now) != levelWarn {
		t.Error("unknown age must warn")
	}
}
