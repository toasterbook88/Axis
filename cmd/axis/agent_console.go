package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/console"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/persist"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/ui"
)

// The transcript console is an opt-in interactive surface for `axis agent`
// (--console, interactive TTY only). Bubble Tea owns stdin in raw mode, so
// approvals go through ApprovalOverlay rather than a synchronous stdin prompt.
// Yes is an explicit y; Enter does not approve; timeout and context cancel deny.

// consoleRunner executes one agent turn. The launcher injects the real
// Agent.RunWithSinks; tests inject a fake.
type consoleRunner func(ctx context.Context, prompt string, obs agent.Observer, out io.Writer) error

// consoleLauncher owns the per-turn wiring: one context, one bridge, and one
// stream writer per turn, none of them shared or reused.
type consoleLauncher struct {
	run     consoleRunner
	timeout time.Duration
	now     func() time.Time

	// slash routes a slash command through the existing REPL handler. Nil
	// disables slash handling.
	slash func(string) (string, error)

	// safety checks shell commands before execution.
	safety agent.ShellSafetyGate

	// loader supplies the cached cluster runtime; it feeds the model catalog
	// for the interactive picker.
	loader func(context.Context) (*runtimectx.Context, error)

	// activeTarget is the model choice captured at startup. The picker hands
	// it to the switch session; the live model afterwards is the agent's own
	// state (Agent.Model()), which the footer reads.
	activeTarget ModelChoice

	// modelSwitch applies a ModelChoice to the live agent. Tests inject a
	// recording stub; production uses consoleModelSwitch.
	modelSwitch func(choice ModelChoice, out io.Writer) error

	// skillEffect runs a chosen learned-skill command as the agent prompt for
	// turn. The turn is supplied so the run's context registers in the
	// launcher's cancels map (Esc aborts a running skill like any turn).
	skillEffect func(ctx context.Context, turn console.TurnID, command string, out io.Writer) error

	// mcpAction prints a server listing for the selected action id
	// (tools/resources/diagnostics) — the explore-then-read loop, committed
	// to the transcript between selections.
	mcpAction func(server, action string, out io.Writer)

	// mcpRegistry supplies the connected-server registry for the /mcp menu.
	mcpRegistry func() *mcpclient.Registry

	mu       sync.Mutex
	cancels  map[console.TurnID]context.CancelFunc
	inFlight int

	// prog is the running program. Set before the first turn is submitted.
	prog interface{ Send(tea.Msg) }
}

func newConsoleLauncher(run consoleRunner, timeout time.Duration, now func() time.Time) *consoleLauncher {
	if now == nil {
		now = time.Now
	}
	return &consoleLauncher{
		run:     run,
		timeout: timeout,
		now:     now,
		cancels: map[console.TurnID]context.CancelFunc{},
	}
}

// submit builds the tea.Cmd that runs one turn. The turn id is captured here
// and handed to freshly constructed sinks, so an abandoned turn keeps writing
// to its own bridge and writer and can never be misattributed.
func (l *consoleLauncher) submit(parent context.Context) console.SubmitFunc {
	return func(turn console.TurnID, prompt string) tea.Cmd {
		if prompt == "/model" || prompt == "/models" {
			return l.runModelPicker(parent, turn)
		}
		if prompt == "/skills" {
			return l.runSkillPicker(parent, turn)
		}
		if prompt == "/mcp" {
			return l.runMCPPicker(parent, turn)
		}
		if strings.HasPrefix(prompt, "/") {
			return l.runSlash(turn, prompt)
		}
		if prompt == "?" {
			return l.runSlash(turn, "/help")
		}
		if strings.HasPrefix(prompt, "!") {
			return l.runShell(parent, turn, strings.TrimPrefix(prompt, "!"))
		}

		ctx, cancel := context.WithTimeout(parent, l.timeout)

		l.mu.Lock()
		l.cancels[turn] = cancel
		// RunWithSinks blocks on the agent's run lock, so a turn submitted
		// while an abandoned one is still draining will wait rather than
		// race it. Say so instead of presenting the new turn as independent.
		waiting := l.inFlight > 0
		l.inFlight++
		l.mu.Unlock()

		return func() tea.Msg {
			defer func() {
				l.mu.Lock()
				delete(l.cancels, turn)
				l.inFlight--
				l.mu.Unlock()
				cancel()
			}()

			if waiting && l.prog != nil {
				l.prog.Send(console.EntryMsg{Entry: console.NewNoticeEntry(
					l.now(), "waiting for a previous turn to finish draining")})
			}

			// Fresh sinks per turn, both stamped with this immutable id.
			writer := console.NewStreamWriter(l.prog, turn)
			bridge := console.NewBridge(l.prog, turn, l.now)
			defer writer.Close()

			err := l.run(ctx, prompt, bridge, writer)

			// Flush the tail before the completion lands so no streamed text
			// arrives after the turn is reported done.
			_ = writer.Close()
			return console.TurnDoneMsg{Turn: turn, Err: err}
		}
	}
}

