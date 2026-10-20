package console

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/ui"
)

// ModelState is the startup state of the active model as the footer shows it.
// It is defined here, ahead of the start-screen behavior that drives it, so the
// footer contract stays stable while that behavior lands.
type ModelState int

const (
	// ModelReady means a model is bound; the footer shows its name. It is the
	// zero value so a Model that never learns a state behaves as it always did.
	ModelReady ModelState = iota
	// ModelPending means a model is still being resolved; the footer shows the
	// spinner frame in place of a name.
	ModelPending
	// ModelNone means resolution finished and found no model.
	ModelNone
)

// defaultWidth is the layout width used while the terminal size is unknown
// (before the first WindowSizeMsg, or a size of 0).
const defaultWidth = 80

// StartupStatusMsg upserts one startup status line, keyed by Line.Key.
type StartupStatusMsg struct{ Line StatusLine }

// initStart copies the start-screen options into a freshly built Model. When
// a status line is already running the tick chain is marked started here:
// Init has a value receiver and cannot record it, and without the mark a later
// ensureTick would start a second chain.
func (m *Model) initStart(opts Options) {
	m.start = opts.Start
	if m.start == nil {
		return
	}
	m.startVersion = opts.Version
	m.startDepth = opts.Depth
	m.headerModel = opts.HeaderModel
	m.status = append([]StatusLine(nil), opts.InitialStatus...)
	m.ticking = m.animating()
}

// startVisible reports whether the splash is on screen.
func (m Model) startVisible() bool { return m.start != nil && !m.startDismissed }

// startRunning reports whether a visible status line needs the spinner. A
// dismissed splash draws nothing, so it must not keep the tick chain alive.
func (m Model) startRunning() bool {
	if !m.startVisible() {
		return false
	}
	for _, l := range m.status {
		if l.State == StatusRunning {
			return true
		}
	}
	return false
}

// layoutWidth is the width to lay the start screen and rule out at.
func (m Model) layoutWidth() int {
	if m.width <= 0 {
		return defaultWidth
	}
	return m.width
}

// startState builds the splash input for this frame. Painted lines bypass the
// normal color guard, so the depth is forced off whenever color is disabled.
func (m Model) startState() StartState {
	depth := m.startDepth
	if !ui.Enabled() {
		depth = ui.ColorNone
	}
	return StartState{
		Version: m.startVersion,
		Unicode: m.unicode,
		Depth:   depth,
		Frame:   spinnerFrame(m.spinner, m.unicode),
		Status:  m.status,
	}
}

// startLines returns the splash rows to prepend to the ephemeral region.
func (m Model) startLines() []Line {
	if !m.startVisible() {
		return nil
	}
	return m.start.Render(m.layoutWidth(), m.height, m.startState())
}

// promptRule is the muted rule drawn above the input on every frame once the
// start path is in use. It stops at width-1 so it never touches the last
// column, where terminals auto-wrap.
func (m Model) promptRule() []Line {
	if m.start == nil {
		return nil
	}
	glyph := "─"
	if !m.unicode {
		glyph = "-"
	}
	n := m.layoutWidth() - 1
	if n < 1 {
		return nil
	}
	return []Line{{Text: strings.Repeat(glyph, n), Style: StyleMuted}}
}

// inputGutter is the input line's marker. The Unicode chevron belongs to the
// start path only, so consoles without Start render exactly as before.
func (m Model) inputGutter() string {
	if m.start != nil && m.unicode {
		return "❯ "
	}
	return "> "
}

// applyStatus upserts a status line. Once the splash is gone there is nowhere
// to show progress, so only Persist lines are kept, as scrollback notices.
func (m Model) applyStatus(l StatusLine) (Model, tea.Cmd) {
	if m.start == nil {
		return m, nil
	}
	if m.startDismissed {
		if !l.Persist {
			return m, nil
		}
		return m, m.commitStatus(l)
	}
	// Copy so a Model value copied earlier never sees this write.
	next := append([]StatusLine(nil), m.status...)
	replaced := false
	for i := range next {
		if next[i].Key == l.Key {
			next[i] = l
			replaced = true
			break
		}
	}
	if !replaced {
		next = append(next, l)
	}
	m.status = next
	if !m.animating() {
		return m, nil
	}
	return m, m.ensureTick() // nil when a chain is already live
}

// commitStatus writes a persisted status line to scrollback.
func (m Model) commitStatus(l StatusLine) tea.Cmd {
	if l.State == StatusFailed {
		return m.commit(NewErrorEntry(m.now(), l.Text))
	}
	return m.commit(NewNoticeEntry(m.now(), l.Text))
}

// headerLine is the one-line record of the splash left in scrollback.
func (m Model) headerLine() string {
	model := ""
	if m.headerModel != nil {
		model = m.headerModel()
	}
	if model == "" {
		model = "no model"
	}
	line := "▲ AXIS agent · v" + m.startVersion + " · " + model
	if !m.unicode {
		line = asciiFallback(line)
	}
	return line
}

// dismissStart retires the splash exactly once: it leaves the header and every
// Persist line in scrollback, in order, then drops the status list so nothing
// can print twice. It is a no-op without a visible splash.
func (m Model) dismissStart() (Model, tea.Cmd) {
	if !m.startVisible() {
		return m, nil
	}
	m.startDismissed = true
	cmds := []tea.Cmd{m.commit(NewNoticeEntry(m.now(), m.headerLine()))}
	for _, l := range m.status {
		if l.Persist {
			cmds = append(cmds, m.commitStatus(l))
		}
	}
	m.status = nil
	return m, tea.Sequence(cmds...)
}
