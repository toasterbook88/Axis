package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

// budgetBackend records each chat request's uncorrected size and scripts
// failures. Summarizer calls (no tools) are counted apart from chat calls.
type budgetBackend struct {
	mu        sync.Mutex
	chatCalls int
	sumCalls  int
	sent      []int
	summary   string
	sumErr    error
	fail      func(call int) error // error for chat call n (1-based); nil = succeed
}

func (b *budgetBackend) ChatStream(_ context.Context, msgs []chat.Message, tools []chat.ToolDef, _ io.Writer) (chat.Message, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if tools == nil {
		b.sumCalls++
		if b.sumErr != nil {
			return chat.Message{}, b.sumErr
		}
		return chat.Message{Role: chat.RoleAssistant, Content: b.summary}, nil
	}
	b.chatCalls++
	b.sent = append(b.sent, roundRequest{msgs: msgs, defs: tools}.rawTokens())
	if b.fail != nil {
		if err := b.fail(b.chatCalls); err != nil {
			return chat.Message{}, err
		}
	}
	return chat.Message{Role: chat.RoleAssistant, Content: "ok"}, nil
}

func newRoundAgent(maxTokens int, mode AutonomyMode, backend ChatBackend, out io.Writer) *Agent {
	a := New(Config{
		Backend:     backend,
		MaxTurns:    1,
		MaxTokens:   maxTokens,
		Output:      out,
		ToolContext: NewToolContext(&RuntimeView{}, nil),
	})
	a.SetAutonomy(mode)
	return a
}

// addRound appends one tool round as the loop does: an assistant tool call
// and its result.
func addRound(a *Agent, id string, resultBytes int) {
	a.conv.Append(chat.Message{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{{
		ID: id, Function: chat.ToolCallFunction{Name: "grep_search", Arguments: json.RawMessage(`{"pattern":"x"}`)},
	}}})
	a.conv.Append(chat.Message{Role: chat.RoleTool, ToolCallID: id, Content: strings.Repeat("r", resultBytes)})
}

var errSummarizer = errors.New("summarizer down")

// The estimate counts what is sent: the tool definitions of the scope and
// the turn context, not only the conversation.
func TestEstimateCountsToolDefsAndTurnContext(t *testing.T) {
	cluster := newRoundAgent(32768, AutonomyCluster, noopChatBackend{}, io.Discard)
	exec := newRoundAgent(32768, AutonomyFull, noopChatBackend{}, io.Discard)
	for _, a := range []*Agent{cluster, exec} {
		a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "hi"})
	}
	ec := cluster.estimate(cluster.buildRequest("hi", nil))
	ee := exec.estimate(exec.buildRequest("hi", nil))
	if ee <= ec {
		t.Fatalf("exec scope estimates %d, cluster %d; exec sends more definitions", ee, ec)
	}
	// Whatever the messages hold, the rest of the estimate is the definitions.
	for _, a := range []*Agent{cluster, exec} {
		req := a.buildRequest("hi", nil)
		msgBytes := 0
		for _, m := range req.msgs {
			msgBytes += messageBytes(m)
		}
		defs, _ := json.Marshal(req.defs)
		if got, want := a.estimate(req), (msgBytes+len(defs))/4; got != want || len(defs) < 2000 {
			t.Fatalf("estimate %d, want %d (messages %d + definitions %d bytes, / 4)", got, want, msgBytes, len(defs))
		}
	}
	withCtx := exec.estimate(exec.buildRequest("hi", []string{strings.Repeat("c", 20000)}))
	if d := withCtx - ee; d < 4990 || d > 5010 {
		t.Fatalf("20,000 chars of turn context added %d tokens, want about 5,000", d)
	}
}

// At the default budget, in exec scope, with a summarizer that always fails
// and a large turn context, no request exceeds the usable budget. Before
// this change the conversation alone could fill 32,768 tokens and the tool
// definitions and turn context were sent on top.
func TestNoRequestExceedsBudgetAtDefault(t *testing.T) {
	b := &budgetBackend{sumErr: errSummarizer}
	a := newRoundAgent(32768, AutonomyFull, b, io.Discard)
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "search everything"})
	turnCtx := []string{strings.Repeat("c", 20000)}
	_, u := a.budget()
	for i := 0; i < 30; i++ {
		addRound(a, "t", 6000)
		if _, err := a.sendRound(context.Background(), b, a.backendGeneration(), "search everything", turnCtx); err != nil {
			t.Fatal(err)
		}
	}
	for i, raw := range b.sent {
		if raw > u {
			t.Fatalf("round %d sent about %d tokens, over U = %d", i+1, raw, u)
		}
	}
	if b.sent[len(b.sent)-1] < u/2 {
		t.Fatalf("last request %d tokens; the test did not reach the budget", b.sent[len(b.sent)-1])
	}
}

