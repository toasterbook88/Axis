package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/console"
	"github.com/toasterbook88/axis/internal/mcpclient"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

const testAdvisory = "advisory: agent output is not cluster truth — it uses tools to read the fact plane"

// fakeConsoleProgram stands in for the Bubble Tea program: it records every
// Send and runs until the test (or a Kill) makes it exit. It never touches a
// terminal.
type fakeConsoleProgram struct {
	capture
	opts    console.Options
	started chan struct{}
	quit    chan struct{}
	once    sync.Once
	onRun   func()
}

func newFakeConsoleProgram() *fakeConsoleProgram {
	return &fakeConsoleProgram{started: make(chan struct{}), quit: make(chan struct{})}
}

func (p *fakeConsoleProgram) Run() (tea.Model, error) {
	if p.onRun != nil {
		p.onRun()
	}
	close(p.started)
	<-p.quit
	return nil, nil
}

func (p *fakeConsoleProgram) Kill() { p.exit() }

func (p *fakeConsoleProgram) exit() { p.once.Do(func() { close(p.quit) }) }

// waitMsg polls the recorded messages until pred matches one.
func (p *fakeConsoleProgram) waitMsg(t *testing.T, what string, pred func(tea.Msg) bool) tea.Msg {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range p.all() {
			if pred(m) {
				return m
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; got %s", what, describeMsgs(p.all()))
	return nil
}

func (p *fakeConsoleProgram) has(pred func(tea.Msg) bool) bool {
	for _, m := range p.all() {
		if pred(m) {
			return true
		}
	}
	return false
}

func describeMsgs(msgs []tea.Msg) string {
	var b strings.Builder
	for _, m := range msgs {
		switch v := m.(type) {
		case console.StartupStatusMsg:
			fmt.Fprintf(&b, "\n  status %s state=%d persist=%v %q", v.Line.Key, v.Line.State, v.Line.Persist, v.Line.Text)
		case console.EntryMsg:
			fmt.Fprintf(&b, "\n  entry %q", entryText(v.Entry))
		default:
			fmt.Fprintf(&b, "\n  %T %+v", m, m)
		}
	}
	return b.String()
}

func entryText(e console.Entry) string {
	return strings.Join(console.PlainAll(e.Render(200)), " ")
}

func statusIs(key string, state console.StatusState, text string) func(tea.Msg) bool {
	return func(m tea.Msg) bool {
		s, ok := m.(console.StartupStatusMsg)
		return ok && s.Line.Key == key && s.Line.State == state && s.Line.Text == text
	}
}

func persistContains(state console.StatusState, sub string) func(tea.Msg) bool {
	return func(m tea.Msg) bool {
		s, ok := m.(console.StartupStatusMsg)
		return ok && s.Line.Persist && s.Line.State == state && strings.Contains(s.Line.Text, sub)
	}
}

func modelStateIs(state console.ModelState) func(tea.Msg) bool {
	return func(m tea.Msg) bool {
		s, ok := m.(console.ModelStateMsg)
		return ok && s.State == state
	}
}

func noticeContains(sub string) func(tea.Msg) bool {
	return func(m tea.Msg) bool {
		e, ok := m.(console.EntryMsg)
		return ok && strings.Contains(entryText(e.Entry), sub)
	}
}

func countMsgs(msgs []tea.Msg, pred func(tea.Msg) bool) int {
	n := 0
	for _, m := range msgs {
		if pred(m) {
			n++
		}
	}
	return n
}

// stubBackend is a named ChatBackend so tests can tell backends apart.
type stubBackend struct{ name string }

func (b *stubBackend) ChatStream(context.Context, []chat.Message, []chat.ToolDef, io.Writer) (chat.Message, error) {
	return chat.Message{Role: chat.RoleAssistant, Content: "ok from " + b.name}, nil
}

func readyResult(model string, b agent.ChatBackend) agentStartupBackendResult {
	return agentStartupBackendResult{
		ActiveTarget: ModelChoice{ID: model, Model: model, Protocol: agent.ProtocolOllama, Endpoint: "http://localhost:11434"},
		Backend:      b,
	}
}

type startupOutcome struct {
	agent *agent.Agent
	err   error
}

// consoleStartupHarness stubs every seam the console startup path reaches:
// AXIS home, runtime loader, program constructor, MCP connect/close.
type consoleStartupHarness struct {
	prog        *fakeConsoleProgram
	programs    atomic.Int32
	mcpClosed   atomic.Bool
	errW        *bytes.Buffer
	errWAtPaint atomic.Int64
}

func newConsoleStartupHarness(t *testing.T, loader func(context.Context) (*runtimectx.Context, error)) *consoleStartupHarness {
	t.Helper()
	t.Setenv("AXIS_HOME", t.TempDir())
	h := &consoleStartupHarness{prog: newFakeConsoleProgram(), errW: &bytes.Buffer{}}
	h.errWAtPaint.Store(-1)
	h.prog.onRun = func() { h.errWAtPaint.Store(int64(h.errW.Len())) }

	prevLoad, prevProg := loadAgentShellRuntime, newConsoleProgramFn
	prevConnect, prevClose := consoleConnectMCPFn, consoleCloseMCPFn
	prevSetup := consoleSetupBackendFn
	prevRTWait, prevShutdown := consoleRuntimeLoadWait, consoleStartupShutdownWaitFor
	t.Cleanup(func() {
		h.prog.exit()
		loadAgentShellRuntime, newConsoleProgramFn = prevLoad, prevProg
		consoleConnectMCPFn, consoleCloseMCPFn = prevConnect, prevClose
		consoleSetupBackendFn = prevSetup
		consoleRuntimeLoadWait, consoleStartupShutdownWaitFor = prevRTWait, prevShutdown
	})
	if loader == nil {
		loader = func(context.Context) (*runtimectx.Context, error) {
			return &runtimectx.Context{Config: &config.Config{}}, nil
		}
	}
	loadAgentShellRuntime = loader
	newConsoleProgramFn = func(_ context.Context, opts console.Options) consoleProgram {
		h.programs.Add(1)
		h.prog.opts = opts
		return h.prog
	}
	consoleConnectMCPFn = func(context.Context, *mcpclient.Registry, *config.Config) {}
	consoleCloseMCPFn = func(*mcpclient.Registry) { h.mcpClosed.Store(true) }
	return h
}

func (h *consoleStartupHarness) launch(t *testing.T, in agentStartupInputs) <-chan startupOutcome {
	t.Helper()
	done := make(chan startupOutcome, 1)
	go func() {
		a, err := runAgentConsoleStartup(context.Background(), in, h.errW)
		done <- startupOutcome{agent: a, err: err}
	}()
	select {
	case <-h.prog.started:
	case out := <-done:
		t.Fatalf("console startup returned before paint: agent=%v err=%v", out.agent, out.err)
	case <-time.After(5 * time.Second):
		t.Fatal("console never painted")
	}
	return done
}

func awaitOutcome(t *testing.T, done <-chan startupOutcome, within time.Duration) startupOutcome {
	t.Helper()
	select {
	case out := <-done:
		return out
	case <-time.After(within):
		t.Fatalf("runAgentConsoleStartup did not return within %s", within)
		return startupOutcome{}
	}
}

func defaultStartupInputs() agentStartupInputs {
	return agentStartupInputs{provider: "auto", autonomy: "default", maxTurns: 25, maxTokens: 32768, timeout: time.Minute}
}

// blockingResolver parks the startup resolver until release is closed.
type blockingResolver struct {
	called  chan agentStartupBackendParams
	release chan struct{}
	result  agentStartupBackendResult
	err     error
}

func newBlockingResolver(result agentStartupBackendResult, err error) *blockingResolver {
	return &blockingResolver{called: make(chan agentStartupBackendParams, 1), release: make(chan struct{}), result: result, err: err}
}

func (r *blockingResolver) fn(p agentStartupBackendParams) (agentStartupBackendResult, error) {
	r.called <- p
	<-r.release
	return r.result, r.err
}

func (r *blockingResolver) awaitCalled(t *testing.T) agentStartupBackendParams {
	t.Helper()
	select {
	case p := <-r.called:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("startup resolver was never called")
		return agentStartupBackendParams{}
	}
}

func runCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("submit returned a nil command")
	}
	got := make(chan tea.Msg, 1)
	go func() { got <- cmd() }()
	select {
	case m := <-got:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("command did not finish")
		return nil
	}
}

func (c *capture) has(pred func(tea.Msg) bool) bool {
	for _, m := range c.all() {
		if pred(m) {
			return true
		}
	}
	return false
}