// runSlash routes a slash command through the existing REPL handler. The
// console does not parse commands itself.
func (l *consoleLauncher) runSlash(turn console.TurnID, line string) tea.Cmd {
	return func() tea.Msg {
		if l.slash == nil {
			return console.TurnDoneMsg{Turn: turn, Err: errors.New("slash commands are not available in this console")}
		}
		out, err := l.slash(line)
		if out != "" && l.prog != nil {
			l.prog.Send(console.EntryMsg{Turn: turn, Entry: console.NewNoticeEntry(l.now(), strings.TrimSpace(out))})
		}
		return console.TurnDoneMsg{Turn: turn, Err: err}
	}
}

// runShell executes an instant local shell escape (!<cmd>) directly in a local subshell.
//
// ARCHITECTURAL BOUNDARY (Layer 4 vs Operator Escape):
// A console bang (!<cmd>) deliberately skips Layer 4 cluster placement and reservation
// leases. While agent tool calls (run_shell, run_on_node) require Layer 4 to prevent
// autonomous LLMs from oversubscribing nodes, an interactive shell escape is an explicit
// human operator command (Standing Law 1: Operator is Commander).
//
// Routing !<cmd> through Layer 4 would introduce fatal operational paradoxes:
// 1. Diagnostics Lockout: An operator could not run '!axis doctor' if the daemon/ledger was wedged.
// 2. Low-Memory Rejection: Commands like '!free -m' or '!ps' would fail if free RAM was below the 1GB cap.
// 3. Ledger/Disk Overhead: Every '!ls' would write task logs and acquire ledger file locks.
//
// However, to protect against accidental destructive inputs (e.g. bad clipboard paste),
// runShell enforces Layer 4 Safety Evaluation (safety.Check / DefaultSafetyGate), blocking
// destructive commands (score >= 80) before subprocess creation, and reports non-zero exit
// codes faithfully via TurnDoneMsg.
//
// The escape shares the agent turn's per-request timeout (--timeout, default
// 5m), so long-lived diagnostics such as `tail -f` are bounded the same way a
// turn is. Captured output is capped to agent.MaxShellOutputRunes like every
// other shell execution surface, so a verbose command cannot flood the
// transcript.
func (l *consoleLauncher) runShell(parent context.Context, turn console.TurnID, cmdLine string) tea.Cmd {
	cmdLine = strings.TrimSpace(cmdLine)
	ctx, cancel := context.WithTimeout(parent, l.timeout)

	l.mu.Lock()
	l.cancels[turn] = cancel
	l.inFlight++
	l.mu.Unlock()

	return func() tea.Msg {
		defer func() {
			l.mu.Lock()
			delete(l.cancels, turn)
			l.inFlight--
			l.mu.Unlock()
			cancel()
		}()

		if cmdLine == "" {
			return console.TurnDoneMsg{Turn: turn, Err: errors.New("empty shell command")}
		}

		gate := l.safety
		if gate == nil {
			gate = agent.DefaultSafetyGate(nil)
		}
		if allow, reason, score := gate(cmdLine); !allow {
			return console.TurnDoneMsg{
				Turn: turn,
				Err:  fmt.Errorf("command blocked by safety check (score %d/100): %s", score, reason),
			}
		}

		cmd := exec.CommandContext(ctx, "sh", "-c", cmdLine)
		cmd.WaitDelay = 100 * time.Millisecond
		out, err := cmd.CombinedOutput()
		trimmed := strings.TrimRight(string(out), "\n")
		if trimmed != "" && l.prog != nil {
			l.prog.Send(console.EntryMsg{
				Turn:  turn,
				Entry: console.NewNoticeEntry(l.now(), agent.CapShellOutput(trimmed)),
			})
		}
		if err != nil && l.prog != nil {
			l.prog.Send(console.EntryMsg{
				Turn:  turn,
				Entry: console.NewErrorEntry(l.now(), fmt.Sprintf("command exited with error: %v", err)),
			})
		}
		return console.TurnDoneMsg{Turn: turn, Err: err}
	}
}

// modelNoticeWriter forwards each completed line written during a model
// switch into the transcript as a notice entry.
type modelNoticeWriter struct {
	prog interface{ Send(tea.Msg) }
	turn console.TurnID
	now  func() time.Time
	buf  []byte
}

