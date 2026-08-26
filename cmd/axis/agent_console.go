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
	"github.com/toasterbook88/axis/internal/ui"
)

// The transcript console is an experimental interactive surface for
// `axis agent`. It is opt-in behind --console because it cannot yet execute
// tools: Bubble Tea owns stdin in raw mode, so the agent's synchronous
// stdin approval prompt cannot run underneath it. Until an asynchronous
// approval overlay exists the console denies every confirmation rather than
// auto-approving or fighting the input reader for the terminal.
//
// Everything else — the plain REPL, single-shot prompts, non-TTY output — is
// untouched by this file.

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

// consoleConfirm fails closed. It never reads stdin: Bubble Tea holds the
// terminal in raw mode, so a synchronous prompt would corrupt the input loop
// and the display. Every attempt is reported to the operator so a denial is
// never silent, and nothing is ever auto-approved.
func consoleConfirm(send func(tea.Msg), now func() time.Time) agent.ConfirmFunc {
	if now == nil {
		now = time.Now
	}
	return func(toolName, description string, safetyScore int) agent.ConfirmResult {
		if send != nil {
			send(console.EntryMsg{Entry: console.NewApprovalEntry(
				now(), toolName, "", safetyScore,
				"denied: interactive approval is not implemented in the console yet",
				console.DecisionDenied,
			)})
		}
		return agent.ConfirmNo
	}
}

// consoleSlashRunner adapts the existing REPL slash handler for the console.
// Output is captured rather than written to the terminal, and both the line
// reader and the selector refuse, so no slash command can read stdin out from
// under Bubble Tea.
func consoleSlashRunner(a *agent.Agent, mcpReg *mcpclient.Registry, target ModelChoice) func(string) (string, error) {
	return func(line string) (string, error) {
		var out strings.Builder
		session := &agentREPLSession{
			Agent:        a,
			MCPRegistry:  mcpReg,
			Runtime:      loadAgentShellRuntime,
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
) error {
	launcher := newConsoleLauncher(a.RunWithSinks, timeout, time.Now)
	launcher.slash = consoleSlashRunner(a, mcpReg, target)

	model := console.NewModel(console.Options{
		Submit: launcher.submit(ctx),
		Cancel: launcher.cancel,
	})

	prog := tea.NewProgram(model, consoleOptions(ctx).teaOptions()...)
	launcher.prog = prog

	// Approvals fail closed. Installing this before the first turn guarantees
	// no code path can reach the agent's stdin prompt while tea owns the tty.
	a.SetConfirm(consoleConfirm(prog.Send, time.Now))

	fmt.Fprintf(errW, "%s transcript console (experimental): tool approvals are denied; use the plain REPL to execute tools\n",
		ui.Yellow("warning:"))

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
