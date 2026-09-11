package console

import (
	"fmt"
	"sort"
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

// ToolPendingMsg marks a tool call as in flight. It never commits to
// scrollback: the pending card renders in the ephemeral region and is
// retired when the completion entry (or the turn) lands. Args are already
// redacted by internal/agent and are shown in verbose mode only.
type ToolPendingMsg struct {
	Turn TurnID
	ID   string
	Name string
	Args string
}

// ToolResolvedMsg retires an in-flight tool card. The committed result card
// travels separately as a normal EntryMsg.
type ToolResolvedMsg struct {
	Turn TurnID
	ID   string
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

// SetOverlayMsg installs or replaces the active overlay.
// Passing a nil Overlay dismisses any active overlay.
type SetOverlayMsg struct {
	Overlay Overlay
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

	editor Editor
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

	// pendingTools holds in-flight tool cards in arrival order for the
	// ephemeral region. Keyed by tool-call ID; retired on completion or when
	// the turn settles. Updated only from Update on the event loop, so no
	// additional lock is needed.
	pendingTools []pendingTool

	// historySink persists submitted prompts off the event loop.
	historySink func(text string) tea.Cmd

	// escPending tracks the first Esc of the esc-esc clear gesture on an
	// idle draft; any other key disarms it.
	escPending bool

	// Thinking-span receipt measurement: chunk arrival is stamped at the
	// inThought transitions (only the console sees where the reasoning
	// block begins and ends in the stream).
	inThought    bool
	thoughtStart time.Time
	thoughtEnd   time.Time

	// lastThought retains the most recent committed thinking block in full
	// for /thought (committed entries are immutable and not re-rendered).
	lastThought string

	// tokenEstimate supplies the /usage context estimate; nil disables.
	tokenEstimate func() int

	// sessionStart anchors the /usage wall-time line.
	sessionStart time.Time

	spinner    int
	interrupts int // consecutive ctrl+c presses on an empty editor
	quitting   bool

	// atCompletion holds the active @ autocomplete state. Zero value is
	// inactive; it is recomputed on every rune insertion and cleared by
	// any navigation, deletion, or explicit dismissal.
	atCompletion atCompletionState

	// atCandidates supplies completion candidates for the text after the
	// leading '@'. Nil disables @ completion entirely.
	atCandidates func() []string
}

// atCompletionState tracks one active @ completion. Token is the text
// between the '@' and the cursor (without the '@'); Start is the rune
// offset of the '@' in the editor buffer; Candidates are the filtered
// matches; Ghost is the accepted-prefix remainder rendered muted after
// the cursor.
type atCompletionState struct {
	active     bool
	token      string
	start      int
	candidates []string
	ghost      string
}

// pendingTool is one in-flight row in the ephemeral region.
type pendingTool struct {
	turn TurnID
	id   string
	name string
	args string
}

// Options configures a console Model.
type Options struct {
	Submit SubmitFunc
	Cancel CancelFunc
	Footer Footer

	// History pre-seeds the command history ring.
	History []string

	// HistorySink persists a submitted prompt, invoked as a tea.Cmd so the
	// event loop never performs I/O. Nil disables persistence.
	HistorySink func(text string) tea.Cmd

	// Now supplies entry timestamps. Nil uses time.Now; tests inject a fixed
	// clock so committed entries render deterministically.
	Now func() time.Time

	// AtCandidates supplies @ completion candidates. Nil disables @
	// completion.
	AtCandidates func() []string

	// TokenEstimate supplies the /usage context-token estimate. Nil shows n/a.
	TokenEstimate func() int

	// CancelGrace bounds how long a cancelled turn may take to acknowledge
	// before the console returns to idle anyway. Zero uses the default.
	CancelGrace time.Duration
}

// Init starts the console.
func (m Model) Init() tea.Cmd { return nil }

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
	ed := NewEditor()
	if len(opts.History) > 0 {
		ed.SetHistory(opts.History)
	}
	return Model{
		width:         80,
		editor:        ed,
		submit:        opts.Submit,
		cancel:        opts.Cancel,
		footer:        opts.Footer,
		historySink:   opts.HistorySink,
		atCandidates:  opts.AtCandidates,
		tokenEstimate: opts.TokenEstimate,
		sessionStart:  now(),
		cancelGrace:   grace,
		now:           now,
	}
}

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

	case SetOverlayMsg:
		m.overlay = msg.Overlay
		return m, nil

	case StreamChunkMsg:
		if m.stale(msg.Turn) {
			return m, nil
		}
		m.stream.WriteString(msg.Text)
		m.stampThinkingSpan()
		return m, nil

	case ToolPendingMsg:
		if m.stale(msg.Turn) {
			return m, nil
		}
		m.addPendingTool(msg)
		return m, nil

	case ToolResolvedMsg:
		if m.stale(msg.Turn) {
			return m, nil
		}
		m.removePendingTool(msg.ID)
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

// expandThought re-commits the most recent thinking block in full. Committed
// entries are immutable, so the expand is a new block, never an in-place
// edit of scrollback.
func (m Model) expandThought() tea.Cmd {
	if m.lastThought == "" {
		return m.commit(NewNoticeEntry(m.now(), "no thinking block recorded this session"))
	}
	e := NewThinkingEntry(m.now(), m.lastThought)
	e.Expanded = true
	return m.commit(e)
}

// usageLine renders the /usage v1 block: context-token estimate, turn
// counter, and session wall time. No dollar cost and no in/out split: the
// repo has neither per-model metering nor a pricing source, and a fabricated
// figure would violate the Sources-of-Truth rule.
func (m Model) usageLine() string {
	tokens := "n/a"
	if m.tokenEstimate != nil {
		tokens = fmt.Sprintf("~%s", FormatTokens(m.tokenEstimate()))
	}
	return fmt.Sprintf("── session: %s tokens (estimate) · %d turns · %s wall ──",
		tokens, m.turn, time.Since(m.sessionStart).Round(time.Second))
}

// addPendingTool records an in-flight tool card. A repeated ID is a no-op:
// the ephemeral region must never show duplicate rows.
func (m *Model) addPendingTool(msg ToolPendingMsg) {
	for _, t := range m.pendingTools {
		if t.id == msg.ID {
			return
		}
	}
	m.pendingTools = append(m.pendingTools, pendingTool{
		turn: msg.Turn,
		id:   msg.ID,
		name: msg.Name,
		args: msg.Args,
	})
}

// removePendingTool retires one in-flight card by tool-call ID.
func (m *Model) removePendingTool(id string) {
	for i, t := range m.pendingTools {
		if t.id == id {
			m.pendingTools = append(m.pendingTools[:i], m.pendingTools[i+1:]...)
			return
		}
	}
}

// flushPendingTools drops every in-flight card belonging to turn. A turn
// that settles without emitting completions must not leave ghost spinners
// behind.
func (m *Model) flushPendingTools(turn TurnID) {
	kept := m.pendingTools[:0]
	for _, t := range m.pendingTools {
		if t.turn != turn {
			kept = append(kept, t)
		}
	}
	m.pendingTools = kept
}

// stampThinkingProgress tracks the inThought transitions across chunk
// arrival: start on false->true, end on true->false. The elapsed is a
// receipt-measured span (console-observed), the same honesty level as the
// ToolEntry elapsed is agent-measured where the agent owns timing.
func (m *Model) stampThinkingSpan() {
	_, _, inThought := parseStreamThought(m.stream.String())
	switch {
	case inThought && !m.inThought:
		m.thoughtStart = m.now()
	case !inThought && m.inThought:
		m.thoughtEnd = m.now()
	}
	m.inThought = inThought
}

// thinkingElapsed returns the measured thinking span for the finished turn,
// zero when none was observed or the span is still open.
func (m Model) thinkingElapsed() time.Duration {
	if m.thoughtEnd.IsZero() || m.thoughtStart.IsZero() {
		return 0
	}
	return m.thoughtEnd.Sub(m.thoughtStart)
}

func (m *Model) syncEditor() {
	if len(m.editor.runes) == 0 && m.input != "" {
		m.editor.SetText(m.input)
	} else {
		m.input = m.editor.Text()
	}
}

// handleKey processes operator input.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.syncEditor()
	key := msg.String()
	if key != "ctrl+c" {
		m.interrupts = 0
	}
	if key != "esc" {
		m.escPending = false
	}

	switch key {
	case "ctrl+c":
		// A press that clears the editor is consumed by that action and does
		// not count toward the quit sequence: clearing a draft must not leave
		// the session one accidental keystroke from exiting. Quitting takes
		// two presses on an already-empty editor.
		if m.editor.Text() != "" {
			m.editor.Clear()
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
		// An active completion consumes Esc before esc-esc arming: the
		// operator is dismissing a suggestion, not starting the clear
		// gesture.
		if m.atCompletion.active {
			m.atCompletion = atCompletionState{}
			return m, nil
		}
		if m.state != turnIdle {
			return m.requestCancel()
		}
		// agy-style esc esc: the first press arms the gesture, the second
		// clears the draft. Clearing a draft on a single accidental Esc was
		// too easy to trigger (same reasoning as the ctrl+c quit gesture).
		if !m.escPending {
			m.escPending = true
			return m, nil
		}
		m.escPending = false
		if m.editor.Text() != "" {
			m.editor.Clear()
			m.input = ""
		}
		return m, nil

	case "tab":
		return m.acceptAtCompletion()

	case "enter":
		return m.submitInput()

	case "backspace":
		m.atCompletion = atCompletionState{}
		m.editor.Backspace()
		m.input = m.editor.Text()
		return m, nil

	case "delete":
		m.atCompletion = atCompletionState{}
		m.editor.Delete()
		m.input = m.editor.Text()
		return m, nil

	case "left", "ctrl+b":
		m.atCompletion = atCompletionState{}
		m.editor.MoveLeft()
		return m, nil

	case "right", "ctrl+f":
		m.atCompletion = atCompletionState{}
		m.editor.MoveRight()
		return m, nil

	case "home", "ctrl+a":
		m.atCompletion = atCompletionState{}
		m.editor.MoveHome()
		return m, nil

	case "end", "ctrl+e":
		m.atCompletion = atCompletionState{}
		m.editor.MoveEnd()
		return m, nil

	case "ctrl+u":
		m.atCompletion = atCompletionState{}
		m.editor.DeleteToStart()
		m.input = m.editor.Text()
		return m, nil

	case "ctrl+k":
		m.atCompletion = atCompletionState{}
		m.editor.DeleteToEnd()
		m.input = m.editor.Text()
		return m, nil

	case "ctrl+w":
		m.atCompletion = atCompletionState{}
		m.editor.DeleteWordBefore()
		m.input = m.editor.Text()
		return m, nil
	case "up", "ctrl+p":
		m.atCompletion = atCompletionState{}
		m.editor.HistoryUp()
		m.input = m.editor.Text()
		return m, nil

	case "down", "ctrl+n":
		m.atCompletion = atCompletionState{}
		m.editor.HistoryDown()
		m.input = m.editor.Text()
		return m, nil
	}

	// Space arrives as its own key type rather than a rune.
	switch msg.Type {
	case tea.KeyRunes:
		m.editor.Insert(string(msg.Runes))
		m.input = m.editor.Text()
		return m, m.refreshAtCompletion()
	case tea.KeySpace:
		m.editor.Insert(" ")
		m.input = m.editor.Text()
		m.atCompletion = atCompletionState{}
	}
	return m, nil
}

// refreshAtCompletion recomputes the @ completion state after an insertion.
// The completion is active only when the token under the cursor begins with
// '@' and has at least one character of filter text; candidates come from
// the configured source, filtered by case-insensitive prefix.
func (m *Model) refreshAtCompletion() tea.Cmd {
	m.atCompletion = atCompletionState{}
	if m.atCandidates == nil {
		return nil
	}
	token, start := m.editor.TokenBeforeCursor()
	if len(token) < 2 || token[0] != '@' {
		return nil
	}
	filter := strings.ToLower(token[1:])
	var matches []string
	for _, c := range m.atCandidates() {
		if strings.HasPrefix(strings.ToLower(c), filter) {
			matches = append(matches, c)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	sort.Strings(matches)
	ghost := ""
	if prefix := commonPrefix(matches); strings.HasPrefix(strings.ToLower(prefix), filter) && len(prefix) > len(token)-1 {
		ghost = prefix[len(token)-1:]
	}
	m.atCompletion = atCompletionState{
		active:     true,
		token:      token[1:],
		start:      start,
		candidates: matches,
		ghost:      ghost,
	}
	return nil
}

// atGhost returns the ghost suffix to render after the typed text, or ""
// when no completion with a prefix extension is active.
func (m Model) atGhost() string {
	if m.atCompletion.active {
		return m.atCompletion.ghost
	}
	return ""
}

// acceptAtCompletion accepts the active completion's first candidate (or the
// common prefix when it extends the typed text) and clears the state.
func (m Model) acceptAtCompletion() (tea.Model, tea.Cmd) {
	if !m.atCompletion.active || len(m.atCompletion.candidates) == 0 {
		return m, nil
	}
	c := m.atCompletion.candidates[0]
	if m.atCompletion.ghost != "" {
		c = c[:len(m.atCompletion.token)+len(m.atCompletion.ghost)]
	}
	m.editor.AcceptCompletion(m.atCompletion.start, c)
	m.input = m.editor.Text()
	m.atCompletion = atCompletionState{}
	return m, nil
}

// requestCancel aborts the running turn and restores queued messages to the
// editor rather than discarding what the operator already typed. The turn is
// not considered finished until it acknowledges or the grace period expires.
func (m Model) requestCancel() (tea.Model, tea.Cmd) {
	if len(m.queued) > 0 {
		m.editor.SetText(strings.Join(m.queued, " "))
		m.input = m.editor.Text()
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

// commonPrefix returns the longest case-insensitive common prefix of the
// given strings. Empty input yields "".
func commonPrefix(items []string) string {
	if len(items) == 0 {
		return ""
	}
	prefix := items[0]
	for _, s := range items[1:] {
		for !strings.HasPrefix(strings.ToLower(s), strings.ToLower(prefix)) {
			r := []rune(prefix)
			prefix = string(r[:len(r)-1])
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
}

// submitInput commits the typed line. While a turn is in flight the line is
// queued as a steering message and delivered at the next turn boundary.
func (m Model) submitInput() (tea.Model, tea.Cmd) {
	m.syncEditor()
	text := strings.TrimSpace(m.editor.Submit())
	m.input = ""
	if text == "" {
		return m, nil
	}

	// Console-local commands intercept before persistence and turns: they
	// touch model-retained state, never become turns, and stay out of
	// history.jsonl (they are commands, not prompts).
	switch text {
	case "/thought":
		return m, m.expandThought()
	case "/usage":
		return m, m.commit(NewNoticeEntry(m.now(), m.usageLine()))
	}

	var cmds []tea.Cmd
	if m.historySink != nil {
		cmds = append([]tea.Cmd{}, m.historySink(text))
	}
	if m.state != turnIdle {
		m.queued = append(m.queued, text)
		// Queued prompts persist too: batch the persistence command with
		// the queue notice so neither is dropped.
		return m, tea.Batch(append(cmds, m.commit(NewNoticeEntry(m.now(), "queued: "+text)))...)
	}
	updated, startCmd := m.startTurn(text)
	return updated, tea.Batch(append(cmds, startCmd)...)
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
	// Both retirement paths (acknowledged TurnDoneMsg and the cancel-grace
	// timeout) flow through here: an abandoned turn's pending tool cards
	// must never outlive it.
	m.flushPendingTools(m.turn)

	var cmds []tea.Cmd
	var lastThought string
	if raw := strings.TrimSpace(m.stream.String()); raw != "" {
		thought, answer := extractThought(raw)
		if thought != "" {
			entry := NewThinkingEntry(m.now(), thought)
			entry.Elapsed = m.thinkingElapsed()
			lastThought = thought
			cmds = append(cmds, m.commit(entry))
		}
		if answer != "" {
			cmds = append(cmds, m.commit(NewAgentEntry(m.now(), answer)))
		}
	}
	m.stream.Reset()
	m.inThought = false
	m.thoughtStart = time.Time{}
	m.thoughtEnd = time.Time{}
	if lastThought != "" {
		m.lastThought = lastThought
	}

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
		_, answer, inThought := parseStreamThought(text)
		if inThought {
			lines = append(lines, Line{Text: spinnerFrames[m.spinner] + " thinking...", Style: StyleMuted})
		} else if answer != "" {
			for _, l := range wrap(answer, effectiveWidth(m.width)) {
				lines = append(lines, Line{Text: l})
			}
		}
	}

	for _, pt := range m.pendingTools {
		row := spinnerFrames[m.spinner] + " " + pt.name
		if pt.args != "" {
			row += " " + pt.args
		}
		lines = append(lines, Line{Text: clipRunes(row, effectiveWidth(m.width)), Style: StyleMuted})
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

	m.syncEditor()
	lines = append(lines, Line{
		Gutter:    "> ",
		Text:      m.editor.Text() + m.atGhost(),
		HasCursor: true,
		CursorPos: m.editor.Cursor(),
		HasGhost:  m.atCompletion.active && m.atCompletion.ghost != "",
	})

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

// History returns the history ring contents.
func (m Model) History() []string { return m.editor.History() }