func (w *modelNoticeWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:idx]), "\r")
		w.buf = w.buf[idx+1:]
		if strings.TrimSpace(line) != "" && w.prog != nil {
			w.prog.Send(console.EntryMsg{
				Turn:  w.turn,
				Entry: console.NewNoticeEntry(w.now(), line),
			})
		}
	}
	return len(p), nil
}

// pickerItems converts the collected catalog into picker rows using the same
// provider/node/endpoint detail formatting the REPL selector shows; the
// disabled reason is preserved so the console shows why a row is not
// selectable (the overlay adds its generic "(unreachable)" label marker).
func pickerItems(choices []ModelChoice) []console.PickerItem {
	items := make([]console.PickerItem, 0, len(choices))
	for _, c := range choices {
		detail := c.ProviderName + " - " + c.ProviderKind
		if c.ProviderKind == "local" {
			if c.Node != "" {
				detail = fmt.Sprintf("Remote node %s [%s] (%s)", c.Node, c.ProviderName, c.Endpoint)
			} else {
				detail = fmt.Sprintf("Local node [%s] (%s)", c.ProviderName, c.Endpoint)
			}
		}
		if c.Disabled && c.DisabledReason != "" {
			detail = fmt.Sprintf("%s (%s)", detail, c.DisabledReason)
		}
		items = append(items, console.PickerItem{ID: c.ID, Label: c.Model, Detail: detail, Disabled: c.Disabled})
	}
	return items
}

// consoleModelSwitch returns the switch function the picker applies: it reuses
// the REPL switch contract wholesale (backend rebuild, guarded runner
// refresh, OwnerLabel provenance) around the console's live agent, capturing
// its status lines into the supplied writer.
func consoleModelSwitch(a *agent.Agent, loader func(context.Context) (*runtimectx.Context, error), target ModelChoice) func(choice ModelChoice, out io.Writer) error {
	return func(choice ModelChoice, out io.Writer) error {
		session := &agentREPLSession{
			Agent:        a,
			Runtime:      loader,
			Selector:     refusingSelector{},
			In:           refusingLineReader{},
			Out:          out,
			ErrOut:       out,
			ActiveTarget: target,
		}
		return switchAgentToModelChoice(session, choice)
	}
}

// consoleFooterModel picks the statusline model label: the agent's live
// model wins once it is set (it tracks /model switches), then the startup
// choice captured at console launch.
func consoleFooterModel(a *agent.Agent, target ModelChoice) string {
	if a != nil {
		if m := a.Model(); m != "" {
			return m
		}
	}
	if target.Model != "" {
		return target.Model
	}
	if target.ID != "" {
		return target.ID
	}
	return "default"
}

// runModelPicker opens the interactive model picker for arg-less /model.
// Catalog loading happens here (off the UI loop) because collectModelChoices
// probes resident endpoints. No approval gate applies: the picker only
// switches inference targets, it executes nothing. Unlike approvals there is
// no timeout — the picker is operator-initiated, owns the keyboard while
// open, and Esc dismisses without a switch.
func (l *consoleLauncher) runModelPicker(parent context.Context, turn console.TurnID) tea.Cmd {
	// Child context registered exactly like every other turn (runShell): an
	// Esc during the keyboard-free catalog window must reach this Cmd, and
	// the deferred cleanup clears the registration. On cancellation the
	// overlay is dismissed and the turn reports clean — an operator cancel
	// is not a failure, and a picker must never install over a retired turn.
	ctx, cancel := context.WithCancel(parent)

	l.mu.Lock()
	l.cancels[turn] = cancel
	l.inFlight++
	l.mu.Unlock()

	return func() tea.Msg {
		defer func() {
			l.mu.Lock()
			delete(l.cancels, turn)
			l.inFlight--
			l.mu.Unlock()
			cancel()
		}()

		rt, err := l.loader(ctx)
		if err != nil {
			return console.TurnDoneMsg{Turn: turn, Err: fmt.Errorf("model catalog unavailable: %w", err)}
		}
		if rt == nil {
			return console.TurnDoneMsg{Turn: turn, Err: errors.New("model catalog unavailable: runtime loader returned no context")}
		}
		choices := collectModelChoices(rt)
		if len(choices) == 0 {
			l.prog.Send(console.EntryMsg{
				Turn:  turn,
				Entry: console.NewNoticeEntry(l.now(), "No models found (neither local Ollama models nor enabled cloud providers)."),
			})
			return console.TurnDoneMsg{Turn: turn, Err: nil}
		}

		// A cancel may have landed while the catalog was loading. Refuse to
		// install over a cancelled turn.
		select {
		case <-ctx.Done():
			return console.TurnDoneMsg{Turn: turn, Err: nil}
		default:
		}

		reply := make(chan string, 1)
		l.prog.Send(console.SetOverlayMsg{
			Overlay: console.NewModelPickerOverlay("Select active model for task routing:", pickerItems(choices), reply),
		})

		var chosenID string
		select {
		case chosenID = <-reply:
		case <-ctx.Done():
			if l.prog != nil {
				l.prog.Send(console.SetOverlayMsg{Overlay: nil})
			}
			return console.TurnDoneMsg{Turn: turn, Err: nil}
		}
		if chosenID == "" {
			return console.TurnDoneMsg{Turn: turn, Err: nil} // operator dismissed
		}
		if ctx.Err() != nil {
			// The turn was cancelled around the same moment as a selection:
			// never switch on a retired turn.
			if l.prog != nil {
				l.prog.Send(console.SetOverlayMsg{Overlay: nil})
			}
			return console.TurnDoneMsg{Turn: turn, Err: nil}
		}

		chosen, err := findModelTargetByRef(choices, chosenID)
		if err != nil {
			return console.TurnDoneMsg{Turn: turn, Err: err}
		}
		if l.modelSwitch == nil {
			return console.TurnDoneMsg{Turn: turn, Err: errors.New("model switching is not available in this console")}
		}
		nw := &modelNoticeWriter{prog: l.prog, turn: turn, now: l.now}
		if err := l.modelSwitch(chosen, nw); err != nil {
			return console.TurnDoneMsg{Turn: turn, Err: err}
		}
		return console.TurnDoneMsg{Turn: turn, Err: nil}
	}
}

