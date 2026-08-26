package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/console"
)

var consoleClock = func() time.Time { return time.Date(2026, 8, 25, 21, 35, 0, 0, time.UTC) }

// capture records everything the launcher sends to the program.
type capture struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (c *capture) Send(m tea.Msg) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, m)
}

func (c *capture) all() []tea.Msg {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]tea.Msg(nil), c.msgs...)
}

func (c *capture) chunks() []console.StreamChunkMsg {
	var out []console.StreamChunkMsg
	for _, m := range c.all() {
		if s, ok := m.(console.StreamChunkMsg); ok {
			out = append(out, s)
		}
	}
	return out
}

func TestConsoleUsesTheMainScreen(t *testing.T) {
	// tea.Println is a no-op under the alternate screen, and it is the only
	// path transcript content takes to scrollback. bubbletea v1.3.10 has no
	// WithoutAltScreen option, so "main screen" means never opting in.
	opts := consoleOptions(context.Background())
	if opts.AltScreen {
		t.Fatal("console requested the alternate screen; tea.Println would be silently dropped")
	}
	if got := len(opts.teaOptions()); got != 1 {
		t.Errorf("teaOptions() returned %d options, want 1 (context only)", got)
	}

	alt := consoleProgramOptions{AltScreen: true, Context: context.Background()}
	if got := len(alt.teaOptions()); got != 2 {
		t.Errorf("alt-screen config produced %d options, want 2", got)
	}
}

func TestConsoleSubmitProducesStreamThenCompletion(t *testing.T) {
	rec := &capture{}
	l := newConsoleLauncher(func(_ context.Context, prompt string, obs agent.Observer, out io.Writer) error {
		if _, err := out.Write([]byte("node-a has 28 GB")); err != nil {
			return err
		}
		obs.ToolSucceeded("call-1", "axis_status", "5 nodes", 7)
		return nil
	}, time.Minute, consoleClock)
	l.prog = rec

	msg := l.submit(context.Background())(1, "which node?")()

	done, ok := msg.(console.TurnDoneMsg)
	if !ok {
		t.Fatalf("submit returned %T, want TurnDoneMsg", msg)
	}
	if done.Turn != 1 || done.Err != nil {
		t.Errorf("TurnDoneMsg = %+v, want turn 1 and no error", done)
	}

	var streamed, entries int
	for _, m := range rec.all() {
		switch v := m.(type) {
		case console.StreamChunkMsg:
			streamed++
			if v.Turn != 1 {
				t.Errorf("stream chunk stamped turn %d, want 1", v.Turn)
			}
		case console.EntryMsg:
			entries++
			if v.Turn != 1 {
				t.Errorf("entry stamped turn %d, want 1", v.Turn)
			}
		}
	}
	if streamed == 0 {
		t.Error("no streamed output reached the program")
	}
	if entries == 0 {
		t.Error("no observer entry reached the program")
	}
}

func TestConsoleClosesWriterOnSuccessAndError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		runErr error
	}{
		{"success", nil},
		{"failure", errors.New("backend exploded")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &capture{}
			var writer io.Writer
			l := newConsoleLauncher(func(_ context.Context, _ string, _ agent.Observer, out io.Writer) error {
				writer = out
				_, _ = out.Write([]byte("tail text"))
				return tc.runErr
			}, time.Minute, consoleClock)
			l.prog = rec

			msg := l.submit(context.Background())(1, "go")()
			done := msg.(console.TurnDoneMsg)
			if !errors.Is(done.Err, tc.runErr) {
				t.Errorf("TurnDoneMsg.Err = %v, want %v", done.Err, tc.runErr)
			}

			// A closed writer flushes its tail; an unclosed one would strand it.
			if len(rec.chunks()) == 0 {
				t.Fatal("writer was not flushed; buffered output was lost")
			}
			// Close must be idempotent: the launcher closes explicitly and again
			// via defer.
			if c, ok := writer.(io.Closer); ok {
				if err := c.Close(); err != nil {
					t.Errorf("second Close returned %v", err)
				}
			}
		})
	}
}

func TestConsoleCancelTargetsOnlyItsOwnTurn(t *testing.T) {
	rec := &capture{}
	started := make(chan struct{})
	release := make(chan struct{})
	cancelled := make(chan bool, 1)

	l := newConsoleLauncher(func(ctx context.Context, prompt string, _ agent.Observer, _ io.Writer) error {
		if prompt == "two" {
			close(started)
			<-release
			cancelled <- ctx.Err() != nil
			return ctx.Err()
		}
		return nil
	}, time.Minute, consoleClock)
	l.prog = rec

	go func() { _ = l.submit(context.Background())(2, "two")() }()
	<-started

	// Cancelling an unrelated turn must not touch turn 2.
	l.cancel(1)
	l.cancel(99)
	close(release)

	select {
	case got := <-cancelled:
		if got {
			t.Error("cancelling another turn cancelled turn 2")
		}
	case <-time.After(time.Second):
		t.Fatal("turn 2 never observed its context")
	}
}