// loggedOverflowOnce fails the first chat call with the logged error.
func loggedOverflowOnce(call int) error {
	if call == 1 {
		return errors.New(loggedOverflow)
	}
	return nil
}

func alwaysOverflow(int) error { return errors.New(loggedOverflow) }

// recoveryAgent holds about 30,000 raw tokens, consistent with the logged
// error's 34,870, under --max-tokens 65536 so nothing is cut before sending.
func recoveryAgent(b *budgetBackend, out io.Writer) *Agent {
	a := newRoundAgent(65536, AutonomyDefault, b, out)
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "go"})
	for a.estimate(a.buildRequest("go", nil)) < 30000 {
		addRound(a, "t", 6000)
	}
	return a
}

func TestOverflowRecovery(t *testing.T) {
	t.Run("one overflow then success", func(t *testing.T) {
		b := &budgetBackend{summary: "summary of earlier work", fail: loggedOverflowOnce}
		a := recoveryAgent(b, io.Discard)
		if _, err := a.sendRound(context.Background(), b, a.backendGeneration(), "go", nil); err != nil {
			t.Fatal(err)
		}
		if b.chatCalls != 2 || a.bud.learnedWindow != 32768 {
			t.Fatalf("chat calls %d, learned window %d; want 2 and 32768", b.chatCalls, a.bud.learnedWindow)
		}
		if k := a.estimateFactor(); k <= 1.05 || k > 1.3 {
			t.Fatalf("k = %.3f, want about 1.16", k)
		}
		if b.sumCalls != 1 {
			t.Fatalf("summarizer calls %d, want 1", b.sumCalls)
		}
	})
	t.Run("truncation alone makes it fit", func(t *testing.T) {
		b := &budgetBackend{sumErr: errSummarizer, fail: loggedOverflowOnce}
		a := recoveryAgent(b, io.Discard)
		if _, err := a.sendRound(context.Background(), b, a.backendGeneration(), "go", nil); err != nil {
			t.Fatal(err)
		}
		_, u := a.budget()
		if b.chatCalls != 2 || b.sent[1] > u {
			t.Fatalf("chat calls %d, retry %d tokens; want 2 calls and the retry within U = %d", b.chatCalls, b.sent[1], u)
		}
	})
	t.Run("two overflows", func(t *testing.T) {
		b := &budgetBackend{summary: "s", fail: alwaysOverflow}
		a := recoveryAgent(b, io.Discard)
		if _, err := a.sendRound(context.Background(), b, a.backendGeneration(), "go", nil); err == nil || b.chatCalls != 2 {
			t.Fatalf("err %v after %d chat calls; want the overflow after exactly 2", err, b.chatCalls)
		}
	})
	t.Run("cannot shrink", func(t *testing.T) {
		b := &budgetBackend{summary: "s", fail: alwaysOverflow}
		a := newRoundAgent(65536, AutonomyDefault, b, io.Discard)
		// About 30,000 raw tokens with the system prompt and definitions,
		// consistent with the error's 34,870.
		big := strings.Repeat("u", 100000)
		a.conv.Append(chat.Message{Role: chat.RoleUser, Content: big})
		_, err := a.sendRound(context.Background(), b, a.backendGeneration(), big, nil)
		if err == nil || b.chatCalls != 1 || b.sumCalls != 0 {
			t.Fatalf("err %v, chat %d, summarizer %d; want the error after 1 chat call and no summary", err, b.chatCalls, b.sumCalls)
		}
		if a.bud.learnedWindow != 32768 || a.estimateFactor() == 1 {
			t.Fatalf("learned window %d, k %.3f; the fit rule was not reached", a.bud.learnedWindow, a.estimateFactor())
		}
	})
	t.Run("other 400", func(t *testing.T) {
		b := &budgetBackend{fail: func(int) error { return errors.New(`http status 400: {"error":"bad tool schema"}`) }}
		a := recoveryAgent(b, io.Discard)
		if _, err := a.sendRound(context.Background(), b, a.backendGeneration(), "go", nil); err == nil || b.chatCalls != 1 || a.bud.learnedWindow != 0 {
			t.Fatalf("err %v, chat %d, learned %d; want the error after 1 call and nothing learned", err, b.chatCalls, a.bud.learnedWindow)
		}
	})
	t.Run("absurd ratio not learned", func(t *testing.T) {
		var out bytes.Buffer
		b := &budgetBackend{summary: "s", fail: loggedOverflowOnce}
		a := newRoundAgent(65536, AutonomyDefault, b, &out)
		a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "short"})
		_, _ = a.sendRound(context.Background(), b, a.backendGeneration(), "short", nil)
		if a.estimateFactor() != 1 || a.bud.learnedWindow != 32768 || !strings.Contains(out.String(), "not learned") {
			t.Fatalf("k %.2f, window %d, output %q; want k 1, window 32768 and a skip event", a.estimateFactor(), a.bud.learnedWindow, out.String())
		}
	})
}