// runSkillPicker opens the learned-skill picker for arg-less /skills.
// The selection runs the skill's command as the next agent prompt through
// skillEffect — the same effect the REPL selector produces.
func (l *consoleLauncher) runSkillPicker(parent context.Context, turn console.TurnID) tea.Cmd {
	// Child context registered exactly like every other turn (runShell,
	// runModelPicker): an Esc during the keyboard-free catalog window must
	// reach this Cmd, and the deferred cleanup clears the registration.
	ctx, cancel := context.WithCancel(parent)

	l.mu.Lock()
	l.cancels[turn] = cancel
	l.inFlight++
	l.mu.Unlock()

	return func() tea.Msg {
		defer func() {
			l.mu.Lock()
			delete(l.cancels, turn)
			l.inFlight--
			l.mu.Unlock()
			cancel()
		}()

		rt, err := l.loader(ctx)
		if err != nil {
			return console.TurnDoneMsg{Turn: turn, Err: fmt.Errorf("skill catalog unavailable: %w", err)}
		}
		choices := skillChoices(rt)
		if len(choices) <= 1 { // only the cancel row: nothing learned yet
			l.prog.Send(console.EntryMsg{
				Turn:  turn,
				Entry: console.NewNoticeEntry(l.now(), "No learned skills yet"),
			})
			return console.TurnDoneMsg{Turn: turn, Err: nil}
		}
		if l.skillEffect == nil {
			return console.TurnDoneMsg{Turn: turn, Err: fmt.Errorf("skill execution is not available in this console")}
		}

		// Refuse to install over a turn cancelled while the catalog loaded.
		select {
		case <-ctx.Done():
			return console.TurnDoneMsg{Turn: turn, Err: nil}
		default:
		}

		reply := make(chan string, 1)
		items := make([]console.PickerItem, 0, len(choices))
		for _, c := range choices {
			items = append(items, console.PickerItem{ID: c.ID, Label: c.Label, Detail: c.Detail})
		}
		l.prog.Send(console.SetOverlayMsg{
			Overlay: console.NewModelPickerOverlay("Execute a learned skill:", items, reply),
		})

		var chosenID string
		select {
		case chosenID = <-reply:
		case <-ctx.Done():
			if l.prog != nil {
				l.prog.Send(console.SetOverlayMsg{Overlay: nil})
			}
			return console.TurnDoneMsg{Turn: turn, Err: nil}
		}
		if chosenID == "" || chosenID == "none" {
			return console.TurnDoneMsg{Turn: turn, Err: nil} // operator cancelled
		}
		if ctx.Err() != nil {
			// A cancellation landed around the same moment as the selection:
			// never run a skill for a retired turn.
			return console.TurnDoneMsg{Turn: turn, Err: nil}
		}
		command := skillCommand(rt, chosenID)
		if command == "" {
			return console.TurnDoneMsg{Turn: turn, Err: fmt.Errorf("skill %q not found", chosenID)}
		}
		nw := &modelNoticeWriter{prog: l.prog, turn: turn, now: l.now}
		fmt.Fprintf(nw, "Running skill command: %s\n", command)
		if err := l.skillEffect(ctx, turn, command, nw); err != nil {
			return console.TurnDoneMsg{Turn: turn, Err: err}
		}
		return console.TurnDoneMsg{Turn: turn, Err: nil}
	}
}

