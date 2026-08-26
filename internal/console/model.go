package console

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The console runs on the main screen, never the alternate screen. Committed
// transcript content is handed to tea.Println, which prints above the program
// into the terminal's own scrollback and is a no-op under alt-screen. What
// View renders is only the ephemeral region: the in-progress response, the
// input line, the footer, and any overlay.

// TurnID identifies one agent turn.
//
// Producers capture their TurnID at construction and never read a shared
// "current turn": a bridge or stream writer belonging to an abandoned turn
// keeps stamping that turn's id, so its late output is recognisable as stale
// rather than inheriting whichever turn happens to be active when it lands.
// Ids are never reused.
type TurnID int64

// EntryMsg commits an entry to the transcript. Turn is the turn that produced
// it; a zero Turn marks an entry that belongs to no turn and is always shown.
type EntryMsg struct {
	Turn  TurnID
	Entry Entry
}

// StreamChunkMsg carries a coalesced batch of the assistant's token stream.
// Chunks accumulate in the ephemeral region and are committed as one
// AgentEntry when the turn ends, so a partial line never reaches scrollback.
type StreamChunkMsg struct {
	Turn TurnID
	Text string
}

// TurnDoneMsg reports that a turn finished. Err is nil on success.
type TurnDoneMsg struct {
	Turn TurnID
	Err  error
}

// cancelTimeoutMsg fires when a cancelled turn has not acknowledged in time.
type cancelTimeoutMsg struct{ Turn TurnID }

// spinnerTickMsg advances the busy indicator.
type spinnerTickMsg struct{}

// SubmitFunc runs a prompt through the agent. It must return promptly with a
// tea.Cmd that does the work off the input loop; the console never blocks on
// inference. Implementations stamp progress with the given TurnID and finish
// with a TurnDoneMsg carrying it.
type SubmitFunc func(turn TurnID, prompt string) tea.Cmd

// CancelFunc aborts an in-flight turn. It must be safe to call when idle and
// must not block.
type CancelFunc func(turn TurnID)

// Overlay is a focused component layered over the ephemeral region: the model
// picker, the approval prompt. An overlay owns the keyboard while visible.
type Overlay interface {
	// Update handles a message and returns the overlay to keep. Returning nil
	// dismisses it.
	Update(tea.Msg) (Overlay, tea.Cmd)

	// Render draws the overlay.
	Render(width int) []Line

	// Done reports that the overlay has finished and should be dismissed.
	Done() bool
}

// Footer renders the persistent status region below the input line.
type Footer interface {
	Render(width int) []Line
}

// turnState tracks the lifecycle of the active turn. Without it, a cancel
// that never produces a completion leaves the console busy forever and every
// later submission queues behind a turn that will not end.
type turnState int

const (
	turnIdle turnState = iota
	turnRunning
	turnCancelling
)

// defaultCancelGrace is how long a cancelled turn has to acknowledge before
// the console gives up waiting and returns to idle on its own.
const defaultCancelGrace = 3 * time.Second

// Model is the console state machine.
type Model struct {
	width  int
	height int

	input  string
	stream strings.Builder

	turn  TurnID
	state turnState

	// retired is the highest turn id that has settled. A turn-scoped message
	// is accepted only while its turn is still the active, running one, so a
	// completed or abandoned turn can never speak again.
	retired TurnID

	// queued holds steering messages typed while a turn is in flight. They are
	// delivered at the next turn boundary rather than interrupting.
	queued []string

	footer  Footer
	overlay Overlay

	submit SubmitFunc
	cancel CancelFunc
	now    func() time.Time

	cancelGrace time.Duration

	spinner    int
	interrupts int // consecutive ctrl+c presses on an empty editor
	quitting   bool
}

// Options configures a console Model.
type Options struct {
	Submit SubmitFunc
	Cancel CancelFunc
	Footer Footer

	// CancelGrace bounds how long a cancelled turn may take to acknowledge
	// before the console returns to idle anyway. Zero uses the default.
	CancelGrace time.Duration

	// Now supplies entry timestamps. Nil uses time.Now; tests inject a fixed
	// clock so committed entries render deterministically.
	Now func() time.Time
}

// NewModel builds a console model.
func NewModel(opts Options) Model {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	grace := opts.CancelGrace
	if grace <= 0 {
		grace = defaultCancelGrace
	}
	return Model{
		width:       80,
		submit:      opts.Submit,
		cancel:      opts.Cancel,
		footer:      opts.Footer,
		cancelGrace: grace,
		now:         now,
	}
}

