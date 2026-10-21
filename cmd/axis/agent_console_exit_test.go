package main

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestConsoleExitCommandsQuitBeforeAndAfterStartup(t *testing.T) {
	for _, pending := range []bool{true, false} {
		for _, line := range []string{"/exit", "/quit", " /EXIT ", "/QuIt", "/exit ignored-argument"} {
			t.Run(line+map[bool]string{true: "/pending", false: "/ready"}[pending], func(t *testing.T) {
				l := newConsoleLauncher(nil, time.Minute, consoleClock)
				l.pending = pending
				l.prog = &capture{}
				l.slash = consoleSlashRunner(nil, nil, ModelChoice{})
				msg := l.submit(context.Background())(1, line)()
				if _, ok := msg.(tea.QuitMsg); !ok {
					t.Fatalf("%q returned %T, want tea.QuitMsg (pending=%t)", line, msg, pending)
				}
			})
		}
	}
}

func TestConsoleQuitLookalikesStayInSession(t *testing.T) {
	for _, line := range []string{"/exit-now", "/quitter", "/model /quit"} {
		t.Run(line, func(t *testing.T) {
			l := newConsoleLauncher(nil, time.Minute, consoleClock)
			l.prog = &capture{}
			l.slash = func(string) (string, error) { return "handled", nil }
			if msg := l.submit(context.Background())(1, line)(); msg != nil {
				if _, quit := msg.(tea.QuitMsg); quit {
					t.Fatalf("%q unexpectedly closed the console", line)
				}
			}
		})
	}
}