// runMCPPicker drives the staged /mcp explorer: server menu → action menu →
// committed listing → action menu again ("Back" returns to the server menu).
func (l *consoleLauncher) runMCPPicker(parent context.Context, turn console.TurnID) tea.Cmd {
	// Child context registered exactly like every other turn: an Esc during
	// the keyboard-free menu window must reach this Cmd.
	ctx, cancel := context.WithCancel(parent)

	l.mu.Lock()
	l.cancels[turn] = cancel
	l.inFlight++
	l.mu.Unlock()

	return func() tea.Msg {
		defer func() {
			l.mu.Lock()
			delete(l.cancels, turn)
			l.inFlight--
			l.mu.Unlock()
			cancel()
		}()

		for {
			if l.mcpRegistry == nil || l.mcpAction == nil {
				return console.TurnDoneMsg{Turn: turn, Err: fmt.Errorf("MCP picker is not available in this console")}
			}

			servers := mcpServerChoices(l.mcpRegistry())
			if len(servers) == 0 {
				l.prog.Send(console.EntryMsg{
					Turn:  turn,
					Entry: console.NewNoticeEntry(l.now(), "No MCP servers configured or connected."),
				})
				return console.TurnDoneMsg{Turn: turn, Err: nil}
			}

			serverID, ok := awaitPickerChoice(l.prog, ctx, "Select an MCP Server:", toPickerRows(servers))
			if !ok {
				return console.TurnDoneMsg{Turn: turn, Err: nil} // dismissed
			}
			if serverID == "" {
				return console.TurnDoneMsg{Turn: turn, Err: nil}
			}

			for {
				actions := mcpActionChoices(serverID)
				actionID, ok := awaitPickerChoice(l.prog, ctx, fmt.Sprintf("MCP Server %q Actions:", serverID), toPickerRows(actions))
				if !ok {
					return console.TurnDoneMsg{Turn: turn, Err: nil}
				}
				if actionID == "" {
					// Esc dismisses the whole /mcp explorer — navigating back
					// on a dismissal would trap the operator in the loop.
					return console.TurnDoneMsg{Turn: turn, Err: nil}
				}
				if actionID == "back" {
					break
				}
				nw := &modelNoticeWriter{prog: l.prog, turn: turn, now: l.now}
				l.mcpAction(serverID, actionID, nw)
			}
		}
	}
}

// toPickerRows converts generic select options into picker rows.
func toPickerRows(opts []ui.SelectOption) []console.PickerItem {
	items := make([]console.PickerItem, 0, len(opts))
	for _, o := range opts {
		items = append(items, console.PickerItem{ID: o.ID, Label: o.Label, Detail: o.Detail, Disabled: false})
	}
	return items
}

// awaitPickerChoice installs a picker and blocks until the operator resolves
// it. Returns ("", false) when the wait is interrupted (cancelled turn);
// ("", true) on dismissal; (id, true) on selection.
func awaitPickerChoice(prog interface{ Send(tea.Msg) }, parent context.Context, title string, items []console.PickerItem) (string, bool) {
	// Refuse to install over an already-cancelled turn: a picker owned by a
	// retired turn must never own the keyboard.
	select {
	case <-parent.Done():
		return "", false
	default:
	}

	reply := make(chan string, 1)
	prog.Send(console.SetOverlayMsg{
		Overlay: console.NewModelPickerOverlay(title, items, reply),
	})
	select {
	case id := <-reply:
		if parent.Err() != nil {
			// A cancellation landed around the same moment as the selection.
			if prog != nil {
				prog.Send(console.SetOverlayMsg{Overlay: nil})
			}
			return "", false
		}
		if id == "" {
			return "", true // dismissed
		}
		return id, true
	case <-parent.Done():
		if prog != nil {
			prog.Send(console.SetOverlayMsg{Overlay: nil})
		}
		return "", false
	}
}

// cancel aborts exactly one turn. Cancelling an unknown or settled turn is a
// no-op, and never touches any other turn's context.
func (l *consoleLauncher) cancel(turn console.TurnID) {
	l.mu.Lock()
	cancel, ok := l.cancels[turn]
	l.mu.Unlock()
	if ok {
		cancel()
	}
}

