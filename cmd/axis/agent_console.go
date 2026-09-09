package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/console"
	"github.com/toasterbook88/axis/internal/mcpclient"
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
		if strings.HasPrefix(prompt, "/") {
			return l.runSlash(turn, prompt)
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

	model := console.NewModel(console.Options{
		Submit: launcher.submit(ctx),
		Cancel: launcher.cancel,
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

// consoleTTY reports whether the console can own the terminal. Both streams
// must be a tty: tea reads stdin and writes stdout.
func consoleTTY() bool {
	return ui.StdinIsTerminal() && ui.StdoutIsTerminal()
}