// switchingBackend installs another backend on the agent, then fails with
// an overflow, as when /model lands while a request is in flight.
type switchingBackend struct {
	a     *Agent
	calls int
}

func (s *switchingBackend) ChatStream(context.Context, []chat.Message, []chat.ToolDef, io.Writer) (chat.Message, error) {
	s.calls++
	s.a.SetBackend(noopChatBackend{}, BackendLocal, 0)
	return chat.Message{}, errors.New(loggedOverflow)
}

func TestOverflowFromSwitchedBackendIsNotLearned(t *testing.T) {
	a := newRoundAgent(65536, AutonomyDefault, noopChatBackend{}, io.Discard)
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "go"})
	s := &switchingBackend{a: a}
	if _, err := a.sendRound(context.Background(), s, a.backendGeneration(), "go", nil); err == nil {
		t.Fatal("overflow not returned")
	}
	if s.calls != 1 || a.bud.learnedWindow != 0 {
		t.Fatalf("calls %d, learned window %d; want 1 and 0", s.calls, a.bud.learnedWindow)
	}
}

// A model switch between reading the backend and sending (while the turn
// context is built) must not teach the old backend's window to the new one.
func TestOverflowAfterSwitchBeforeSendIsNotLearned(t *testing.T) {
	old := &budgetBackend{fail: alwaysOverflow}
	a := newRoundAgent(65536, AutonomyDefault, old, io.Discard)
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "go"})
	backend, _, bud := a.backendState()
	a.SetBackend(noopChatBackend{}, BackendLocal, 0)
	if _, err := a.sendRound(context.Background(), backend, bud.gen, "go", nil); err == nil {
		t.Fatal("overflow not returned")
	}
	if b, _ := a.budget(); a.bud.learnedWindow != 0 || b != 65536 {
		t.Fatalf("learned window %d, B %d; the old backend's overflow reached the new one", a.bud.learnedWindow, b)
	}
}

// Forced compaction does not re-summarize only its own earlier summary, so
// a retry is never an equal-size resend.
func TestForcedCompactionSkipsLoneSummary(t *testing.T) {
	b := &budgetBackend{summary: "s"}
	a := newRoundAgent(65536, AutonomyDefault, b, io.Discard)
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: compactedSummaryPrefix + "earlier work"})
	for i := 0; i < 4; i++ {
		addRound(a, fmt.Sprintf("t%d", i), 100)
	}
	if a.shouldCompact(1<<20, 1000, true) {
		t.Fatal("forced compaction would summarize only the earlier summary")
	}
	addRound(a, "t4", 100)
	if !a.shouldCompact(1<<20, 1000, true) {
		t.Fatal("forced compaction skipped with a new message to summarize")
	}
}