// draining reports how many agent runs have not yet returned. The console
// watchdog freeing the UI is not proof that a run has exited.
func (l *consoleLauncher) draining() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inFlight
}

const defaultApprovalTimeout = 2 * time.Minute

// consoleConfirm bridges agent tool confirmation into Bubble Tea's overlay system.
// It never reads stdin: Bubble Tea holds the terminal in raw mode, so a synchronous prompt
// would corrupt the input loop and the display. Instead, it dispatches an ApprovalOverlay
// to the program's event loop and waits on a Go channel for the operator's decision.
func consoleConfirm(ctx context.Context, send func(tea.Msg), now func() time.Time) agent.ConfirmFunc {
	return consoleConfirmWithTimeout(ctx, send, now, defaultApprovalTimeout)
}

func consoleConfirmWithTimeout(ctx context.Context, send func(tea.Msg), now func() time.Time, timeout time.Duration) agent.ConfirmFunc {
	if now == nil {
		now = time.Now
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return func(toolName, description string, safetyScore int) agent.ConfirmResult {
		if send == nil {
			return agent.ConfirmNo
		}

		reply := make(chan agent.ConfirmResult, 1)
		overlay := console.NewApprovalOverlay(toolName, description, safetyScore, reply)
		send(console.SetOverlayMsg{Overlay: overlay})

		var result agent.ConfirmResult
		var reason string
		var decision console.Decision

		if timeout <= 0 {
			timeout = defaultApprovalTimeout
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()

		select {
		case res := <-reply:
			result = res
			switch res {
			case agent.ConfirmYes:
				decision = console.DecisionOnce
				reason = "approved once"
			case agent.ConfirmAlways:
				decision = console.DecisionSession
				reason = "auto-approved for session"
			case agent.ConfirmNever:
				decision = console.DecisionBlocked
				reason = "blocked for session"
			default:
				decision = console.DecisionDenied
				reason = "denied by operator"
			}
		case <-timer.C:
			// Dismiss overlay if timed out
			send(console.SetOverlayMsg{Overlay: nil})
			result = agent.ConfirmNo
			decision = console.DecisionDenied
			reason = "approval timed out (denied)"
		case <-ctx.Done():
			// Dismiss overlay if context canceled
			send(console.SetOverlayMsg{Overlay: nil})
			result = agent.ConfirmNo
			decision = console.DecisionDenied
			reason = "approval canceled (denied)"
		}

		send(console.EntryMsg{Entry: console.NewApprovalEntry(
			now(), toolName, "", safetyScore,
			reason,
			decision,
		)})

		return result
	}
}

// consoleSlashRunner adapts the existing REPL slash handler for the console.
// Output is captured rather than written to the terminal, and both the line
// reader and the selector refuse, so no slash command can read stdin out from
// under Bubble Tea.
func consoleSlashRunner(a *agent.Agent, mcpReg *mcpclient.Registry, target ModelChoice, optionalLoader ...func(context.Context) (*runtimectx.Context, error)) func(string) (string, error) {
	loader := loadAgentShellRuntime
	if len(optionalLoader) > 0 && optionalLoader[0] != nil {
		loader = optionalLoader[0]
	}
	return func(line string) (string, error) {
		var out strings.Builder
		session := &agentREPLSession{
			Agent:        a,
			MCPRegistry:  mcpReg,
			Runtime:      loader,
			Selector:     refusingSelector{},
			In:           refusingLineReader{},
			Out:          &out,
			ErrOut:       &out,
			ActiveTarget: target,
		}
		handled, _, err := handleREPLSlashCommand(session, line)
		if !handled && err == nil {
			return out.String(), fmt.Errorf("unknown command %q", line)
		}
		return out.String(), err
	}
}

// refusingLineReader satisfies LineReader without touching stdin.
type refusingLineReader struct{}

func (refusingLineReader) Readline() (string, error) { return "", io.EOF }

// refusingSelector satisfies ui.Selector without touching stdin.
type refusingSelector struct{}

func (refusingSelector) Select(context.Context, string, []ui.SelectOption) (ui.SelectResult, error) {
	return ui.SelectResult{}, errors.New("interactive selection is not available in the console")
}

// consoleProgramOptions records the console's Bubble Tea configuration in a
// form tests can assert on. tea.ProgramOption is an opaque function, so the
// intent is captured here and converted at the call site.
//
// Note on the alternate screen: bubbletea v1.3.10 has no WithoutAltScreen
// option. The main screen is the default and WithAltScreen opts in, so the
// console configures the main screen by never requesting the alternate one.
// That is load-bearing rather than cosmetic: tea.Println is a no-op while the
// alternate screen is active, and tea.Println is the only path by which
// transcript content reaches scrollback.
type consoleProgramOptions struct {
	// AltScreen must stay false. See above.
	AltScreen bool
	Context   context.Context
}

func consoleOptions(ctx context.Context) consoleProgramOptions {
	return consoleProgramOptions{AltScreen: false, Context: ctx}
}

// teaOptions converts the recorded intent into bubbletea options.
func (o consoleProgramOptions) teaOptions() []tea.ProgramOption {
	opts := []tea.ProgramOption{tea.WithContext(o.Context)}
	if o.AltScreen {
		opts = append(opts, tea.WithAltScreen())
	}
	return opts
}

// runAgentConsole starts the transcript console. It returns when the operator
// quits or the program errors, and always restores the terminal.
func runAgentConsole(
	ctx context.Context,
	a *agent.Agent,
	errW io.Writer,
	timeout time.Duration,
	historyPath string,
	mcpReg *mcpclient.Registry,
	target ModelChoice,
	optionalLoader ...func(context.Context) (*runtimectx.Context, error),
) error {
	loader := loadAgentShellRuntime
	if len(optionalLoader) > 0 && optionalLoader[0] != nil {
		loader = optionalLoader[0]
	}
	launcher := newConsoleLauncher(a.RunWithSinks, timeout, time.Now)
	launcher.slash = consoleSlashRunner(a, mcpReg, target, loader)
	if a != nil {
		launcher.safety = a.SafetyGate()
	}
	launcher.loader = loader
	launcher.activeTarget = target
	launcher.modelSwitch = consoleModelSwitch(a, loader, target)
	launcher.skillEffect = func(ctx context.Context, turn console.TurnID, command string, out io.Writer) error {
		if a == nil {
			return fmt.Errorf("agent is not available in this console")
		}
		ctx2, cancel := agentRequestContext(ctx, timeout)
		defer cancel()
		return a.RunWithSinks(ctx2, command, nil, out)
	}
	if mcpReg != nil {
		launcher.mcpRegistry = func() *mcpclient.Registry { return mcpReg }
		launcher.mcpAction = func(server, action string, out io.Writer) {
			sc := mcpReg.Get(server)
			if sc == nil {
				fmt.Fprintf(out, "Server %q is no longer connected.\n", server)
				return
			}
			switch action {
			case "tools":
				slashMCPListTools(out, sc)
			case "resources":
				slashMCPListResources(out, sc)
			case "diagnostics":
				slashMCPDiagnostics(out, sc)
			default:
				fmt.Fprintf(out, "Unknown MCP action %q.\n", action)
			}
		}
	}

	var initialHistory []string
	if a != nil && a.Conversation() != nil {
		for _, msg := range a.Conversation().Messages() {
			if msg.Role == chat.RoleUser && strings.TrimSpace(msg.Content) != "" {
				initialHistory = append(initialHistory, msg.Content)
			}
		}
	}
	// Persisted prompt history follows the conversation seed, in file
	// order. Consecutive duplicates collapse (a resumed conversation and
	// the file overlap; a cancelled queue restore that gets resubmitted
	// also re-lands), matching the editor ring's own dedupe at submit.
	seeded := append(initialHistory, loadConsoleHistory(consoleHistoryPath(), 100)...)
	initialHistory = initialHistory[:0]
	for _, h := range seeded {
		if len(initialHistory) > 0 && initialHistory[len(initialHistory)-1] == h {
			continue
		}
		initialHistory = append(initialHistory, h)
	}
	trimConsoleHistory(consoleHistoryPath(), 500)

	var lastFleetCheck time.Time
	var cachedFleet string = "unknown"
	var fleetMu sync.Mutex

	footer := console.NewStatusFooter(console.StatusFooterConfig{
		Model: func() string { return consoleFooterModel(a, target) },
		UsedTokens: func() int {
			if a != nil {
				return a.ContextTokens()
			}
			return 0
		},
		MaxTokens: func() int {
			if a != nil {
				return a.MaxTokens()
			}
			return 32768
		},
		Mode: func() string {
			if a != nil {
				return string(a.Autonomy())
			}
			return "default"
		},
		Fleet: func() string {
			fleetMu.Lock()
			defer fleetMu.Unlock()
			if !lastFleetCheck.IsZero() && time.Since(lastFleetCheck) < 5*time.Second {
				return cachedFleet
			}
			lastFleetCheck = time.Now()
			rctx, err := loader(ctx)
			if err == nil && rctx != nil && rctx.Snapshot != nil {
				s := rctx.Snapshot.Summary
				if s.TotalNodes > 0 {
					if s.ReachableNodes == s.TotalNodes {
						cachedFleet = fmt.Sprintf("%d/%d ok", s.ReachableNodes, s.TotalNodes)
					} else {
						cachedFleet = fmt.Sprintf("%d/%d ok (%d unreach)", s.ReachableNodes, s.TotalNodes, s.TotalNodes-s.ReachableNodes)
					}
				} else {
					cachedFleet = "local"
				}
			} else {
				cachedFleet = "unknown"
			}
			return cachedFleet
		},
	})

	promptHistoryPath := consoleHistoryPath()
	model := console.NewModel(console.Options{
		Submit:  launcher.submit(ctx),
		Cancel:  launcher.cancel,
		Footer:  footer,
		History: initialHistory,
		// Same source the footer's context gauge reads (a.ContextTokens).
		TokenEstimate: func() int {
			if a != nil {
				return a.ContextTokens()
			}
			return 0
		},
		// /usage prefers the backend-reported per-turn totals.
		UsageStats: func() (int, int, int) {
			if a == nil {
				return 0, 0, 0
			}
			return a.UsageStats()
		},
		HistorySink: func(text string) tea.Cmd {
			return appendConsoleHistory(promptHistoryPath, text)
		},
	})

	prog := tea.NewProgram(model, consoleOptions(ctx).teaOptions()...)
	launcher.prog = prog

	// Approvals fail closed. Installing this before the first turn guarantees
	// no code path can reach the agent's stdin prompt while tea owns the tty.
	a.SetConfirm(consoleConfirm(ctx, prog.Send, time.Now))

	// Restore the terminal whatever happens, including a panic in a view.
	defer prog.Kill()

	if _, err := prog.Run(); err != nil {
		return fmt.Errorf("console: %w", err)
	}

	if historyPath != "" {
		_ = saveAgentConversation(a.Conversation(), historyPath, errW)
	}
	return nil
}

// consoleHistoryPath is the persisted prompt-history file for the console.
// Entries are one JSON object per line: {"ts":RFC3339,"text":prompt}.
func consoleHistoryPath() string {
	return persist.AxisPath("history.jsonl")
}

// loadConsoleHistory reads the last max prompt-history entries in
// chronological order. A missing file is not an error; malformed lines are
// skipped so a hand-edited file cannot wedge the console.
func loadConsoleHistory(path string, max int) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil || entry.Text == "" {
			continue
		}
		out = append(out, entry.Text)
	}
	if len(out) > max {
		out = out[len(out)-max:]
	}
	// Consecutive duplicates collapse: a cancelled queue restore that gets
	// resubmitted, and an append-race double-write, must not make the recall
	// ring show the same prompt twice in a row.
	deduped := out[:0]
	for _, text := range out {
		if len(deduped) > 0 && deduped[len(deduped)-1] == text {
			continue
		}
		deduped = append(deduped, text)
	}
	return deduped
}

