//go:build linux

package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/sys/unix"

	"github.com/toasterbook88/axis/internal/console"
)

// These tests drive a real bubbletea program on a PTY. The bytes are the
// sequences a terminal sends; the assertions are the workspace state after
// the parser and the mouse handler have both run.
//
// Which program turns mouse tracking on is locked in package main
// (TestWorkspaceProgramMouseScrollsChat and
// TestDashboardAndStandaloneDoNotEnableMouse). Here the workspace is started
// with the same two options that function adds: the alternate screen and
// mouse cell motion.

func TestPTYWheelEncodingsLeaveHistoryAlone(t *testing.T) {
	if wheelScrollLines != 3 {
		t.Fatalf("wheel notch = %d lines, the runbook says three", wheelScrollLines)
	}
	s := startWorkspace(t, 120, 40, true)
	s.wait("\x1b[?1002h")
	s.wait("\x1b[?1006h")
	last := s.seed(30)
	s.wait(last)

	// A click, its release, and a drag are not a wheel. None of them may
	// scroll the transcript or recall a prompt.
	s.write(sgrMouse(0, 10, 5, 'M'))
	s.until(mouseAction(tea.MouseActionPress))
	s.write(sgrMouse(0, 10, 5, 'm'))
	s.until(mouseAction(tea.MouseActionRelease))
	s.write(sgrMouse(32, 10, 5, 'M'))
	s.until(mouseAction(tea.MouseActionMotion))
	s.poke()
	if strings.Contains(s.scr.text(), "previous prompt") || strings.Contains(s.scr.text(), "more line(s) below") {
		t.Fatalf("click or drag changed the conversation:\n%s", s.scr.tail())
	}

	// SGR wheel up, then the same wheel with the shift bit set. A terminal
	// that reports shift+wheel still scrolls; shift-drag selection is the
	// terminal not reporting the drag.
	s.write(sgrMouse(64, 10, 5, 'M'))
	s.until(wheelButton(tea.MouseButtonWheelUp))
	s.poke()
	s.wait(marker(wheelScrollLines))
	s.write(sgrMouse(68, 10, 5, 'M'))
	s.until(wheelButton(tea.MouseButtonWheelUp))
	s.poke()
	s.wait(marker(2 * wheelScrollLines))

	// X10 is the fallback encoding. One notch down leaves a single notch of lift.
	s.write(x10Wheel(true))
	s.until(wheelButton(tea.MouseButtonWheelDown))
	s.poke()
	if strings.Contains(s.scr.text(), "previous prompt") {
		t.Fatal("a wheel sequence recalled the input history")
	}

	s.write([]byte("\x1b[A"))
	s.until(keyIs("up"))
	s.poke()
	s.wait("previous prompt")
	s.write([]byte("\x1b[B"))
	s.until(keyIs("down"))
	s.poke()

	ws := s.quitLeader()
	if got := chatInput(t, ws); got != "" {
		t.Fatalf("down did not restore the draft, input = %q", got)
	}
	view := ws.View()
	if n, ok := markerCount(view); !ok || n != wheelScrollLines {
		t.Fatalf("marker = %d present=%v, want %d\n%s", n, ok, wheelScrollLines, view)
	}
	if strings.Contains(view, "previous prompt") {
		t.Fatalf("recalled prompt still on screen:\n%s", view)
	}
}

func TestPTYWheelAtTopDoesNotRecallHistory(t *testing.T) {
	s := startWorkspace(t, 120, 40, true)
	s.wait("\x1b[?1002h")
	last := s.seed(30)
	s.wait(last)
	for i := 0; i < 40; i++ {
		s.write(sgrMouse(64, 10, 5, 'M'))
		s.until(wheelButton(tea.MouseButtonWheelUp))
	}
	s.poke()
	ws := s.kill()
	if got := chatInput(t, ws); got != "" {
		t.Fatalf("wheel at the top recalled %q", got)
	}
	view := ws.View()
	if !strings.Contains(view, noticeName(0)) || strings.Contains(view, last) {
		t.Fatalf("scrolling to the top did not reach the first entry:\n%s", view)
	}
	if _, ok := markerCount(view); !ok {
		t.Fatalf("top of the transcript lost the below marker:\n%s", view)
	}
}