// Init starts the console.
func (m Model) Init() tea.Cmd { return nil }

// spinnerFrames is the busy indicator. ASCII so it survives any terminal.
var spinnerFrames = []string{"-", "\\", "|", "/"}

const spinnerInterval = 120 * time.Millisecond

func spinnerTick() tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

// commit prints an entry above the program, into terminal scrollback. This is
// the only path by which transcript content leaves the console.
func (m Model) commit(e Entry) tea.Cmd {
	lines := e.Render(m.width)
	if len(lines) == 0 {
		return nil
	}
	return tea.Println(PaintAll(lines))
}

// stale reports whether a turn-scoped message must be ignored. Zero is never
// stale: it marks turn-independent content such as a startup notice.
//
// A message is accepted only while its turn is the active one and that turn
// has not settled. Checking equality alone is not enough: after a turn
// finishes, m.turn still holds its id, so a late event carrying that id would
// be accepted while the console sits idle.
func (m Model) stale(turn TurnID) bool {
	if turn == 0 {
		return false
	}
	return turn != m.turn || m.state == turnIdle || turn <= m.retired
}

// Update advances the state machine. It performs no I/O: every side effect
// leaves as a tea.Cmd, so a slow backend can never stall input and an approval
// or interrupt is never queued behind a read.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// An overlay owns the keyboard, but not the whole message stream: work
	// started before it opened must keep flowing into the transcript.
	if m.overlay != nil {
		updated, cmd := m.overlay.Update(msg)
		if updated == nil || updated.Done() {
			m.overlay = nil
		} else {
			m.overlay = updated
		}
		if _, isKey := msg.(tea.KeyMsg); isKey {
			return m, cmd
		}
		model, rest := m.route(msg)
		return model, tea.Batch(cmd, rest)
	}

	if key, ok := msg.(tea.KeyMsg); ok {
		return m.handleKey(key)
	}
	return m.route(msg)
}

// route handles non-keyboard messages.
func (m Model) route(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case EntryMsg:
		if m.stale(msg.Turn) {
			return m, nil
		}
		return m, m.commit(msg.Entry)

	case StreamChunkMsg:
		if m.stale(msg.Turn) {
			return m, nil
		}
		m.stream.WriteString(msg.Text)
		return m, nil

	case TurnDoneMsg:
		if m.stale(msg.Turn) {
			return m, nil
		}
		return m.finishTurn(msg.Err)

	case cancelTimeoutMsg:
		// The turn never acknowledged cancellation. Give up waiting rather
		// than leaving the console busy for the rest of the session.
		if msg.Turn != m.turn || m.state != turnCancelling {
			return m, nil
		}
		model, cmd := m.finishTurn(nil)
		return model, tea.Batch(
			m.commit(NewNoticeEntry(m.now(), "cancelled (backend did not acknowledge)")),
			cmd,
		)

	case spinnerTickMsg:
		if m.state == turnIdle {
			return m, nil
		}
		m.spinner = (m.spinner + 1) % len(spinnerFrames)
		return m, spinnerTick()
	}
	return m, nil
}

// handleKey processes operator input.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key != "ctrl+c" {
		m.interrupts = 0
	}

	switch key {
	case "ctrl+c":
		// A press that clears the editor is consumed by that action and does
		// not count toward the quit sequence: clearing a draft must not leave
		// the session one accidental keystroke from exiting. Quitting takes
		// two presses on an already-empty editor.
		if m.input != "" {
			m.input = ""
			m.interrupts = 0
			return m, nil
		}
		m.interrupts++
		if m.interrupts >= 2 {
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil

	case "esc":
		return m.requestCancel()

	case "enter":
		return m.submitInput()

	case "backspace":
		if m.input != "" {
			runes := []rune(m.input)
			m.input = string(runes[:len(runes)-1])
		}
		return m, nil
	}

	// Space arrives as its own key type rather than a rune.
	switch msg.Type {
	case tea.KeyRunes:
		m.input += string(msg.Runes)
	case tea.KeySpace:
		m.input += " "
	}
	return m, nil
}