// trimConsoleHistory rewrites the history file keeping its last keep
// entries. Appends are never trimmed, so a long-lived console would grow
// the file without bound and slow every launch; this best-effort rewrite
// runs once per launch.
func trimConsoleHistory(path string, keep int) {
	entries := loadConsoleHistory(path, keep)
	if len(entries) == 0 {
		return
	}
	var buf bytes.Buffer
	for _, text := range entries {
		entry, err := json.Marshal(struct {
			Ts   string `json:"ts"`
			Text string `json:"text"`
		}{Ts: "", Text: text})
		if err != nil {
			return
		}
		buf.Write(append(entry, '\n'))
	}
	_ = persist.WritePrivateFileAtomic(path, buf.Bytes())
}

// appendConsoleHistory returns a tea.Cmd that appends one prompt to the
// history file, best effort: persistence failures must not disturb the
// session. The append runs off the event loop.
func appendConsoleHistory(path, text string) tea.Cmd {
	return func() tea.Msg {
		entry, err := json.Marshal(struct {
			Ts   string `json:"ts"`
			Text string `json:"text"`
		}{Ts: time.Now().UTC().Format(time.RFC3339), Text: text})
		if err != nil {
			return nil
		}
		// OpenPrivateFile creates missing parents with 0600/0700 so a
		// first-run AXIS_HOME is not world-readable.
		f, err := persist.OpenPrivateFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY)
		if err != nil {
			return nil
		}
		defer f.Close()
		f.Write(append(entry, '\n'))
		return nil
	}
}

// consoleTTY reports whether the console can own the terminal. Both streams
// must be a tty: tea reads stdin and writes stdout.
func consoleTTY() bool {
	return ui.StdinIsTerminal() && ui.StdoutIsTerminal()
}
