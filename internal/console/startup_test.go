package console

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/ui"
)

var ansiSeq = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiSeq.ReplaceAllString(s, "") }

func newStartModel(opts Options) Model {
	opts.Now = fixedNow
	opts.CancelGrace = time.Millisecond
	if opts.Submit == nil {
		opts.Submit = noopSubmit
	}
	if opts.Start == nil {
		opts.Start = HeroScreen{}
	}
	if opts.Version == "" {
		opts.Version = "0.19.6"
	}
	if opts.HeaderModel == nil {
		opts.HeaderModel = func() string { return "example-model" }
	}
	return NewModel(opts)
}

func sendMsg(m Model, msg tea.Msg) (Model, tea.Cmd) {
	updated, cmd := m.Update(msg)
	return updated.(Model), cmd
}

func countContaining(lines []string, sub string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

func TestStartScreen_VisibleUntilFirstSubmit(t *testing.T) {
	m := newStartModel(Options{Unicode: true})
	if !m.startVisible() || !strings.Contains(stripANSI(m.View()), "agent · v0.19.6") {
		t.Fatalf("start screen not visible:\n%s", m.View())
	}
	m = typeText(m, "/usage")
	m, cmd := press(m, tea.KeyEnter)
	if m.startVisible() || strings.Contains(stripANSI(m.View()), "agent · v0.19.6") {
		t.Fatalf("start screen still visible after submit:\n%s", m.View())
	}
	out := printed(cmd)
	if n := countContaining(out, "▲ AXIS agent · v0.19.6 · example-model"); n != 1 {
		t.Fatalf("want exactly one header line, got %d in %q", n, out)
	}
}

func TestStartScreen_HeaderOrderBeforeUserEntry(t *testing.T) {
	m := newStartModel(Options{Unicode: true})
	m = typeText(m, "hello")
	_, cmd := press(m, tea.KeyEnter)
	out := printed(cmd)
	hi, ui := -1, -1
	for i, l := range out {
		if strings.Contains(l, "▲ AXIS agent") {
			hi = i
		}
		if strings.Contains(l, "hello") {
			ui = i
		}
	}
	if hi < 0 || ui < 0 || hi > ui {
		t.Fatalf("header must print before the user line: %q", out)
	}
}

func TestStartScreen_AsciiHeader(t *testing.T) {
	m := newStartModel(Options{Unicode: false})
	m = typeText(m, "/usage")
	_, cmd := press(m, tea.KeyEnter)
	out := printed(cmd)
	if countContaining(out, "^ AXIS agent - v0.19.6 - example-model") != 1 {
		t.Fatalf("ascii header wrong: %q", out)
	}
}

func TestStartScreen_HeaderNoModel(t *testing.T) {
	m := newStartModel(Options{Unicode: true, HeaderModel: func() string { return "" }})
	m = typeText(m, "/usage")
	_, cmd := press(m, tea.KeyEnter)
	if countContaining(printed(cmd), "▲ AXIS agent · v0.19.6 · no model") != 1 {
		t.Fatal("empty model must read no model")
	}
}

func TestStartScreen_PersistLinesFlushedOnce(t *testing.T) {
	m := newStartModel(Options{Unicode: true, InitialStatus: []StatusLine{
		{Key: "advisory", State: StatusInfo, Text: "advisory: agent output is not cluster truth", Persist: true},
		{Key: "cwd", State: StatusInfo, Text: "cwd: /home/user"},
	}})
	var all []string
	for i := 0; i < 2; i++ {
		m = typeText(m, "/usage")
		var cmd tea.Cmd
		m, cmd = press(m, tea.KeyEnter)
		all = append(all, printed(cmd)...)
	}
	if n := countContaining(all, "▲ AXIS agent"); n != 1 {
		t.Errorf("header printed %d times", n)
	}
	if n := countContaining(all, "advisory: agent output"); n != 1 {
		t.Errorf("advisory printed %d times", n)
	}
	if n := countContaining(all, "cwd:"); n != 0 {
		t.Errorf("non-persist line printed %d times", n)
	}
}

func TestStartScreen_StatusUpsert(t *testing.T) {
	m := newStartModel(Options{Unicode: true})
	m, _ = sendMsg(m, StartupStatusMsg{Line: StatusLine{Key: "model", State: StatusRunning, Text: "finding a model…"}})
	m, _ = sendMsg(m, StartupStatusMsg{Line: StatusLine{Key: "model", State: StatusDone, Text: "example-model ready"}})
	v := stripANSI(m.View())
	if strings.Count(v, "example-model ready") != 1 || !strings.Contains(v, "✓ example-model ready") {
		t.Fatalf("want one done model line:\n%s", v)
	}
	if strings.Contains(v, "finding a model") {
		t.Fatalf("running line should have been replaced:\n%s", v)
	}
}

func TestStartScreen_PersistAfterDismissCommitsNotice(t *testing.T) {
	m := newStartModel(Options{Unicode: true})
	m = typeText(m, "/usage")
	m, _ = press(m, tea.KeyEnter)
	before := m.View()
	m, cmd := sendMsg(m, StartupStatusMsg{Line: StatusLine{Key: "fallback", State: StatusInfo, Text: "Using model example-model instead", Persist: true}})
	if countContaining(printed(cmd), "Using model example-model instead") != 1 {
		t.Fatal("persist line after dismissal must be committed")
	}
	if m.View() != before {
		t.Fatal("view must not change")
	}
	_, cmd = sendMsg(m, StartupStatusMsg{Line: StatusLine{Key: "x", Text: "transient"}})
	if len(printed(cmd)) != 0 {
		t.Fatal("non-persist line after dismissal must be dropped")
	}
	_, cmd = sendMsg(m, StartupStatusMsg{Line: StatusLine{Key: "bad", State: StatusFailed, Text: "boom", Persist: true}})
	if out := printed(cmd); len(out) != 1 || !strings.Contains(out[0], "boom") {
		t.Fatalf("failed persist line: %q", out)
	}
}

func TestStartScreen_ResizeAcrossTiers(t *testing.T) {
	m := newStartModel(Options{Unicode: true, InitialStatus: []StatusLine{
		{Key: "model", State: StatusRunning, Text: "finding a model…"},
		{Key: "mcp", State: StatusInfo, Text: "2 MCP servers"},
	}})
	if !m.ticking {
		t.Fatal("a running status line must own the tick chain from construction")
	}
	sizes := [][2]int{{120, 40}, {60, 40}, {30, 10}, {120, 40}, {0, 0}}
	for _, sz := range sizes {
		m, _ = sendMsg(m, tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		w := sz[0]
		if w == 0 {
			w = 80
		}
		for _, l := range strings.Split(stripANSI(m.View()), "\n") {
			if got := visibleWidth(l); got > w-1 {
				t.Errorf("%dx%d: line width %d > %d: %q", sz[0], sz[1], got, w-1, l)
			}
		}
		if !m.ticking {
			t.Errorf("%dx%d: ticking dropped", sz[0], sz[1])
		}
		if cmd := m.ensureTick(); cmd != nil {
			t.Errorf("%dx%d: a second tick chain would start", sz[0], sz[1])
		}
	}
}

func TestStartScreen_InitStartsOneChain(t *testing.T) {
	m := newStartModel(Options{Unicode: true, InitialStatus: []StatusLine{{Key: "model", State: StatusRunning, Text: "finding a model…"}}})
	if m.Init() == nil || !m.ticking {
		t.Fatal("Init must start the chain for a running line")
	}
	m, cmd := sendMsg(m, StartupStatusMsg{Line: StatusLine{Key: "mcp", State: StatusRunning, Text: "connecting MCP…"}})
	if cmd != nil {
		t.Fatal("a chain is already in flight; no second tick")
	}
	idle := newStartModel(Options{Unicode: true})
	if idle.ticking {
		t.Fatal("nothing runs; no chain")
	}
	idle, cmd = sendMsg(idle, StartupStatusMsg{Line: StatusLine{Key: "model", State: StatusRunning, Text: "finding a model…"}})
	if cmd == nil || !idle.ticking {
		t.Fatal("a late running line must start the chain")
	}
}

func TestStartScreen_SpinnerStopsWhenNothingRuns(t *testing.T) {
	m := newStartModel(Options{Unicode: true, InitialStatus: []StatusLine{{Key: "model", State: StatusDone, Text: "example-model ready"}}})
	m.ticking = true // a tick is in flight
	m, cmd := sendMsg(m, spinnerTickMsg{})
	if cmd != nil || m.ticking {
		t.Fatalf("chain must stop: cmd=%v ticking=%v", cmd != nil, m.ticking)
	}
}

func TestStartScreen_DismissedRunningLineStopsAnimating(t *testing.T) {
	m := newStartModel(Options{Unicode: true, InitialStatus: []StatusLine{{Key: "model", State: StatusRunning, Text: "finding a model…"}}})
	m = typeText(m, "/usage")
	m, _ = press(m, tea.KeyEnter)
	if m.animating() {
		t.Fatal("hidden start screen must not keep the spinner alive")
	}
}

func lineAbove(t *testing.T, view string) (rule, input string) {
	t.Helper()
	lines := strings.Split(stripANSI(view), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "> ") || strings.HasPrefix(l, "❯ ") {
			if i == 0 {
				t.Fatal("no line above input")
			}
			return lines[i-1], l
		}
	}
	t.Fatalf("no input line in:\n%s", view)
	return "", ""
}

func TestStartScreen_PromptRule(t *testing.T) {
	m := newStartModel(Options{Unicode: true})
	m, _ = sendMsg(m, tea.WindowSizeMsg{Width: 80, Height: 40})
	rule, input := lineAbove(t, m.View())
	if rule != strings.Repeat("─", 79) || !strings.HasPrefix(input, "❯ ") {
		t.Fatalf("rule=%q input=%q", rule, input)
	}
	m = typeText(m, "/usage")
	m, _ = press(m, tea.KeyEnter)
	if rule, _ := lineAbove(t, m.View()); rule != strings.Repeat("─", 79) {
		t.Fatalf("rule must persist after dismissal: %q", rule)
	}

	a := newStartModel(Options{Unicode: false})
	a, _ = sendMsg(a, tea.WindowSizeMsg{Width: 80, Height: 40})
	rule, input = lineAbove(t, a.View())
	if rule != strings.Repeat("-", 79) || !strings.HasPrefix(input, "> ") {
		t.Fatalf("ascii rule=%q input=%q", rule, input)
	}

	plain := NewModel(Options{Submit: noopSubmit, Now: fixedNow, Unicode: true})
	for _, l := range strings.Split(stripANSI(plain.View()), "\n") {
		if strings.Contains(l, "─") || strings.HasPrefix(l, "❯") {
			t.Fatalf("model without Start must keep today's output: %q", l)
		}
	}
}

func TestStartScreen_NoEscapeWhenColorDisabled(t *testing.T) {
	ui.Init(true)
	t.Cleanup(func() { ui.Init(false) })
	m := newStartModel(Options{Unicode: true, Depth: ui.ColorTrue})
	m, _ = sendMsg(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if strings.Contains(m.View(), "\x1b[") {
		t.Fatalf("color disabled but View has SGR:\n%q", m.View())
	}
}