func TestPTYNewOutputStaysBelowWhileScrolled(t *testing.T) {
	s := startWorkspace(t, 120, 40, true)
	s.wait("\x1b[?1002h")
	s.seed(30)
	s.write(sgrMouse(64, 10, 5, 'M'))
	s.until(wheelButton(tea.MouseButtonWheelUp))
	s.poke()
	s.wait(marker(wheelScrollLines))

	const late = "late-line-not-yet-read"
	s.p.Send(console.EntryMsg{Entry: console.NewNoticeEntry(ptyClock, late)})
	s.until(noticeIs(late))
	s.poke()
	ws := s.kill()
	view := ws.View()
	if strings.Contains(view, late) {
		t.Fatalf("new output jumped into the scrolled view:\n%s", view)
	}
	n, ok := markerCount(view)
	if !ok || n <= wheelScrollLines {
		t.Fatalf("marker = %d present=%v, want it to grow past %d\n%s", n, ok, wheelScrollLines, view)
	}
	if got := chatInput(t, ws); got != "" {
		t.Fatalf("input = %q", got)
	}
}

func TestPTYPgUpStillScrolls(t *testing.T) {
	s := startWorkspace(t, 120, 40, true)
	s.wait("\x1b[?1002h")
	last := s.seed(30)
	s.wait(last)
	s.write([]byte("\x1b[5~"))
	s.until(keyIs("pgup"))
	s.poke()
	s.wait("more line(s) below")
	// Half a pane is more than one wheel notch. A handful of notches returns.
	for i := 0; i < 8; i++ {
		s.write(sgrMouse(65, 10, 5, 'M'))
		s.until(wheelButton(tea.MouseButtonWheelDown))
	}
	s.poke()
	ws := s.quitLeader()
	view := ws.View()
	if _, ok := markerCount(view); ok {
		t.Fatalf("wheel down did not return to the bottom:\n%s", view)
	}
	if !strings.Contains(view, last) {
		t.Fatalf("newest entry not visible at the bottom:\n%s", view)
	}
	if got := chatInput(t, ws); got != "" {
		t.Fatalf("input = %q", got)
	}
}

func TestPTYNarrowPaneStillScrolls(t *testing.T) {
	// Below minSidebarTerminal the sidebar is hidden. The wheel still hits
	// the conversation, which now owns the full width.
	s := startWorkspace(t, 80, 24, true)
	s.wait("\x1b[?1006h")
	last := s.seed(30)
	s.wait(last)
	s.write(sgrMouse(64, 4, 3, 'M'))
	s.until(wheelButton(tea.MouseButtonWheelUp))
	s.poke()
	ws := s.kill()
	view := ws.View()
	if n, ok := markerCount(view); !ok || n != wheelScrollLines {
		t.Fatalf("narrow marker = %d present=%v\n%s", n, ok, view)
	}
	if got := chatInput(t, ws); got != "" {
		t.Fatalf("input = %q", got)
	}
	if ws.sidebarVisible() {
		t.Fatal("sidebar visible below the minimum width")
	}
}

func TestPTYLeaderAndHelpIgnoreWheel(t *testing.T) {
	t.Run("leader", func(t *testing.T) {
		s := startWorkspace(t, 120, 40, true)
		s.wait("\x1b[?1002h")
		s.write([]byte{0x18})
		s.until(keyIs("ctrl+x"))
		s.poke()
		s.write(sgrMouse(64, 10, 5, 'M'))
		s.until(wheelButton(tea.MouseButtonWheelUp))
		s.poke()
		ws := s.kill()
		if !ws.leader {
			t.Fatal("wheel cleared the leader")
		}
		if got := chatInput(t, ws); got != "" {
			t.Fatalf("input = %q", got)
		}
	})
	t.Run("help", func(t *testing.T) {
		s := startWorkspace(t, 120, 40, true)
		s.wait("\x1b[?1002h")
		s.write([]byte{0x18, 'h'})
		s.until(keyIs("h"))
		s.poke()
		s.write(sgrMouse(65, 10, 5, 'M'))
		s.until(wheelButton(tea.MouseButtonWheelDown))
		s.poke()
		// A key would have closed help, and one ctrl+c would then not quit.
		ws := s.writeAndStop([]byte{0x03})
		if !ws.showHelp {
			t.Fatal("wheel closed the help overlay")
		}
	})
}

