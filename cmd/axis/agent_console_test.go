package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/console"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
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
		obs.ToolSucceeded("call-1", "axis_status", "5 nodes", 7, 9*time.Millisecond)
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
	// the input loop, so the console uses an overlay and times out / fails closed
	// if no operator response is received.
	rec := &capture{}
	confirm := consoleConfirmWithTimeout(context.Background(), rec.Send, consoleClock, 10*time.Millisecond)

	for _, score := range []int{0, 35, 74, 95} {
		if got := confirm("bash", "rm -rf /tmp/x", score); got != agent.ConfirmNo {
			t.Errorf("safety %d: confirm returned %v, want ConfirmNo", score, got)
		}
	}

	msgs := rec.all()
	// Each approval produces a SetOverlayMsg, an unregister SetOverlayMsg on timeout, and an EntryMsg
	var entries []console.EntryMsg
	for _, m := range msgs {
		if e, ok := m.(console.EntryMsg); ok {
			entries = append(entries, e)
		}
	}
	if len(entries) != 4 {
		t.Fatalf("got %d approval notices, want 4", len(entries))
	}
	for _, e := range entries {
		rendered := strings.Join(console.PlainAll(e.Entry.Render(100)), " ")
		if !strings.Contains(rendered, "denied") {
			t.Errorf("denial was silent: %q", rendered)
		}
	}
}

func TestConsoleApprovalInteractiveDecisions(t *testing.T) {
	rec := &capture{}

	// Approving 'y' delivers ConfirmYes
	confirmApprove := consoleConfirmWithTimeout(context.Background(), func(m tea.Msg) {
		rec.Send(m)
		if som, ok := m.(console.SetOverlayMsg); ok && som.Overlay != nil {
			som.Overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
		}
	}, consoleClock, time.Second)

	if got := confirmApprove("shell", "ls", 10); got != agent.ConfirmYes {
		t.Fatalf("expected ConfirmYes, got %v", got)
	}

	// Denying 'n' delivers ConfirmNo
	confirmDeny := consoleConfirmWithTimeout(context.Background(), func(m tea.Msg) {
		rec.Send(m)
		if som, ok := m.(console.SetOverlayMsg); ok && som.Overlay != nil {
			som.Overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
		}
	}, consoleClock, time.Second)

	if got := confirmDeny("shell", "rm -rf /", 90); got != agent.ConfirmNo {
		t.Fatalf("expected ConfirmNo, got %v", got)
	}

	// Approving 'a' delivers ConfirmAlways
	confirmAlways := consoleConfirmWithTimeout(context.Background(), func(m tea.Msg) {
		rec.Send(m)
		if som, ok := m.(console.SetOverlayMsg); ok && som.Overlay != nil {
			som.Overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
		}
	}, consoleClock, time.Second)

	if got := confirmAlways("shell", "status", 15); got != agent.ConfirmAlways {
		t.Fatalf("expected ConfirmAlways, got %v", got)
	}
}