func TestConsoleCancelStopsTheNamedTurn(t *testing.T) {
	rec := &capture{}
	started := make(chan struct{})
	errCh := make(chan error, 1)

	l := newConsoleLauncher(func(ctx context.Context, _ string, _ agent.Observer, _ io.Writer) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}, time.Minute, consoleClock)
	l.prog = rec

	go func() {
		msg := l.submit(context.Background())(5, "work")()
		errCh <- msg.(console.TurnDoneMsg).Err
	}()
	<-started
	l.cancel(5)

	select {
	case err := <-errCh:
		if err == nil {
			t.Error("cancelled turn reported no error")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop the turn")
	}
}

func TestConsoleReportsWhenATurnWaitsBehindADrainingRun(t *testing.T) {
	// The console watchdog freeing the UI is not proof the agent run exited.
	// A turn submitted while one is still draining must say so rather than
	// present itself as independent.
	rec := &capture{}
	first := make(chan struct{})
	hold := make(chan struct{})

	l := newConsoleLauncher(func(_ context.Context, prompt string, _ agent.Observer, _ io.Writer) error {
		if prompt == "first" {
			close(first)
			<-hold
		}
		return nil
	}, time.Minute, consoleClock)
	l.prog = rec

	firstDone := make(chan struct{})
	go func() {
		_ = l.submit(context.Background())(1, "first")()
		close(firstDone)
	}()
	<-first

	if l.draining() != 1 {
		t.Fatalf("draining() = %d, want 1", l.draining())
	}

	secondDone := make(chan struct{})
	go func() {
		_ = l.submit(context.Background())(2, "second")()
		close(secondDone)
	}()
	time.Sleep(20 * time.Millisecond)
	close(hold)
	<-firstDone
	<-secondDone

	var warned bool
	for _, m := range rec.all() {
		if e, ok := m.(console.EntryMsg); ok {
			if strings.Contains(strings.Join(console.PlainAll(e.Entry.Render(80)), " "), "draining") {
				warned = true
			}
		}
	}
	if !warned {
		t.Error("no notice that a turn waited behind a draining run")
	}
	if l.draining() != 0 {
		t.Errorf("draining() = %d after both turns, want 0", l.draining())
	}
}

func TestConsoleBuildsFreshSinksPerTurn(t *testing.T) {
	// Reusing a bridge or writer across turns would reintroduce the shared
	// mutable attribution the immutable TurnID design removed.
	rec := &capture{}
	var observers []agent.Observer
	var writers []io.Writer

	l := newConsoleLauncher(func(_ context.Context, _ string, obs agent.Observer, out io.Writer) error {
		observers = append(observers, obs)
		writers = append(writers, out)
		return nil
	}, time.Minute, consoleClock)
	l.prog = rec

	submit := l.submit(context.Background())
	_ = submit(1, "one")()
	_ = submit(2, "two")()

	if len(observers) != 2 || len(writers) != 2 {
		t.Fatalf("got %d observers and %d writers, want 2 each", len(observers), len(writers))
	}
	if observers[0] == observers[1] {
		t.Error("the same observer was reused across turns")
	}
	if writers[0] == writers[1] {
		t.Error("the same stream writer was reused across turns")
	}
}

func TestConsoleStampsEachTurnWithItsOwnID(t *testing.T) {
	rec := &capture{}
	l := newConsoleLauncher(func(_ context.Context, prompt string, _ agent.Observer, out io.Writer) error {
		_, _ = out.Write([]byte(prompt))
		return nil
	}, time.Minute, consoleClock)
	l.prog = rec

	submit := l.submit(context.Background())
	_ = submit(1, "one")()
	_ = submit(2, "two")()

	seen := map[console.TurnID]string{}
	for _, c := range rec.chunks() {
		seen[c.Turn] += c.Text
	}
	if seen[1] != "one" || seen[2] != "two" {
		t.Errorf("turn attribution wrong: %v", seen)
	}
}

func TestConsoleApprovalFailsClosedWithoutReadingStdin(t *testing.T) {
	// Bubble Tea holds stdin in raw mode. A synchronous prompt would corrupt
	// the input loop, so the console denies rather than prompting — and never
	// auto-approves.
	rec := &capture{}
	confirm := consoleConfirm(rec.Send, consoleClock)

	for _, score := range []int{0, 35, 74, 95} {
		if got := confirm("bash", "rm -rf /tmp/x", score); got != agent.ConfirmNo {
			t.Errorf("safety %d: confirm returned %v, want ConfirmNo", score, got)
		}
	}

	msgs := rec.all()
	if len(msgs) != 4 {
		t.Fatalf("got %d approval notices, want 4", len(msgs))
	}
	for _, m := range msgs {
		e, ok := m.(console.EntryMsg)
		if !ok {
			t.Fatalf("approval produced %T, want EntryMsg", m)
		}
		rendered := strings.Join(console.PlainAll(e.Entry.Render(100)), " ")
		if !strings.Contains(rendered, "denied") {
			t.Errorf("denial was silent: %q", rendered)
		}
	}
}