func TestPTYDashboardAndPaletteWheel(t *testing.T) {
	t.Run("dashboard", func(t *testing.T) {
		s := startWorkspace(t, 120, 40, true)
		s.wait("\x1b[?1002h")
		s.p.Send(snapshotLoadedMsg{Snapshot: redesignFixture(), Timestamp: "3:04PM", Source: "test"})
		s.until(func(msg tea.Msg) bool {
			_, ok := msg.(snapshotLoadedMsg)
			return ok
		})
		s.poke()
		s.write([]byte("\x1bOQ"))
		s.until(keyIs("f2"))
		s.poke()
		s.write(sgrMouse(65, 10, 5, 'M'))
		s.until(wheelButton(tea.MouseButtonWheelDown))
		s.poke()
		ws := s.kill()
		if ws.view != viewDashboard || ws.dash.cursor != 1 {
			t.Fatalf("view %d cursor %d, want dashboard cursor 1", ws.view, ws.dash.cursor)
		}
		if got := chatInput(t, ws); got != "" {
			t.Fatalf("dashboard wheel reached the prompt: %q", got)
		}
	})
	t.Run("palette", func(t *testing.T) {
		s := startWorkspace(t, 120, 40, true)
		s.wait("\x1b[?1002h")
		s.write([]byte{0x0f})
		s.until(keyIs("ctrl+o"))
		s.poke()
		s.write(sgrMouse(65, 10, 5, 'M'))
		s.until(wheelButton(tea.MouseButtonWheelDown))
		s.poke()
		ws := s.kill()
		if ws.palette == nil || ws.palette.cursor != 1 {
			t.Fatalf("palette = %+v, want cursor 1", ws.palette)
		}
		if got := chatInput(t, ws); got != "" {
			t.Fatalf("palette wheel reached the prompt: %q", got)
		}
	})
}

func TestPTYPlacementWheelDoesNotRecallHistory(t *testing.T) {
	s := startWorkspace(t, 120, 40, true)
	s.wait("\x1b[?1002h")
	s.p.Send(snapshotLoadedMsg{Snapshot: redesignFixture(), Timestamp: "3:04PM", Source: "test"})
	s.until(func(msg tea.Msg) bool {
		_, ok := msg.(snapshotLoadedMsg)
		return ok
	})
	s.poke()
	s.write([]byte{0x18, 'p'})
	s.until(keyIs("p"))
	s.poke()
	s.wait("Workload Placement Wizard")
	s.write(sgrMouse(64, 10, 5, 'M'))
	s.until(wheelButton(tea.MouseButtonWheelUp))
	s.poke()
	ws := s.kill()
	if !ws.dash.modalActive {
		t.Fatal("wheel closed placement")
	}
	if got := chatInput(t, ws); got != "" {
		t.Fatalf("placement wheel recalled %q", got)
	}
	if strings.Contains(ws.View(), "previous prompt") {
		t.Fatal("recalled prompt drawn over placement")
	}
}

func TestPTYArrowWithoutMouseRecallsHistory(t *testing.T) {
	// The control: with tracking off, an up key is history. That is what a
	// terminal synthesizes for the wheel when the program never enables the mouse.
	s := startWorkspace(t, 120, 40, false)
	// The prompt is drawn after the startup sequences, so a mouse-enable
	// sequence would already be in the capture.
	s.wait("> ")
	if strings.Contains(s.scr.text(), "\x1b[?1002h") || strings.Contains(s.scr.text(), "\x1b[?1006h") {
		t.Fatalf("mouse tracking enabled without the option:\n%s", s.scr.tail())
	}
	s.write([]byte("\x1b[A"))
	s.until(keyIs("up"))
	s.poke()
	ws := s.kill()
	if got := chatInput(t, ws); got != "previous prompt" {
		t.Fatalf("up recalled %q", got)
	}
	if _, ok := markerCount(ws.View()); ok {
		t.Fatal("an up key scrolled the transcript")
	}
}

var ptyClock = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

type wheelPoke struct{}

