package console

import (
	"fmt"
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
	m.modelState = opts.InitialModelState
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
	if model == "" || m.modelState == ModelNone {
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

// noModelNotice is shown whenever a prompt cannot be sent for want of a model.
const noModelNotice = "no model available — start one with axis model start <name>, or configure ~/.axis/ai.yaml"

// ModelStateMsg reports that model resolution changed state.
type ModelStateMsg struct{ State ModelState }

// RunCommandMsg submits Text as if the operator typed it and pressed Enter.
// It is how a launch flag opens a console command such as /model at startup.
type RunCommandMsg struct{ Text string }

// isCommand reports whether text bypasses the model wait: slash commands and
// shell escapes are handled without the model.
func isCommand(text string) bool {
	return strings.HasPrefix(text, "/") || strings.HasPrefix(text, "!")
}

// waitingForModel reports whether prompts are held for a pending model, which
// is what keeps the "waiting for model" line spinning.
func (m Model) waitingForModel() bool {
	return m.modelState == ModelPending && len(m.held) > 0
}

// footerWidth is the width the footer lays out at. With the start path a 0x0
// size report must not collapse the footer while the splash draws at 80.
func (m Model) footerWidth() int {
	if m.start != nil {
		return m.layoutWidth()
	}
	return m.width
}

// heldLines is the waiting indicator drawn above the input while prompts wait.
func (m Model) heldLines() []Line {
	if !m.waitingForModel() {
		return nil
	}
	text := spinnerFrame(m.spinner, m.unicode) + " waiting for model…"
	if !m.unicode {
		text = asciiFallback(text)
	}
	return []Line{{Text: text, Style: StyleMuted}}
}

// submitDismissing retires the splash, then submits text. Sequence, not Batch:
// the header must reach scrollback before the turn's own lines.
func (m Model) submitDismissing(text string) (tea.Model, tea.Cmd) {
	m, startCmd := m.dismissStart()
	updated, cmd := m.submitText(text)
	if startCmd == nil {
		return updated, cmd
	}
	return updated, tea.Sequence(startCmd, cmd)
}

// runCommand handles RunCommandMsg through the normal submit path.
func (m Model) runCommand(text string) (Model, tea.Cmd) {
	text = strings.TrimSpace(text)
	if text == "" {
		return m, nil
	}
	updated, cmd := m.submitDismissing(text)
	return updated.(Model), cmd
}

// gatePrompt decides a non-command prompt while no model is ready. It reports
// handled=true when the prompt was held or refused, so the caller must not
// send it. The user entry is committed here, once; a held prompt is later sent
// with beginTurn so it is never committed twice. cmds carries persistence work
// the caller already built.
func (m Model) gatePrompt(text string, cmds []tea.Cmd) (Model, tea.Cmd, bool) {
	// Held prompts keep typed order: while any wait, a new prompt joins the
	// tail even if the model is now ready, so nothing overtakes them.
	if isCommand(text) || (m.modelState == ModelReady && len(m.held) == 0) {
		return m, nil, false
	}
	cmds = append(cmds, m.commit(NewUserEntry(m.now(), text)))
	if m.modelState == ModelNone && len(m.held) == 0 {
		cmds = append(cmds, m.commit(NewNoticeEntry(m.now(), noModelNotice)))
		return m, tea.Sequence(cmds...), true
	}
	// Copy so a Model value copied earlier never sees this write.
	m.held = append(append([]string(nil), m.held...), text)
	cmds = append(cmds, m.ensureTick()) // the waiting line spins
	return m, tea.Sequence(cmds...), true
}

// applyModelState records a model state change and acts on held prompts.
func (m Model) applyModelState(s ModelState) (Model, tea.Cmd) {
	m.modelState = s
	switch s {
	case ModelReady:
		return m.releaseHeld()
	case ModelNone:
		if len(m.held) == 0 {
			return m, nil
		}
		// Say it once and drop the prompts: they can never be sent.
		m.held = nil
		return m, m.commit(NewNoticeEntry(m.now(), noModelNotice))
	}
	return m, nil
}

// releaseHeld sends the oldest held prompt when the model is ready and no turn
// is running. One at a time: the next goes at the next turn boundary.
func (m Model) releaseHeld() (Model, tea.Cmd) {
	if m.modelState != ModelReady || m.state != turnIdle || len(m.held) == 0 {
		return m, nil
	}
	next := m.held[0]
	m.held = append([]string(nil), m.held[1:]...)
	return m.beginTurn(next)
}

// cancelHeld drops every held prompt on an idle cancel gesture. ok is false
// when there is nothing to cancel, so the gesture keeps its usual meaning; a
// cancel that does something never counts toward the quit gesture.
func (m Model) cancelHeld() (Model, tea.Cmd, bool) {
	if m.state != turnIdle || len(m.held) == 0 {
		return m, nil, false
	}
	m.interrupts = 0
	next, cmd := m.dropHeld()
	return next, cmd, true
}

// dropHeld discards held prompts with a notice; a no-op when none are held.
func (m Model) dropHeld() (Model, tea.Cmd) {
	n := len(m.held)
	if n == 0 {
		return m, nil
	}
	m.held = nil
	return m, m.commit(NewNoticeEntry(m.now(), fmt.Sprintf("cancelled: %d waiting prompt(s)", n)))
}