func TestConsoleApprovalContextCancelReturnsDeny(t *testing.T) {
	rec := &capture{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-canceled

	confirm := consoleConfirmWithTimeout(ctx, rec.Send, consoleClock, time.Minute)
	got := confirm("bash", "rm -rf /tmp/x", 75)
	if got != agent.ConfirmNo {
		t.Fatalf("expected ConfirmNo on canceled context, got %v", got)
	}

	msgs := rec.all()
	var dismissed bool
	var entry *console.EntryMsg
	for _, m := range msgs {
		if som, ok := m.(console.SetOverlayMsg); ok && som.Overlay == nil {
			dismissed = true
		}
		if em, ok := m.(console.EntryMsg); ok {
			entry = &em
		}
	}
	if !dismissed {
		t.Error("expected overlay to be dismissed on context cancel")
	}
	if entry == nil {
		t.Fatal("expected entry to be logged on context cancel")
	}
	rendered := strings.Join(console.PlainAll(entry.Entry.Render(100)), " ")
	if !strings.Contains(rendered, "denied") && !strings.Contains(rendered, "canceled") {
		t.Errorf("entry did not mention denial or cancellation: %q", rendered)
	}
}

func TestConsoleApprovalNeverAutoApproves(t *testing.T) {
	confirm := consoleConfirm(context.Background(), nil, consoleClock)
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

func TestConsoleAndPlainFlagContract(t *testing.T) {
	// Track 4 contract: the console is the default interactive surface on an
	// interactive terminal. --console is retained as a force alias with its
	// deprecation marked in the help, and --plain downgrades to the legacy
	// REPL; both flags default off so scripted behavior is explicit.
	cmd := agentCmd()
	f := cmd.Flags().Lookup("console")
	if f == nil {
		t.Fatal("--console flag is not registered")
	}
	if f.DefValue != "false" {
		t.Errorf("--console default = %q, want false", f.DefValue)
	}
	if !strings.Contains(f.Usage, "deprecated") {
		t.Errorf("--console usage does not mark the deprecation: %q", f.Usage)
	}
	p := cmd.Flags().Lookup("plain")
	if p == nil {
		t.Fatal("--plain flag is not registered")
	}
	if p.DefValue != "false" {
		t.Errorf("--plain default = %q, want false", p.DefValue)
	}
	if !strings.Contains(p.Usage, "legacy") {
		t.Errorf("--plain usage does not state the legacy REPL: %q", p.Usage)
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

func TestConsoleShellEscapeShortcut(t *testing.T) {
	rec := &capture{}
	l := newConsoleLauncher(nil, time.Minute, consoleClock)
	l.prog = rec

	cmd := l.submit(context.Background())(1, "!echo hello-from-shell")
	msg := cmd()

	done, ok := msg.(console.TurnDoneMsg)
	if !ok || done.Turn != 1 || done.Err != nil {
		t.Fatalf("unexpected turn done message: %+v", msg)
	}

	msgs := rec.all()
	var found bool
	for _, m := range msgs {
		if em, ok := m.(console.EntryMsg); ok {
			rendered := strings.Join(console.PlainAll(em.Entry.Render(100)), " ")
			if strings.Contains(rendered, "hello-from-shell") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("shell output not captured in console entries: %+v", msgs)
	}
}

func TestConsoleHelpShortcut(t *testing.T) {
	rec := &capture{}
	l := newConsoleLauncher(nil, time.Minute, consoleClock)
	l.prog = rec
	l.slash = func(cmd string) (string, error) {
		if cmd == "/help" {
			return "mock help text", nil
		}
		return "", errors.New("unexpected command")
	}

	cmd := l.submit(context.Background())(1, "?")
	msg := cmd()

	done, ok := msg.(console.TurnDoneMsg)
	if !ok || done.Turn != 1 || done.Err != nil {
		t.Fatalf("unexpected turn done message: %+v", msg)
	}

	msgs := rec.all()
	var found bool
	for _, m := range msgs {
		if em, ok := m.(console.EntryMsg); ok {
			rendered := strings.Join(console.PlainAll(em.Entry.Render(100)), " ")
			if strings.Contains(rendered, "mock help text") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("help output not captured in console entries: %+v", msgs)
	}
}

func TestConsoleShellEscapeCancel(t *testing.T) {
	rec := &capture{}
	l := newConsoleLauncher(nil, time.Minute, consoleClock)
	l.prog = rec

	ready := filepath.Join(t.TempDir(), "shell-escape-ready")
	cmd := l.submit(context.Background())(1, "!touch "+ready+" && sleep 30")
	doneCh := make(chan tea.Msg, 1)
	go func() {
		doneCh <- cmd()
	}()

	// Poll for the marker file instead of sleeping a fixed interval: the
	// cancel below must land on a running subprocess, and polling makes
	// readiness deterministic on slow or loaded machines.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shell escape never signalled readiness")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Cancel the turn
	l.cancel(1)

	select {
	case msg := <-doneCh:
		done, ok := msg.(console.TurnDoneMsg)
		if !ok || done.Turn != 1 {
			t.Fatalf("unexpected turn done message: %+v", msg)
		}
		if done.Err == nil {
			t.Fatal("cancelled shell escape should report the kill as an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shell command was not cancelled within timeout")
	}
}

func TestConsoleShellEscapeOutputCapped(t *testing.T) {
	rec := &capture{}
	l := newConsoleLauncher(nil, time.Minute, consoleClock)
	l.prog = rec

	// seq emits well over 1MB of output; the console must cap it like every
	// other shell execution surface instead of flooding the transcript.
	cmd := l.submit(context.Background())(1, "!seq 1 200000")
	msg := cmd()

	done, ok := msg.(console.TurnDoneMsg)
	if !ok || done.Turn != 1 || done.Err != nil {
		t.Fatalf("unexpected turn done message: %+v", msg)
	}

	var rendered string
	for _, m := range rec.all() {
		if em, ok := m.(console.EntryMsg); ok {
			rendered += strings.Join(console.PlainAll(em.Entry.Render(100)), "\n")
		}
	}
	if !strings.Contains(rendered, "[truncated to") {
		t.Fatalf("expected truncation marker in capped shell output, got %d bytes", len(rendered))
	}
	// The cap is rune-based (agent.MaxShellOutputRunes); wrapping can expand
	// short lines several-fold, so bound the flood generously while keeping it
	// far below the ~1.4MB seq would otherwise print into the transcript.
	if len(rendered) > 4*agent.MaxShellOutputRunes {
		t.Fatalf("rendered shell output %d bytes exceeds the %d rune cap", len(rendered), 4*agent.MaxShellOutputRunes)
	}
	if strings.Contains(rendered, "\n200000") {
		t.Fatal("uncapped tail of seq output reached the transcript")
	}
}

func TestConsoleShellEscapeEmpty(t *testing.T) {
	rec := &capture{}
	l := newConsoleLauncher(nil, time.Minute, consoleClock)
	l.prog = rec

	cmd := l.submit(context.Background())(1, "!   ")
	msg := cmd()

	done, ok := msg.(console.TurnDoneMsg)
	if !ok || done.Turn != 1 || done.Err == nil {
		t.Fatalf("expected error for empty shell command, got: %+v", msg)
	}
	if len(rec.all()) != 0 {
		t.Fatalf("empty shell command should not send manual EntryMsg, got: %+v", rec.all())
	}
}

func TestConsoleFleetThrottleOnFailure(t *testing.T) {
	calls := 0
	loader := func(ctx context.Context) (*runtimectx.Context, error) {
		calls++
		return nil, errors.New("daemon offline")
	}

	var lastFleetCheck time.Time
	var cachedFleet string = "unknown"
	var fleetMu sync.Mutex

	fleetFn := func() string {
		fleetMu.Lock()
		defer fleetMu.Unlock()
		if !lastFleetCheck.IsZero() && time.Since(lastFleetCheck) < 5*time.Second {
			return cachedFleet
		}
		lastFleetCheck = time.Now()
		rctx, err := loader(context.Background())
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
	}

	// Call 10 times in a tight loop
	for i := 0; i < 10; i++ {
		res := fleetFn()
		if res != "unknown" {
			t.Fatalf("call %d returned %q, want 'unknown'", i, res)
		}
	}

	if calls != 1 {
		t.Fatalf("loader called %d times, want exactly 1 (throttled)", calls)
	}
}

func TestConsoleShellEscapeSafetyBlocked(t *testing.T) {
	rec := &capture{}
	l := newConsoleLauncher(nil, time.Minute, consoleClock)
	l.prog = rec

	// Test default safety gate blocking destructive command
	cmd := l.submit(context.Background())(1, "!rm -rf /")
	msg := cmd()

	done, ok := msg.(console.TurnDoneMsg)
	if !ok || done.Turn != 1 || done.Err == nil {
		t.Fatalf("expected safety block error for '!rm -rf /', got: %+v", msg)
	}
	if !strings.Contains(done.Err.Error(), "blocked by safety check") {
		t.Fatalf("error should cite safety gate block, got: %v", done.Err)
	}

	// Test custom safety gate
	customCalled := false
	l.safety = func(command string) (bool, string, int) {
		customCalled = true
		return false, "policy violation", 95
	}

	cmd2 := l.submit(context.Background())(2, "!echo test")
	msg2 := cmd2()

	if !customCalled {
		t.Fatal("custom safety gate was not called")
	}
	done2, ok := msg2.(console.TurnDoneMsg)
	if !ok || done2.Turn != 2 || done2.Err == nil {
		t.Fatalf("expected error from custom safety gate, got: %+v", msg2)
	}
	if !strings.Contains(done2.Err.Error(), "policy violation") {
		t.Fatalf("error should cite custom reason, got: %v", done2.Err)
	}
}

func TestConsoleShellEscapeNonZeroExit(t *testing.T) {
	rec := &capture{}
	l := newConsoleLauncher(nil, time.Minute, consoleClock)
	l.prog = rec

	// Run command that exits non-zero
	cmd := l.submit(context.Background())(1, "!sh -c 'exit 42'")
	msg := cmd()

	done, ok := msg.(console.TurnDoneMsg)
	if !ok || done.Turn != 1 {
		t.Fatalf("unexpected turn done message structure: %+v", msg)
	}
	if done.Err == nil {
		t.Fatal("expected non-zero exit to report non-nil TurnDoneMsg.Err")
	}

	// Verify an error entry was also sent to the console log
	msgs := rec.all()
	var foundError bool
	for _, m := range msgs {
		if em, ok := m.(console.EntryMsg); ok {
			if _, isErr := em.Entry.(*console.ErrorEntry); isErr {
				rendered := strings.Join(console.PlainAll(em.Entry.Render(100)), " ")
				if strings.Contains(rendered, "command exited with error") {
					foundError = true
					break
				}
			}
		}
	}
	if !foundError {
		t.Fatalf("expected error entry rendered to console, got: %+v", msgs)
	}
}

// pickerTestSetup stubs the catalog probes so collectModelChoices runs
// offline; it returns the hostname needed for local resident snapshots.
//
// The stubs mutate package-level vars (probeEndpointFn, inferenceAILoadFn)
// and restore them on cleanup. That is only safe while cmd/axis runs no
// t.Parallel() tests — parallel subtests would race on those vars. If a
// parallel test is ever added, convert these to per-test seams first.
func pickerTestSetup(t *testing.T) string {
	hn, err := os.Hostname()
	if err != nil || hn == "" {
		t.Skip("hostname unavailable")
	}
	prevProbe := probeEndpointFn
	prevLoad := inferenceAILoadFn
	t.Cleanup(func() {
		probeEndpointFn = prevProbe
		inferenceAILoadFn = prevLoad
	})
	probeEndpointFn = func(string) bool { return true }
	inferenceAILoadFn = func(string) (*config.AIConfig, error) { return &config.AIConfig{}, nil }
	return hn
}

func pickerTestRuntime(hn string) *runtimectx.Context {
	return &runtimectx.Context{
		Snapshot: &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{{
				Name:     "local-node",
				Hostname: hn,
				Status:   models.StatusComplete,
				ResidentModels: []models.ResidentModel{
					{Name: "qwen3.8-27b", Runtime: "llama.cpp", Port: 8082},
				},
			}},
		},
		Config: &config.Config{},
	}
}

func awaitPickerOverlay(t *testing.T, rec *capture) *console.ModelPickerOverlay {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, m := range rec.all() {
			if som, ok := m.(console.SetOverlayMsg); ok {
				if po, ok := som.Overlay.(*console.ModelPickerOverlay); ok {
					return po
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("model picker overlay was never installed")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestConsoleModelPickerInstallsOverlayAndSwitches(t *testing.T) {
	hn := pickerTestSetup(t)
	a := agent.New(agent.Config{Endpoint: "http://localhost:11434", Model: "granite3.1-moe:1b", MaxTokens: 4096})
	rt := pickerTestRuntime(hn)

	rec := &capture{}
	l := newConsoleLauncher(nil, time.Minute, consoleClock)
	l.prog = rec
	l.loader = func(context.Context) (*runtimectx.Context, error) { return rt, nil }
	l.modelSwitch = consoleModelSwitch(a, l.loader, ModelChoice{Model: "granite3.1-moe:1b"})

	doneCh := make(chan tea.Msg, 1)
	go func() {
		doneCh <- l.submit(context.Background())(1, "/model")()
	}()

	picker := awaitPickerOverlay(t, rec)
	picker.Update(tea.KeyMsg{Type: tea.KeyEnter})

	select {
	case msg := <-doneCh:
		done, ok := msg.(console.TurnDoneMsg)
		if !ok || done.Turn != 1 || done.Err != nil {
			t.Fatalf("unexpected turn done message: %+v", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("picker selection did not resolve the turn")
	}

	if a.Model() != "qwen3.8-27b" {
		t.Fatalf("model not switched, got %q", a.Model())
	}
	found := false
	for _, m := range rec.all() {
		if em, ok := m.(console.EntryMsg); ok {
			rendered := strings.Join(console.PlainAll(em.Entry.Render(100)), " ")
			if strings.Contains(rendered, "Switched to") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("switch status line not captured in transcript")
	}
}

func TestConsoleModelPickerCancelKeepsModel(t *testing.T) {
	hn := pickerTestSetup(t)
	a := agent.New(agent.Config{Endpoint: "http://localhost:11434", Model: "granite3.1-moe:1b", MaxTokens: 4096})
	rt := pickerTestRuntime(hn)

	rec := &capture{}
	l := newConsoleLauncher(nil, time.Minute, consoleClock)
	l.prog = rec
	l.loader = func(context.Context) (*runtimectx.Context, error) { return rt, nil }
	l.modelSwitch = consoleModelSwitch(a, l.loader, ModelChoice{Model: "granite3.1-moe:1b"})

	doneCh := make(chan tea.Msg, 1)
	go func() {
		doneCh <- l.submit(context.Background())(1, "/model")()
	}()

	picker := awaitPickerOverlay(t, rec)
	picker.Update(tea.KeyMsg{Type: tea.KeyEsc})

	select {
	case msg := <-doneCh:
		done, ok := msg.(console.TurnDoneMsg)
		if !ok || done.Turn != 1 || done.Err != nil {
			t.Fatalf("cancel must yield a clean turn, got %+v", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("picker dismissal did not resolve the turn")
	}
	if a.Model() != "granite3.1-moe:1b" {
		t.Fatalf("cancel must not switch models, got %q", a.Model())
	}
	for _, m := range rec.all() {
		if em, ok := m.(console.EntryMsg); ok {
			rendered := strings.Join(console.PlainAll(em.Entry.Render(100)), " ")
			if strings.Contains(rendered, "Switched to") {
				t.Fatal("cancel must not emit a switch notice")
			}
		}
	}
}

func TestConsoleModelPickerWithArgsUnchanged(t *testing.T) {
	rec := &capture{}
	l := newConsoleLauncher(nil, time.Minute, consoleClock)
	l.prog = rec
	l.slash = func(cmd string) (string, error) {
		if cmd == "/model some-name" {
			return "mock-slash-out", nil
		}
		return "", errors.New("unexpected command")
	}

	msg := l.submit(context.Background())(1, "/model some-name")()

	done, ok := msg.(console.TurnDoneMsg)
	if !ok || done.Turn != 1 || done.Err != nil {
		t.Fatalf("unexpected turn done message: %+v", msg)
	}
	for _, m := range rec.all() {
		if _, isOverlay := m.(console.SetOverlayMsg); isOverlay {
			t.Fatal("arg-less interception must not trigger for /model with args")
		}
		if em, ok := m.(console.EntryMsg); ok {
			if strings.Contains(strings.Join(console.PlainAll(em.Entry.Render(100)), " "), "mock-slash-out") {
				return
			}
		}
	}
	t.Fatal("slash output not captured")
}

func TestConsoleModelPickerNoChoices(t *testing.T) {
	rec := &capture{}
	l := newConsoleLauncher(nil, time.Minute, consoleClock)
	l.prog = rec
	l.loader = func(context.Context) (*runtimectx.Context, error) {
		return &runtimectx.Context{Snapshot: &models.ClusterSnapshot{}, Config: &config.Config{}}, nil
	}
	prevLoad := inferenceAILoadFn
	t.Cleanup(func() { inferenceAILoadFn = prevLoad })
	inferenceAILoadFn = func(string) (*config.AIConfig, error) { return &config.AIConfig{}, nil }

	msg := l.submit(context.Background())(1, "/model")()

	done, ok := msg.(console.TurnDoneMsg)
	if !ok || done.Turn != 1 || done.Err != nil {
		t.Fatalf("unexpected turn done message: %+v", msg)
	}
	found := false
	for _, m := range rec.all() {
		if _, isOverlay := m.(console.SetOverlayMsg); isOverlay {
			t.Fatal("no overlay expected when the catalog is empty")
		}
		if em, ok := m.(console.EntryMsg); ok {
			if strings.Contains(strings.Join(console.PlainAll(em.Entry.Render(100)), " "), "No models found") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("expected the no-models notice in the transcript")
	}
}

func TestConsoleFooterModelFollowsAgent(t *testing.T) {
	a := agent.New(agent.Config{Endpoint: "http://localhost:11434", Model: "startup-model", MaxTokens: 4096})
	if got := consoleFooterModel(a, ModelChoice{Model: "target-model"}); got != "startup-model" {
		t.Fatalf("live agent model should win, got %q", got)
	}
	a.SetModel("switched-model")
	if got := consoleFooterModel(a, ModelChoice{Model: "target-model"}); got != "switched-model" {
		t.Fatalf("footer must track switches, got %q", got)
	}
	if got := consoleFooterModel(nil, ModelChoice{Model: "target-model"}); got != "target-model" {
		t.Fatalf("nil agent must fall back to target, got %q", got)
	}
}

func TestConsoleModelPickerCancelDuringCatalogLoad(t *testing.T) {
	// Regression for the review's critical turn-invariant finding: the
	// picker turn must register its cancel like every other turn, so an Esc
	// during the keyboard-free catalog window retires the turn cleanly and
	// the picker never installs over it.
	hn := pickerTestSetup(t)
	a := agent.New(agent.Config{Endpoint: "http://localhost:11434", Model: "granite3.1-moe:1b", MaxTokens: 4096})
	rt := pickerTestRuntime(hn)

	release := make(chan struct{})
	rec := &capture{}
	l := newConsoleLauncher(nil, time.Minute, consoleClock)
	l.prog = rec
	l.loader = func(context.Context) (*runtimectx.Context, error) {
		<-release
		return rt, nil
	}
	l.modelSwitch = consoleModelSwitch(a, l.loader, ModelChoice{Model: "granite3.1-moe:1b"})

	doneCh := make(chan tea.Msg, 1)
	go func() {
		doneCh <- l.submit(context.Background())(1, "/model")()
	}()

	time.Sleep(20 * time.Millisecond) // the Cmd is parked inside the loader
	l.cancel(1)                       // what requestCancel routes to on Esc
	close(release)

	select {
	case msg := <-doneCh:
		done, ok := msg.(console.TurnDoneMsg)
		if !ok || done.Turn != 1 || done.Err != nil {
			t.Fatalf("cancelled picker must end as a clean turn, got %+v", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled picker never resolved")
	}
	for _, m := range rec.all() {
		if som, ok := m.(console.SetOverlayMsg); ok && som.Overlay != nil {
			if _, isPicker := som.Overlay.(*console.ModelPickerOverlay); isPicker {
				t.Fatal("cancelled picker turn must not install an overlay")
			}
		}
	}
	if a.Model() != "granite3.1-moe:1b" {
		t.Fatalf("cancel must not switch models, got %q", a.Model())
	}
}

func TestConsoleModelPickerLoaderError(t *testing.T) {
	rec := &capture{}
	l := newConsoleLauncher(nil, time.Minute, consoleClock)
	l.prog = rec
	l.loader = func(context.Context) (*runtimectx.Context, error) {
		return nil, errors.New("daemon offline")
	}

	msg := l.submit(context.Background())(1, "/model")()

	done, ok := msg.(console.TurnDoneMsg)
	if !ok || done.Turn != 1 {
		t.Fatalf("unexpected turn done message: %+v", msg)
	}
	if done.Err == nil || !strings.Contains(done.Err.Error(), "model catalog unavailable") || !strings.Contains(done.Err.Error(), "daemon offline") {
		t.Fatalf("expected the wrapped loader error, got %v", done.Err)
	}
	for _, m := range rec.all() {
		if _, isOverlay := m.(console.SetOverlayMsg); isOverlay {
			t.Fatal("no overlay expected when the loader fails")
		}
	}
}

func TestConsoleModelPickerLoaderNilContext(t *testing.T) {
	rec := &capture{}
	l := newConsoleLauncher(nil, time.Minute, consoleClock)
	l.prog = rec
	l.loader = func(context.Context) (*runtimectx.Context, error) { return nil, nil }

	msg := l.submit(context.Background())(1, "/model")()

	done, ok := msg.(console.TurnDoneMsg)
	if !ok || done.Turn != 1 {
		t.Fatalf("unexpected turn done message: %+v", msg)
	}
	if done.Err == nil || !strings.Contains(done.Err.Error(), "runtime loader returned no context") {
		t.Fatalf("expected the nil-context loader error, got %v", done.Err)
	}
}

func TestLoadConsoleHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	var buf bytes.Buffer
	for i := 0; i < 5; i++ {
		entry, _ := json.Marshal(struct {
			Ts   string `json:"ts"`
			Text string `json:"text"`
		}{Ts: fmt.Sprintf("2026-09-10T00:0%d:00Z", i), Text: fmt.Sprintf("prompt %d", i)})
		buf.Write(append(entry, '\n'))
	}
	buf.WriteString("not json\n\n")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	got := loadConsoleHistory(path, 100)
	if len(got) != 5 || got[0] != "prompt 0" || got[4] != "prompt 4" {
		t.Fatalf("history = %v, want 5 in-order prompts with malformed lines skipped", got)
	}

	capped := loadConsoleHistory(path, 3)
	if len(capped) != 3 || capped[0] != "prompt 2" {
		t.Fatalf("capped history = %v, want last 3", capped)
	}

	if missing := loadConsoleHistory(filepath.Join(dir, "absent.jsonl"), 100); missing != nil {
		t.Fatalf("missing file must yield nil, got %v", missing)
	}
}

func TestAppendConsoleHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")

	appendConsoleHistory(path, "first")()
	appendConsoleHistory(path, "second")()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("file has %d lines, want 2", len(lines))
	}
	var entry struct {
		Ts   string `json:"ts"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &entry); err != nil || entry.Text != "second" {
		t.Fatalf("second line = %q, want a parseable second prompt", lines[1])
	}
	if _, err := time.Parse(time.RFC3339, entry.Ts); err != nil {
		t.Fatalf("timestamp not RFC3339: %q", entry.Ts)
	}
}