type ptySession struct {
	t      *testing.T
	p      *tea.Program
	master *os.File
	scr    *ptyScreen
	msgs   chan tea.Msg
	done   chan runResult
	stop   sync.Once
	result runResult
	clean  bool
}

type runResult struct {
	model tea.Model
	err   error
}

func startWorkspace(t *testing.T, cols, rows int, mouse bool) *ptySession {
	t.Helper()
	isolateAxisState(t)
	master, slave := openPTY(t, cols, rows)
	scr := capturePTY(master)
	msgs := make(chan tea.Msg, 512)
	chat := console.NewModel(console.Options{
		Embedded: true,
		History:  []string{"previous prompt"},
		Now:      func() time.Time { return ptyClock },
		Submit:   func(console.TurnID, string) tea.Cmd { return nil },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	opts := []tea.ProgramOption{
		tea.WithContext(ctx),
		tea.WithAltScreen(),
		tea.WithInput(slave),
		tea.WithOutput(slave),
		tea.WithoutSignalHandler(),
		tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			select {
			case msgs <- msg:
			default:
			}
			return msg
		}),
	}
	if mouse {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	p := tea.NewProgram(NewWorkspace(chat), opts...)
	done := make(chan runResult, 1)
	go func() {
		model, err := p.Run()
		done <- runResult{model, err}
	}()
	s := &ptySession{t: t, p: p, master: master, scr: scr, msgs: msgs, done: done}
	// Registered after openPTY, so this runs first. Files close only after
	// Run has returned on the graceful path. Kill's shutdown closes the
	// cancel reader without waiting for the read loop, and that races
	// File.Fd, so a forced kill leaks the PTY instead of closing under it.
	t.Cleanup(func() {
		s.await(true)
		cancel()
		if s.clean {
			slave.Close()
			master.Close()
		}
	})
	return s
}

func (s *ptySession) seed(n int) string {
	s.t.Helper()
	last := noticeName(n - 1)
	for i := 0; i < n; i++ {
		s.p.Send(console.EntryMsg{Entry: console.NewNoticeEntry(ptyClock, noticeName(i))})
	}
	s.until(noticeIs(last))
	return last
}

func (s *ptySession) write(b []byte) {
	s.t.Helper()
	if _, err := s.master.Write(b); err != nil {
		s.t.Fatalf("write pty: %v", err)
	}
}

func (s *ptySession) wait(sub string) {
	s.t.Helper()
	s.scr.waitContains(s.t, 5*time.Second, sub)
}

func (s *ptySession) until(match func(tea.Msg) bool) {
	s.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case msg := <-s.msgs:
			if match(msg) {
				return
			}
		case <-deadline:
			s.t.Fatalf("timeout waiting for a message; screen tail:\n%s", s.scr.tail())
		}
	}
}

func (s *ptySession) poke() {
	s.t.Helper()
	s.p.Send(wheelPoke{})
	s.until(func(msg tea.Msg) bool {
		_, ok := msg.(wheelPoke)
		return ok
	})
}

// kill observes the model without a key. Program.Quit sends QuitMsg, which
// returns the model unchanged. Program.Kill cancels the context, and
// shutdown then closes the input reader before the read loop leaves File.Fd.
func (s *ptySession) kill() Workspace {
	s.t.Helper()
	return s.finish(true)
}

func (s *ptySession) quitLeader() Workspace {
	s.t.Helper()
	s.write([]byte{0x18, 'q'})
	return s.finish(false)
}

func (s *ptySession) writeAndStop(b []byte) Workspace {
	s.t.Helper()
	s.write(b)
	return s.finish(false)
}

// await waits until Run returns. quit asks the program to exit without a
// key. The first caller wins; cleanup calls this again and must not receive
// from done twice.
func (s *ptySession) await(quit bool) runResult {
	s.stop.Do(func() {
		if quit {
			s.p.Quit()
		}
		if s.waitDone(5 * time.Second) {
			s.clean = true
			return
		}
		s.p.Quit()
		if s.waitDone(time.Second) {
			s.clean = true
			return
		}
		// Last resort. Run may return while the read loop is still in File.Fd,
		// so cleanup must not close the PTY when this path is taken.
		s.p.Kill()
		if s.waitDone(time.Second) {
			return
		}
		s.result.err = errors.New("program did not stop")
	})
	return s.result
}

