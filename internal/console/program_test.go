package console

import (
	"bytes"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// These tests drive the console Model through a real tea.Program on the main
// screen and assert on the bytes the renderer actually emits. They are the
// acceptance gate for the transcript-first design: tea.Println is the only
// path by which committed content reaches terminal scrollback, and it is a
// no-op under the alternate screen. A unit test that inspects the Model's
// returned commands cannot prove the renderer flushed them; only a program
// run against a real output stream can.

// immediateSubmit finishes a turn instantly, so the committed UserEntry is the
// only transcript content produced by a submitted prompt.
func immediateSubmit() SubmitFunc {
	return func(turn TurnID, _ string) tea.Cmd {
		return func() tea.Msg { return TurnDoneMsg{Turn: turn} }
	}
}

// runProgram drives a console Model through a real tea.Program, submits the
// given line, waits for the turn to settle and the renderer to flush, then
// quits. It returns everything the renderer wrote to the output stream.
//
// The committed transcript is flushed asynchronously on the renderer's own
// ticker, so the program is quit from a goroutine after a bounded wait rather
// than by a ctrl+c keypress, which would race the flush and drop the line.
func runProgram(t *testing.T, submit SubmitFunc, line string, opts ...tea.ProgramOption) string {
	t.Helper()

	var out bytes.Buffer
	m := NewModel(Options{Submit: submit, Now: fixedNow})

	base := []tea.ProgramOption{
		tea.WithInput(strings.NewReader(line + "\r")),
		tea.WithOutput(&out),
	}
	base = append(base, opts...)

	p := tea.NewProgram(m, base...)

	go func() {
		time.Sleep(200 * time.Millisecond)
		p.Quit()
	}()

	if _, err := p.Run(); err != nil {
		t.Fatalf("program run: %v", err)
	}
	return out.String()
}

func TestProgramCommitsTranscriptToOutput(t *testing.T) {
	// "hello" is submitted, which commits a UserEntry via tea.Println. If the
	// committed line never reaches the output stream, the transcript-first
	// design is broken regardless of what the unit tests say.
	out := runProgram(t, immediateSubmit(), "hello")

	if !strings.Contains(out, "hello") {
		t.Fatalf("committed transcript did not reach the output stream:\n%q", out)
	}
}

func TestProgramAltScreenDropsCommittedLines(t *testing.T) {
	// Negative control: the main-screen requirement is load-bearing. Under the
	// alternate screen tea.Println is a no-op, so the committed transcript must
	// NOT reach the output stream. This pins the reason consoleProgramOptions
	// must never request WithAltScreen.
	out := runProgram(t, immediateSubmit(), "hello", tea.WithAltScreen())

	if strings.Contains(out, "hello") {
		t.Errorf("committed transcript reached the output under the alternate screen; tea.Println should be a no-op:\n%q", out)
	}
}
