//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/sys/unix"

	"github.com/toasterbook88/axis/cmd/axis/tui"
	"github.com/toasterbook88/axis/internal/console"
)

// TestWorkspaceProgramMouseScrollsChat locks the production option function.
// newWorkspaceProgram uses workspaceProgramOptions. On a PTY that must emit
// both mouse-tracking sequences, and one SGR wheel notch must lift the
// transcript without recalling the prompt. An up arrow still recalls it.
func TestWorkspaceProgramMouseScrollsChat(t *testing.T) {
	isolateProgramState(t)
	master, slave := openProgramPTY(t, 120, 40)
	scr := captureProgramPTY(master)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	chat := console.NewModel(console.Options{
		Embedded: true,
		History:  []string{"previous prompt"},
		Now:      func() time.Time { return fixed },
		Submit:   func(console.TurnID, string) tea.Cmd { return nil },
	})
	opts := workspaceProgramOptions(ctx).teaOptions()
	opts = append(opts,
		tea.WithInput(slave),
		tea.WithOutput(slave),
		tea.WithoutSignalHandler(),
	)
	p := tea.NewProgram(tui.NewWorkspace(chat), opts...)
	done := make(chan error, 1)
	var final tea.Model
	var run sync.WaitGroup
	run.Add(1)
	go func() {
		defer run.Done()
		model, err := p.Run()
		final = model
		done <- err
	}()
	// Quit, then wait, then the PTY cleanup closes the slave. Kill does not
	// wait for the read loop, so closing after it races File.Fd.
	t.Cleanup(func() {
		p.Quit()
		run.Wait()
	})

	waitProgram(t, scr, "\x1b[?1002h")
	waitProgram(t, scr, "\x1b[?1006h")
	for i := 0; i < 30; i++ {
		name := "entry-" + string(rune('A'+i%26)) + string(rune('a'+i/26))
		p.Send(console.EntryMsg{Entry: console.NewNoticeEntry(fixed, name)})
	}
	waitProgram(t, scr, "entry-Db")
	if _, err := master.Write([]byte("\x1b[<64;10;5M")); err != nil {
		t.Fatalf("write wheel: %v", err)
	}
	waitProgram(t, scr, "3 more line(s) below")
	if strings.Contains(scr.text(), "previous prompt") {
		t.Fatal("wheel recalled the input history")
	}
	if _, err := master.Write([]byte("\x1b[A")); err != nil {
		t.Fatalf("write up: %v", err)
	}
	waitProgram(t, scr, "previous prompt")
	if _, err := master.Write([]byte{0x18, 'q'}); err != nil {
		t.Fatalf("write quit: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("program: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("program did not quit")
	}
	ws, ok := final.(tui.Workspace)
	if !ok {
		t.Fatalf("final model %T", final)
	}
	view := ws.View()
	if !strings.Contains(view, "3 more line(s) below") || !strings.Contains(view, "previous prompt") {
		t.Fatalf("final view lost the scroll or the recalled prompt:\n%s", view)
	}
}

// TestDashboardAndStandaloneDoNotEnableMouse locks the two programs that must
// keep the terminal's own scrollback. runDashboard passes only the alternate
// screen. The standalone console passes consoleOptions, which leaves the mouse off.
func TestDashboardAndStandaloneDoNotEnableMouse(t *testing.T) {
	t.Run("dashboard", func(t *testing.T) {
		isolateProgramState(t)
		master, slave := openProgramPTY(t, 80, 24)
		scr := captureProgramPTY(master)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		t.Cleanup(cancel)
		p := tea.NewProgram(tui.NewModel(),
			tea.WithContext(ctx),
			tea.WithAltScreen(),
			tea.WithInput(slave),
			tea.WithOutput(slave),
			tea.WithoutSignalHandler(),
		)
		done := make(chan error, 1)
		var run sync.WaitGroup
		run.Add(1)
		go func() {
			defer run.Done()
			_, err := p.Run()
			done <- err
		}()
		t.Cleanup(func() {
			p.Quit()
			run.Wait()
		})
		waitProgramAny(t, scr, "Loading cluster snapshot", "Error:")
		assertMouseOff(t, scr)
		if _, err := master.Write([]byte{'q'}); err != nil {
			t.Fatalf("write quit: %v", err)
		}
		waitProgramExit(t, done)
	})

	t.Run("standalone", func(t *testing.T) {
		isolateProgramState(t)
		master, slave := openProgramPTY(t, 80, 24)
		scr := captureProgramPTY(master)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		t.Cleanup(cancel)
		model := console.NewModel(console.Options{
			History: []string{"previous prompt"},
			Submit:  func(console.TurnID, string) tea.Cmd { return nil },
		})
		opts := consoleOptions(ctx).teaOptions()
		opts = append(opts, tea.WithInput(slave), tea.WithOutput(slave), tea.WithoutSignalHandler())
		p := tea.NewProgram(model, opts...)
		done := make(chan error, 1)
		var run sync.WaitGroup
		run.Add(1)
		go func() {
			defer run.Done()
			_, err := p.Run()
			done <- err
		}()
		t.Cleanup(func() {
			p.Quit()
			run.Wait()
		})
		waitProgram(t, scr, "> ")
		assertMouseOff(t, scr)
		// Two ctrl+c presses on an empty editor quit the standalone console.
		if _, err := master.Write([]byte{0x03, 0x03}); err != nil {
			t.Fatalf("write quit: %v", err)
		}
		waitProgramExit(t, done)
	})
}

func assertMouseOff(t *testing.T, scr *programScreen) {
	t.Helper()
	text := scr.text()
	if strings.Contains(text, "\x1b[?1002h") || strings.Contains(text, "\x1b[?1006h") {
		t.Fatalf("mouse tracking was enabled:\n%s", scr.tail())
	}
}

func waitProgramExit(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("program: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("program did not quit")
	}
}

func isolateProgramState(t *testing.T) {
	t.Helper()
	t.Setenv("AXIS_HOME", t.TempDir())
	t.Setenv("AXIS_API_TOKEN", strings.Repeat("ab", 32))
}

func waitProgram(t *testing.T, scr *programScreen, sub string) {
	t.Helper()
	waitProgramAny(t, scr, sub)
}

func waitProgramAny(t *testing.T, scr *programScreen, subs ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		text := scr.text()
		for _, sub := range subs {
			if strings.Contains(text, sub) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %q; screen tail:\n%s", subs, scr.tail())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func openProgramPTY(t *testing.T, cols, rows int) (master, slave *os.File) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open ptmx: %v", err)
	}
	master = os.NewFile(uintptr(fd), "ptmx")
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		t.Fatalf("unlock pty: %v", err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		master.Close()
		t.Fatalf("pty number: %v", err)
	}
	sfd, err := unix.Open(fmt.Sprintf("/dev/pts/%d", n), unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		master.Close()
		t.Fatalf("open pts: %v", err)
	}
	slave = os.NewFile(uintptr(sfd), "pts")
	ws := &unix.Winsize{Row: uint16(rows), Col: uint16(cols)}
	if err := unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, ws); err != nil {
		slave.Close()
		master.Close()
		t.Fatalf("winsize: %v", err)
	}
	if err := unix.IoctlSetWinsize(sfd, unix.TIOCSWINSZ, ws); err != nil {
		slave.Close()
		master.Close()
		t.Fatalf("winsize slave: %v", err)
	}
	t.Cleanup(func() {
		slave.Close()
		master.Close()
	})
	return master, slave
}

type programScreen struct {
	mu  sync.Mutex
	buf strings.Builder
}

func captureProgramPTY(master *os.File) *programScreen {
	s := &programScreen{}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				s.mu.Lock()
				s.buf.Write(buf[:n])
				s.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	return s
}

func (s *programScreen) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *programScreen) tail() string {
	text := s.text()
	if len(text) > 800 {
		text = text[len(text)-800:]
	}
	return fmt.Sprintf("%q", text)
}
