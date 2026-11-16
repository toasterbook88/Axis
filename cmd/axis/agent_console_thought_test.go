package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/console"
)

type consoleMsgSender struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (s *consoleMsgSender) Send(msg tea.Msg) {
	s.mu.Lock()
	s.msgs = append(s.msgs, msg)
	s.mu.Unlock()
}

// TestAgentMultiRoundOutOfBandThinkingConsoleIntegration verifies the real
// CloudBackend -> Agent.Run -> ColorWriter -> console.streamWriter -> console.Model
// pipeline across multiple tool-calling rounds whose reasoning_content contains
// a literal </think> token.
func TestAgentMultiRoundOutOfBandThinkingConsoleIntegration(t *testing.T) {
	var round int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		round++
		w.Header().Set("Content-Type", "text/event-stream")
		if round == 1 {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"plan </think> inspect\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"todo\",\"arguments\":\"{\\\"action\\\":\\\"list\\\"}\"}}]}}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"verify result\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"No tasks.\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	backend, err := agent.NewOpenAICompatibleBackend(server.URL, "test-model", "")
	if err != nil {
		t.Fatalf("NewOpenAICompatibleBackend: %v", err)
	}

	rec := &consoleMsgSender{}
	writer := console.NewStreamWriter(rec, 1)
	bridge := console.NewBridge(rec, 1, time.Now)

	a := agent.New(agent.Config{
		Backend:  backend,
		MaxTurns: 4,
		Autonomy: agent.AutonomyDefault,
		Observer: bridge,
		Output:   writer,
	})

	if err := a.Run(context.Background(), "check tasks"); err != nil {
		t.Fatalf("Agent.Run: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}

	var base = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	var step int
	m := console.NewModel(console.Options{
		Submit: func(console.TurnID, string) tea.Cmd { return nil },
		Now: func() time.Time {
			step++
			return base.Add(time.Duration(step) * time.Second)
		},
	})
	for _, r := range "check tasks" {
		u, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = u.(console.Model)
	}
	u, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = u.(console.Model)

	rec.mu.Lock()
	msgs := append([]tea.Msg(nil), rec.msgs...)
	rec.mu.Unlock()
	for _, msg := range msgs {
		u, _ := m.Update(msg)
		m = u.(console.Model)
	}
	u, cmd := m.Update(console.TurnDoneMsg{Turn: m.Turn()})
	m = u.(console.Model)

	if cmd == nil {
		t.Fatal("expected committed entries from TurnDoneMsg")
	}
	msgsOut := collectPrintedLines(cmd)
	joined := strings.Join(msgsOut, "\n")
	if !strings.Contains(joined, "plan </think> inspect") || !strings.Contains(joined, "1 more lines") {
		t.Fatalf("missing collapsed multi-round thoughts:\n%s", joined)
	}
	if !strings.Contains(joined, "No tasks.") || strings.Contains(joined, "verify result") {
		t.Fatalf("unexpected committed answer/collapse:\n%s", joined)
	}
	if !strings.Contains(joined, "Thought for") {
		t.Fatalf("missing ThinkingEntry duration header:\n%s", joined)
	}

	// Verify /thought expands both rounds cleanly without any truncation or leak.
	for _, r := range "/thought" {
		u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = u.(console.Model)
	}
	_, cmdExp := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	expanded := strings.Join(collectPrintedLines(cmdExp), "\n")
	if !strings.Contains(expanded, "plan </think> inspect") || !strings.Contains(expanded, "verify result") {
		t.Fatalf("/thought expansion missing full multi-round thoughts:\n%s", expanded)
	}
}

func collectPrintedLines(cmd tea.Cmd) []string {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch m := msg.(type) {
	case tea.BatchMsg:
		var out []string
		for _, c := range m {
			out = append(out, collectPrintedLines(c)...)
		}
		return out
	default:
		s := fmt.Sprintf("%v", msg)
		if s != "" {
			return []string{s}
		}
		return nil
	}
}