// requestCancel aborts the running turn and restores queued messages to the
// editor rather than discarding what the operator already typed. The turn is
// not considered finished until it acknowledges or the grace period expires.
func (m Model) requestCancel() (tea.Model, tea.Cmd) {
	if len(m.queued) > 0 {
		m.input = strings.Join(m.queued, " ")
		m.queued = nil
	}
	if m.state != turnRunning {
		return m, nil
	}

	m.state = turnCancelling
	cancelled := m.turn
	var cmds []tea.Cmd
	if m.cancel != nil {
		c := m.cancel
		cmds = append(cmds, func() tea.Msg {
			c(cancelled)
			return nil
		})
	}
	cmds = append(cmds, tea.Tick(m.cancelGrace, func(time.Time) tea.Msg {
		return cancelTimeoutMsg{Turn: cancelled}
	}))
	return m, tea.Batch(cmds...)
}

// submitInput commits the typed line. While a turn is in flight the line is
// queued as a steering message and delivered at the next turn boundary.
func (m Model) submitInput() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input)
	if text == "" {
		return m, nil
	}
	m.input = ""

	if m.state != turnIdle {
		m.queued = append(m.queued, text)
		return m, m.commit(NewNoticeEntry(m.now(), "queued: "+text))
	}
	return m.startTurn(text)
}

// startTurn commits the operator's line and hands the prompt to the agent.
func (m Model) startTurn(text string) (Model, tea.Cmd) {
	cmds := []tea.Cmd{m.commit(NewUserEntry(m.now(), text))}

	if m.submit == nil {
		cmds = append(cmds, m.commit(NewErrorEntry(m.now(), "no agent backend attached")))
		return m, tea.Batch(cmds...)
	}

	m.turn++
	m.state = turnRunning
	m.spinner = 0
	m.stream.Reset()

	cmds = append(cmds, m.submit(m.turn, text), spinnerTick())
	return m, tea.Batch(cmds...)
}

// finishTurn commits the accumulated response and drains one queued message.
// The turn is retired here, on both the acknowledged and abandoned paths, so
// nothing carrying its id is accepted afterwards.
func (m Model) finishTurn(err error) (Model, tea.Cmd) {
	m.state = turnIdle
	m.retired = m.turn

	var cmds []tea.Cmd
	if text := strings.TrimSpace(m.stream.String()); text != "" {
		cmds = append(cmds, m.commit(NewAgentEntry(m.now(), text)))
	}
	m.stream.Reset()

	if err != nil {
		cmds = append(cmds, m.commit(NewErrorEntry(m.now(), err.Error())))
	}

	// Steering messages are delivered one at a time: the next turn starts only
	// after the previous one has settled.
	if len(m.queued) > 0 {
		next := m.queued[0]
		m.queued = m.queued[1:]
		updated, cmd := m.startTurn(next)
		m = updated
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// SetOverlay shows an overlay, which takes the keyboard until dismissed.
func (m Model) SetOverlay(o Overlay) Model {
	m.overlay = o
	return m
}

// View renders the ephemeral region only. Committed transcript content is
// already in scrollback and is never re-rendered here.
func (m Model) View() string {
	if m.quitting {
		return ""
	}

	var lines []Line

	// An in-progress response lives here until the turn ends, so scrollback
	// never receives a partial line.
	if text := m.stream.String(); text != "" {
		for _, l := range wrap(text, effectiveWidth(m.width)) {
			lines = append(lines, Line{Text: l})
		}
	}

	switch m.state {
	case turnRunning:
		lines = append(lines, Line{Text: spinnerFrames[m.spinner] + " working", Style: StyleMuted})
	case turnCancelling:
		lines = append(lines, Line{Text: spinnerFrames[m.spinner] + " cancelling", Style: StyleMuted})
	}

	if m.overlay != nil {
		lines = append(lines, m.overlay.Render(m.width)...)
	}

	lines = append(lines, Line{Gutter: "> ", Text: m.input})

	if m.footer != nil {
		lines = append(lines, m.footer.Render(m.width)...)
	}

	return PaintAll(lines)
}

// Busy reports whether a turn is running or cancelling.
func (m Model) Busy() bool { return m.state != turnIdle }

// Cancelling reports whether a cancel is awaiting acknowledgement.
func (m Model) Cancelling() bool { return m.state == turnCancelling }

// Turn returns the active turn id.
func (m Model) Turn() TurnID { return m.turn }

// Retired returns the highest turn id that has settled. A launcher uses it to
// know which abandoned turns must be drained before their sinks are reused.
func (m Model) Retired() TurnID { return m.retired }

// Queued returns the pending steering messages.
func (m Model) Queued() []string { return append([]string(nil), m.queued...) }

// Input returns the current editor contents.
func (m Model) Input() string { return m.input }