func (s *ptySession) waitDone(d time.Duration) bool {
	select {
	case s.result = <-s.done:
		return true
	case <-time.After(d):
		return false
	}
}

func (s *ptySession) finish(quit bool) Workspace {
	s.t.Helper()
	res := s.await(quit)
	if res.err != nil {
		s.t.Fatalf("program: %v\nscreen tail:\n%s", res.err, s.scr.tail())
	}
	ws, ok := res.model.(Workspace)
	if !ok {
		s.t.Fatalf("final model %T", res.model)
	}
	return ws
}

func noticeName(i int) string {
	return "entry-" + string(rune('A'+i%26)) + string(rune('a'+i/26))
}

func noticeIs(text string) func(tea.Msg) bool {
	return func(msg tea.Msg) bool {
		e, ok := msg.(console.EntryMsg)
		if !ok {
			return false
		}
		n, ok := e.Entry.(*console.NoticeEntry)
		return ok && n.Text == text
	}
}

func keyIs(want string) func(tea.Msg) bool {
	return func(msg tea.Msg) bool {
		k, ok := msg.(tea.KeyMsg)
		return ok && k.String() == want
	}
}

func wheelButton(btn tea.MouseButton) func(tea.Msg) bool {
	return func(msg tea.Msg) bool {
		m, ok := msg.(tea.MouseMsg)
		return ok && m.Button == btn && tea.MouseEvent(m).IsWheel()
	}
}

func mouseAction(action tea.MouseAction) func(tea.Msg) bool {
	return func(msg tea.Msg) bool {
		m, ok := msg.(tea.MouseMsg)
		if !ok || tea.MouseEvent(m).IsWheel() {
			return false
		}
		return tea.MouseEvent(m).Action == action
	}
}

func chatInput(t *testing.T, ws Workspace) string {
	t.Helper()
	m, ok := ws.chat.(console.Model)
	if !ok {
		t.Fatalf("chat type %T", ws.chat)
	}
	return m.Input()
}

func marker(n int) string {
	return fmt.Sprintf("%d more line(s) below", n)
}

var markerPattern = regexp.MustCompile(`(\d+) more line\(s\) below`)

func markerCount(view string) (int, bool) {
	m := markerPattern.FindStringSubmatch(view)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// sgrMouse is one SGR mouse report. release is 'M' for press and 'm' for release.
// Wheel buttons have no release. 64 is wheel up, 65 is wheel down, 68 is
// wheel up with shift, 32 is motion, 0 is the left button.
func sgrMouse(button, x, y int, release byte) []byte {
	return fmt.Appendf(nil, "\x1b[<%d;%d;%d%c", button, x, y, release)
}

// x10Wheel is the six-byte fallback report. down selects wheel down.
func x10Wheel(down bool) []byte {
	cb := byte(32 + 64)
	if down {
		cb = byte(32 + 65)
	}
	return []byte{0x1b, '[', 'M', cb, byte(32 + 10), byte(32 + 5)}
}

func isolateAxisState(t *testing.T) {
	t.Helper()
	t.Setenv("AXIS_HOME", t.TempDir())
	// 32 decoded bytes, so a snapshot fetch does not create a token file.
	t.Setenv("AXIS_API_TOKEN", strings.Repeat("ab", 32))
}

func openPTY(t *testing.T, cols, rows int) (master, slave *os.File) {
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
	// The caller closes both after Program.Run returns. Closing the slave
	// while the read loop is in File.Fd races.
	return master, slave
}

type ptyScreen struct {
	mu  sync.Mutex
	buf strings.Builder
}

func capturePTY(master *os.File) *ptyScreen {
	s := &ptyScreen{}
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

func (s *ptyScreen) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *ptyScreen) tail() string {
	text := s.text()
	if len(text) > 800 {
		text = text[len(text)-800:]
	}
	return strconv.Quote(text)
}

func (s *ptyScreen) waitContains(t *testing.T, timeout time.Duration, sub string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if strings.Contains(s.text(), sub) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s; screen tail:\n%s", strconv.Quote(sub), s.tail())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