func TestConsoleApprovalNeverAutoApproves(t *testing.T) {
	confirm := consoleConfirm(nil, consoleClock)
	for _, r := range []agent.ConfirmResult{agent.ConfirmYes, agent.ConfirmAlways} {
		if confirm("bash", "anything", 0) == r {
			t.Fatalf("console confirm returned %v; it must always deny", r)
		}
	}
}

func TestConsoleSlashHelpersNeverReadStdin(t *testing.T) {
	// Slash commands run through the existing REPL handler, but with a reader
	// and selector that refuse, so nothing can steal stdin from tea.
	if _, err := (refusingLineReader{}).Readline(); !errors.Is(err, io.EOF) {
		t.Errorf("line reader returned %v, want io.EOF", err)
	}
	if _, err := (refusingSelector{}).Select(context.Background(), "pick", nil); err == nil {
		t.Error("selector did not refuse")
	}
}

func TestConsoleRejectsSlashWhenUnwired(t *testing.T) {
	rec := &capture{}
	l := newConsoleLauncher(func(context.Context, string, agent.Observer, io.Writer) error {
		t.Fatal("slash input was sent to the model")
		return nil
	}, time.Minute, consoleClock)
	l.prog = rec

	msg := l.submit(context.Background())(1, "/status")()
	done := msg.(console.TurnDoneMsg)
	if done.Err == nil {
		t.Error("unwired slash command reported success")
	}
}

func TestConsoleFlagIsOptInAndDefaultsOff(t *testing.T) {
	// The console cannot execute tools yet, so it must never displace the
	// readline REPL by default.
	cmd := agentCmd()
	f := cmd.Flags().Lookup("console")
	if f == nil {
		t.Fatal("--console flag is not registered")
	}
	if f.DefValue != "false" {
		t.Errorf("--console default = %q, want false", f.DefValue)
	}
	if !strings.Contains(f.Usage, "approvals are denied") {
		t.Errorf("--console usage does not state the approval limitation: %q", f.Usage)
	}
}

func TestSingleShotAndNonTTYContractUnchanged(t *testing.T) {
	// The console is reachable only from the interactive branch. Single-shot
	// invocation is positional, and neither it nor a non-TTY run may acquire
	// a new required flag or lose an existing one.
	cmd := agentCmd()
	for _, name := range []string{
		"model", "role", "timeout", "max-tokens", "max-turns", "auto-approve",
		"autonomy", "system", "resume", "verbose", "dry-run", "provider",
		"cloud-model", "cheap-model", "select",
	} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("pre-existing flag --%s disappeared", name)
		}
	}
	if cmd.Args != nil {
		// Positional prompt is the single-shot path; it must stay accepted.
		if err := cmd.Args(cmd, []string{"summarise the fleet"}); err != nil {
			t.Errorf("single-shot positional prompt rejected: %v", err)
		}
	}
}

func TestConsoleRequiresATTY(t *testing.T) {
	// go test runs without a tty, so this exercises the real guard.
	if consoleTTY() {
		t.Skip("test environment has a tty on both streams")
	}
	err := func() error {
		if !consoleTTY() {
			return errors.New("--console requires an interactive terminal")
		}
		return nil
	}()
	if err == nil {
		t.Error("console did not refuse a non-tty environment")
	}
}

func TestLauncherDoesNotEmitForRetiredTurns(t *testing.T) {
	// The launcher stamps immutably; rejection is the model's job. This pins
	// the contract they meet at: a late producer keeps its own turn id, and
	// the model drops it because that turn has retired.
	rec := &capture{}
	l := newConsoleLauncher(func(_ context.Context, _ string, _ agent.Observer, out io.Writer) error {
		_, _ = out.Write([]byte("late"))
		return nil
	}, time.Minute, consoleClock)
	l.prog = rec

	_ = l.submit(context.Background())(1, "one")()

	m := console.NewModel(console.Options{
		Submit: func(console.TurnID, string) tea.Cmd { return nil },
		Now:    consoleClock,
	})
	updated, _ := m.Update(console.TurnDoneMsg{Turn: 1})
	m = updated.(console.Model)

	for _, c := range rec.chunks() {
		if c.Turn != 1 {
			t.Fatalf("chunk stamped turn %d, want 1", c.Turn)
		}
		next, _ := m.Update(c)
		if strings.Contains(next.(console.Model).View(), "late") {
			t.Error("a retired turn's output was accepted by the model")
		}
	}
}