// With a fixed part above 70% of a small budget, compaction does not run
// every round: its own earlier summary does not count as freeable.
func TestNoCompactionThrashAtSmallBudget(t *testing.T) {
	var out bytes.Buffer
	b := &budgetBackend{summary: strings.Repeat("s", 3000)}
	a := newRoundAgent(8192, AutonomyFull, b, &out)
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "go"})
	// The fixed part (about 6,500 tokens) is over 70% of U (7,168), so the
	// trigger is true every round. Compaction first runs at round 17, once
	// enough is freeable; its 750-token summary alone passes the 10% guard,
	// so counting it would compact again on every later round.
	for i := 0; i < 20; i++ {
		addRound(a, "t", 400)
		if _, err := a.sendRound(context.Background(), b, a.backendGeneration(), "go", nil); err != nil {
			t.Fatal(err)
		}
	}
	if b.sumCalls != 1 {
		t.Fatalf("summarizer called %d times in 20 rounds, want exactly 1", b.sumCalls)
	}
	if n := strings.Count(out.String(), "sending it anyway"); n != 1 {
		t.Fatalf("over-budget warning shown %d times, want exactly once", n)
	}
}

// When the fixed part alone exceeds the budget, a summary cannot make the
// request fit, so the summarizer is never called.
func TestNoCompactionWhenFixedPartExceedsBudget(t *testing.T) {
	var out bytes.Buffer
	b := &budgetBackend{summary: "s"}
	a := newRoundAgent(1000, AutonomyFull, b, &out)
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "go"})
	for i := 0; i < 10; i++ {
		addRound(a, "t", 300)
		if _, err := a.sendRound(context.Background(), b, a.backendGeneration(), "go", nil); err != nil {
			t.Fatal(err)
		}
	}
	if b.sumCalls != 0 {
		t.Fatalf("summarizer called %d times; the fixed part alone exceeds U", b.sumCalls)
	}
	req := a.buildRequest("go", nil)
	if fixed := a.fixedTokens(req, nil); !strings.Contains(out.String(), fmt.Sprintf("hold about %d", fixed)) {
		t.Fatalf("warning %q does not state the fixed part (%d tokens)", out.String(), fixed)
	}
}

// requirePairedToolResults fails if any tool result is not preceded, within
// its block of results, by the assistant message that called it.
func requirePairedToolResults(t *testing.T, msgs []chat.Message) {
	t.Helper()
	for i, m := range msgs {
		if m.Role != chat.RoleTool {
			continue
		}
		j := i - 1
		for j >= 0 && msgs[j].Role == chat.RoleTool {
			j--
		}
		found := false
		if j >= 0 && msgs[j].Role == chat.RoleAssistant {
			for _, tc := range msgs[j].ToolCalls {
				found = found || tc.ID == m.ToolCallID
			}
		}
		if !found {
			t.Fatalf("tool result %q at %d has no tool call before it", m.ToolCallID, i)
		}
	}
}

// Compaction keeps a tool call and its results together: when the message
// at the cut (len - protectLastTurns) is a tool result, the cut moves back
// so its call stays in the protected tail. Otherwise the request carries an
// orphaned tool result, which strict servers reject.
func TestCompactionKeepsToolCallWithResults(t *testing.T) {
	b := &budgetBackend{summary: "s"}
	a := newRoundAgent(65536, AutonomyDefault, b, io.Discard)
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "go"})
	for i := 0; i < 5; i++ {
		addRound(a, fmt.Sprintf("old%d", i), 100)
	}
	a.conv.Append(chat.Message{Role: chat.RoleAssistant, Content: "done"})
	for i := 0; i < 3; i++ {
		addRound(a, fmt.Sprintf("new%d", i), 100)
	}
	msgs := a.conv.Messages()
	if cut := msgs[len(msgs)-protectLastTurns]; cut.Role != chat.RoleTool {
		t.Fatalf("fixture: message at the cut is %s, want a tool result", cut.Role)
	}
	if err := a.compactContext(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	requirePairedToolResults(t, a.conv.Messages())
	if _, _, _, err := a.CompactManually(context.Background()); err == nil {
		requirePairedToolResults(t, a.conv.Messages())
	}
}

// /context and the footer read the cached size of the last request.
func TestContextTokensReportsLastRequest(t *testing.T) {
	b := &budgetBackend{}
	a := newRoundAgent(32768, AutonomyFull, b, io.Discard)
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "go"})
	convOnly := a.ContextTokens()
	if _, err := a.sendRound(context.Background(), b, a.backendGeneration(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if got := a.ContextTokens(); got != b.sent[0] || got <= convOnly {
		t.Fatalf("ContextTokens = %d, want the sent size %d (above the conversation-only %d)", got, b.sent[0], convOnly)
	}
	if got := a.MaxTokens(); got != 28672 {
		t.Fatalf("MaxTokens = %d, want U = 28672", got)
	}
}
