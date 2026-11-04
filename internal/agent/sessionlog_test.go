package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/persist"
)

const sessionLogSecret = "sk-live-SHOULDNOTAPPEAR"

type sessionLogObserver struct {
	called int
}

func (o *sessionLogObserver) TurnStarted(int, int)               {}
func (o *sessionLogObserver) CompactionSkipped(error)            {}
func (o *sessionLogObserver) ToolCalled(string, string, string)  { o.called++ }
func (o *sessionLogObserver) ToolSkipped(string, string, string) {}
func (o *sessionLogObserver) ToolSucceeded(string, string, string, int, time.Duration) {
}
func (o *sessionLogObserver) ToolFailed(string, string, error, time.Duration) {}
func (o *sessionLogObserver) ShellExecuting(string, string, string, string)   {}
func (o *sessionLogObserver) MaxTurnsReached(int)                             {}

func TestDailySessionLogAppendsRedactsAndKeepsTheObserver(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenDailySessionLog(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	day := time.Date(2026, 10, 21, 15, 0, 0, 0, time.FixedZone("UTC-4", -4*3600))
	log.now = func() time.Time { return day }
	log.SetResumed(true)

	obs := &sessionLogObserver{}
	a := newSessionLogAgent(t, log, obs, &scriptedBackend{responses: []chat.Message{
		{
			Role: chat.RoleAssistant,
			ToolCalls: []chat.ToolCall{
				toolCall("c1", "echo_secret", `{"api_key":"`+sessionLogSecret+`"}`),
			},
			UsageTokensIn:  11,
			UsageTokensOut: 4,
		},
		{Role: chat.RoleAssistant, Content: "done token=" + sessionLogSecret, UsageTokensIn: 3, UsageTokensOut: 2},
	}})
	a.tools.allowExtra("echo_secret")
	a.tools.add("echo_secret", "echo", json.RawMessage(`{"type":"object"}`), func(ctx context.Context, args json.RawMessage) (string, error) {
		return "api_key=" + sessionLogSecret, nil
	})

	prompt := "please use token=" + sessionLogSecret + " now"
	if err := a.Run(context.Background(), prompt); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := a.Run(context.Background(), "second question"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if obs.called == 0 {
		t.Fatal("session log replaced the observer; tool call was not delivered")
	}

	path := filepath.Join(dir, "agent-2026-10-21.jsonl")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if info.Mode().Perm() != persist.PrivateFileMode {
		t.Fatalf("file mode = %o, want %o", info.Mode().Perm(), persist.PrivateFileMode)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != persist.PrivateDirMode {
		t.Fatalf("dir mode = %o, want %o", dirInfo.Mode().Perm(), persist.PrivateDirMode)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), sessionLogSecret) {
		t.Fatal("daily log stored a credential-shaped value")
	}
	if strings.Contains(string(raw), "Never fabricate tool results") {
		t.Fatal("daily log stored the system prompt")
	}

	finished := finishedRecords(t, raw)
	if len(finished) != 2 {
		t.Fatalf("finished records = %d, want 2", len(finished))
	}
	if finished[0].Prompt != "please use token=[REDACTED] now" {
		t.Fatalf("first prompt = %q", finished[0].Prompt)
	}
	if finished[1].Prompt != "second question" {
		t.Fatalf("second prompt = %q", finished[1].Prompt)
	}
	if finished[0].Stop != "answer" {
		t.Fatalf("stop = %q", finished[0].Stop)
	}
	if finished[0].Answer != "done token=[REDACTED]" {
		t.Fatalf("answer = %q", finished[0].Answer)
	}
	if !finished[0].Resumed || finished[0].SessionID == "" || finished[0].Day != "2026-10-21" {
		t.Fatalf("record identity = %+v", finished[0])
	}
	if finished[0].TokensIn != 14 || finished[0].TokensOut != 6 {
		t.Fatalf("tokens = %d/%d, want 14/6", finished[0].TokensIn, finished[0].TokensOut)
	}
	if finished[1].TokensIn != 0 || finished[1].TokensOut != 0 {
		t.Fatalf("second turn tokens = %d/%d, want 0/0 (delta)", finished[1].TokensIn, finished[1].TokensOut)
	}
	var sawTool bool
	for _, tool := range finished[0].Tools {
		if tool.Name == "echo_secret" && tool.Event == "succeeded" && tool.OK {
			sawTool = true
			if strings.Contains(tool.Summary, sessionLogSecret) {
				t.Fatalf("tool summary leaked the secret: %q", tool.Summary)
			}
		}
	}
	if !sawTool {
		t.Fatalf("finished tools = %+v", finished[0].Tools)
	}
}

func TestDailySessionLogSplitsOnLocalDay(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenDailySessionLog(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	loc := time.FixedZone("UTC-4", -4*3600)
	log.now = func() time.Time { return time.Date(2026, 10, 21, 23, 30, 0, 0, loc) }
	a := newSessionLogAgent(t, log, nil, &scriptedBackend{responses: []chat.Message{
		{Role: chat.RoleAssistant, Content: "night"},
	}})
	if err := a.Run(context.Background(), "late"); err != nil {
		t.Fatalf("late: %v", err)
	}
	log.now = func() time.Time { return time.Date(2026, 10, 22, 0, 30, 0, 0, loc) }
	a.client = &scriptedBackend{responses: []chat.Message{
		{Role: chat.RoleAssistant, Content: "morning"},
	}}
	if err := a.Run(context.Background(), "early"); err != nil {
		t.Fatalf("early: %v", err)
	}

	late, err := os.ReadFile(filepath.Join(dir, "agent-2026-10-21.jsonl"))
	if err != nil {
		t.Fatalf("late file: %v", err)
	}
	early, err := os.ReadFile(filepath.Join(dir, "agent-2026-10-22.jsonl"))
	if err != nil {
		t.Fatalf("early file: %v", err)
	}
	lateFinished := finishedRecords(t, late)
	earlyFinished := finishedRecords(t, early)
	if len(lateFinished) != 1 || lateFinished[0].Prompt != "late" {
		t.Fatalf("late records = %+v", lateFinished)
	}
	if len(earlyFinished) != 1 || earlyFinished[0].Prompt != "early" {
		t.Fatalf("early records = %+v", earlyFinished)
	}
	if strings.Contains(string(late), "early") || strings.Contains(string(early), `"prompt":"late"`) {
		t.Fatal("a later day rewrote the previous day's log")
	}
}

func TestDailySessionLogRecordsMaxTurnsAndRedactedErrors(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenDailySessionLog(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	day := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	log.now = func() time.Time { return day }

	maxAgent := newSessionLogAgent(t, log, nil, &scriptedBackend{responses: []chat.Message{
		{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{toolCall("call-max", "missing_tool", `{}`)}},
	}})
	maxAgent.maxTurns = 1
	if err := maxAgent.Run(context.Background(), "loop"); err != nil {
		t.Fatalf("max turns run: %v", err)
	}

	errAgent := newSessionLogAgent(t, log, nil, failChatBackend{})
	errRun := errAgent.Run(context.Background(), "boom token="+sessionLogSecret)
	if errRun == nil {
		t.Fatal("expected backend error")
	}

	raw, err := os.ReadFile(filepath.Join(dir, "agent-2026-10-21.jsonl"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), sessionLogSecret) {
		t.Fatal("error record stored a credential-shaped value")
	}
	finished := finishedRecords(t, raw)
	if len(finished) != 2 {
		t.Fatalf("finished = %d, want 2", len(finished))
	}
	if finished[0].Stop != "max_turns" {
		t.Fatalf("max stop = %q", finished[0].Stop)
	}
	if finished[1].Stop != "error" || !strings.Contains(finished[1].Error, "[REDACTED]") {
		t.Fatalf("error record = %+v", finished[1])
	}
}

func TestDailySessionLogCapsToolResults(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenDailySessionLog(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	log.now = func() time.Time { return time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC) }
	a := newSessionLogAgent(t, log, nil, &scriptedBackend{responses: []chat.Message{
		{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{toolCall("b1", "big", `{}`)}},
		{Role: chat.RoleAssistant, Content: "ok"},
	}})
	a.tools.allowExtra("big")
	a.tools.add("big", "big", json.RawMessage(`{"type":"object"}`), func(ctx context.Context, args json.RawMessage) (string, error) {
		return strings.Repeat("y", sessionLogToolCap+50), nil
	})
	if err := a.Run(context.Background(), "read a lot"); err != nil {
		t.Fatalf("run: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "agent-2026-10-21.jsonl"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var truncated bool
	for _, rec := range finishedRecords(t, raw) {
		for _, msg := range rec.Messages {
			if msg.Role == chat.RoleTool && msg.Truncated {
				truncated = true
				if len(msg.Content) > sessionLogToolCap {
					t.Fatalf("tool content len = %d", len(msg.Content))
				}
			}
		}
	}
	if !truncated {
		t.Fatal("tool result was stored without the cap")
	}
}

// A shell start is not a result. Recording it OK=true makes a command that
// later fails look like it already succeeded.
func TestDailySessionLogShellStartIsNotSuccess(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenDailySessionLog(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	log.now = func() time.Time { return time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC) }

	ran := false
	a := New(Config{
		Model: "test-model",
		Backend: &scriptedBackend{responses: []chat.Message{
			{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{
				toolCall("sh1", "run_shell", `{"command":"echo session-log-shell-fail"}`),
			}},
			{Role: chat.RoleAssistant, Content: "shell failed"},
		}},
		MaxTurns:   4,
		MaxTokens:  8192,
		Output:     io.Discard,
		Confirm:    func(_, _ string, _ int) ConfirmResult { return ConfirmYes },
		Autonomy:   AutonomyFull,
		SessionLog: log,
		RunShell: func(context.Context, string) (string, error) {
			ran = true
			return "", errors.New("shell exited 1")
		},
	})
	if err := a.Run(context.Background(), "run the check"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !ran {
		t.Fatal("shell runner was not called")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "agent-2026-10-21.jsonl"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	finished := finishedRecords(t, raw)
	if len(finished) != 1 {
		t.Fatalf("finished records = %d, want 1", len(finished))
	}
	var sawStart, sawFail bool
	for _, tool := range finished[0].Tools {
		if tool.Name == "shell" && tool.Event == "shell" {
			sawStart = true
			if tool.OK {
				t.Fatalf("shell start recorded ok before the command finished: %+v", tool)
			}
		}
		if tool.Name == "run_shell" && tool.Event == "failed" {
			sawFail = true
			if tool.OK {
				t.Fatalf("failed shell recorded ok: %+v", tool)
			}
		}
	}
	if !sawStart || !sawFail {
		t.Fatalf("tools = %+v", finished[0].Tools)
	}
}

// Conversation compaction shortens older tool results to "... [truncated]"
// before the logger's own 4096-byte cap. That earlier cut must still be flagged.
func TestDailySessionLogFlagsConversationCompactedToolResults(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenDailySessionLog(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	log.now = func() time.Time { return time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC) }
	a := New(Config{
		Model:      "test-model",
		Backend:    &scriptedBackend{},
		MaxTurns:   4,
		MaxTokens:  100,
		Output:     io.Discard,
		Confirm:    func(_, _ string, _ int) ConfirmResult { return ConfirmYes },
		SessionLog: log,
		Autonomy:   AutonomyDefault,
	})
	const prompt = "show the file"
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: prompt})
	a.conv.Append(chat.Message{Role: chat.RoleTool, ToolCallID: "t1", Content: strings.Repeat("z", 500)})
	for _, pad := range []string{"p1", "p2", "p3", "final-b"} {
		a.conv.Append(chat.Message{Role: chat.RoleAssistant, Content: pad})
	}
	var stored string
	for _, m := range a.conv.Messages() {
		if m.Role == chat.RoleTool {
			stored = m.Content
		}
	}
	if !strings.HasSuffix(stored, "... [truncated]") {
		t.Fatalf("conversation did not compact the tool result: %q", stored)
	}
	if len(stored) > sessionLogToolCap {
		t.Fatalf("compacted tool result len = %d, above logger cap", len(stored))
	}
	log.finishTurn(a, prompt, nil, 0, 0)

	raw, err := os.ReadFile(filepath.Join(dir, "agent-2026-10-21.jsonl"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	finished := finishedRecords(t, raw)
	if len(finished) != 1 {
		t.Fatalf("finished records = %d, want 1", len(finished))
	}
	var saw bool
	for _, msg := range finished[0].Messages {
		if msg.Role != chat.RoleTool {
			continue
		}
		saw = true
		if !strings.HasSuffix(msg.Content, "... [truncated]") {
			t.Fatalf("logged tool content = %q", msg.Content)
		}
		if !msg.Truncated {
			t.Fatal("conversation-compacted tool result was logged truncated=false")
		}
	}
	if !saw {
		t.Fatal("finished transcript omitted the tool result")
	}
}

// Agent compaction deletes the user message whose text equals the prompt.
// The finished record must still keep the protected tail and the answer.
// The prompt field already stores the original prompt on its own.
func TestDailySessionLogKeepsAnswerAfterPromptCompaction(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenDailySessionLog(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	log.now = func() time.Time { return time.Date(2026, 10, 21, 11, 0, 0, 0, time.UTC) }
	a := newSessionLogAgent(t, log, nil, &scriptedBackend{})
	const prompt = "original operator prompt"
	const answer = "surviving answer"
	a.conv.Append(chat.Message{Role: chat.RoleUser, Content: prompt})
	for i := 0; i < protectLastTurns-1; i++ {
		a.conv.Append(chat.Message{Role: chat.RoleAssistant, Content: "older note"})
	}
	a.conv.Append(chat.Message{Role: chat.RoleAssistant, Content: answer})
	start := a.conv.FirstNonSystemIndex()
	end := a.conv.Len() - protectLastTurns
	if end <= start {
		t.Fatalf("compaction range %d:%d does not cover the prompt", start, end)
	}
	a.conv.ReplaceRange(start, end, []chat.Message{{
		Role:    chat.RoleUser,
		Content: compactedSummaryPrefix + "kept facts",
	}})
	for _, m := range a.conv.Messages() {
		if m.Content == prompt {
			t.Fatal("prompt survived compaction; this test did not reproduce the miss")
		}
	}
	log.finishTurn(a, prompt, nil, 0, 0)

	raw, err := os.ReadFile(filepath.Join(dir, "agent-2026-10-21.jsonl"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	finished := finishedRecords(t, raw)
	if len(finished) != 1 {
		t.Fatalf("finished records = %d, want 1", len(finished))
	}
	if finished[0].Prompt != prompt {
		t.Fatalf("prompt field = %q", finished[0].Prompt)
	}
	if finished[0].Answer != answer {
		t.Fatalf("answer = %q, want %q", finished[0].Answer, answer)
	}
	if len(finished[0].Messages) == 0 {
		t.Fatal("transcript empty after the prompt message was compacted away")
	}
	var sawAnswer bool
	for _, msg := range finished[0].Messages {
		if msg.Role == chat.RoleAssistant && msg.Content == answer {
			sawAnswer = true
		}
	}
	if !sawAnswer {
		t.Fatalf("transcript = %+v", finished[0].Messages)
	}
}

// A failed model call rolls the new prompt back out of the conversation.
// That miss must not reuse the previous turn's answer.
func TestDailySessionLogRollbackDoesNotCopyPreviousAnswer(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenDailySessionLog(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	log.now = func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }
	a := newSessionLogAgent(t, log, nil, &scriptedBackend{responses: []chat.Message{
		{Role: chat.RoleAssistant, Content: "first answer"},
	}})
	if err := a.Run(context.Background(), "first question"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	a.SetBackend(failChatBackend{}, 0)
	if err := a.Run(context.Background(), "second question"); err == nil {
		t.Fatal("second run succeeded; rollback was not exercised")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "agent-2026-10-21.jsonl"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	finished := finishedRecords(t, raw)
	if len(finished) != 2 {
		t.Fatalf("finished records = %d, want 2", len(finished))
	}
	if finished[0].Answer != "first answer" {
		t.Fatalf("first answer = %q", finished[0].Answer)
	}
	if finished[1].Prompt != "second question" {
		t.Fatalf("second prompt = %q", finished[1].Prompt)
	}
	if finished[1].Answer != "" {
		t.Fatalf("rolled-back turn copied an answer: %q", finished[1].Answer)
	}
	if len(finished[1].Messages) != 0 {
		t.Fatalf("rolled-back transcript = %+v", finished[1].Messages)
	}
}

// Compaction rewrites earlier messages before the first model call returns.
// The saved prompt index is then stale. A failed call must still drop the
// prompt, and the log must not treat the older summary as this turn.
func TestChatStreamErrorAfterCompactionDropsThePrompt(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenDailySessionLog(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	log.now = func() time.Time { return time.Date(2026, 10, 21, 13, 0, 0, 0, time.UTC) }
	const prompt = "fresh-prompt-must-drop"
	a := New(Config{
		Model:      "test-model",
		Backend:    summaryThenFailBackend{summary: "SUMMARY: older turns"},
		MaxTurns:   2,
		MaxTokens:  200,
		Output:     io.Discard,
		Confirm:    func(_, _ string, _ int) ConfirmResult { return ConfirmYes },
		SessionLog: log,
		Autonomy:   AutonomyDefault,
	})
	big := strings.Repeat("tool output content ", 40)
	for i := 0; i < 6; i++ {
		a.conv.Append(chat.Message{Role: chat.RoleUser, Content: "do thing " + string(rune('a'+i))})
		a.conv.Append(chat.Message{Role: chat.RoleAssistant, Content: "ok"})
		a.conv.Append(chat.Message{Role: chat.RoleTool, ToolCallID: "c", Content: big})
	}
	if err := a.Run(context.Background(), prompt); err == nil {
		t.Fatal("run succeeded; model failure was not exercised")
	}
	var sawSummary, sawPrompt bool
	for _, m := range a.conv.Messages() {
		if m.Role == chat.RoleUser && strings.HasPrefix(m.Content, compactedSummaryPrefix) {
			sawSummary = true
		}
		if m.Content == prompt {
			sawPrompt = true
		}
	}
	if !sawSummary {
		t.Fatal("compaction did not run; the prompt index was not shifted")
	}
	if sawPrompt {
		t.Fatal("failed model call left the prompt in the conversation")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "agent-2026-10-21.jsonl"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	finished := finishedRecords(t, raw)
	if len(finished) != 1 {
		t.Fatalf("finished records = %d, want 1", len(finished))
	}
	if finished[0].Prompt != prompt {
		t.Fatalf("prompt field = %q", finished[0].Prompt)
	}
	if finished[0].Stop != "error" {
		t.Fatalf("stop = %q", finished[0].Stop)
	}
	if finished[0].Answer != "" || len(finished[0].Messages) != 0 {
		t.Fatalf("failed turn reused older transcript: answer=%q messages=%+v", finished[0].Answer, finished[0].Messages)
	}
}

type summaryThenFailBackend struct {
	summary string
}

func (b summaryThenFailBackend) ChatStream(ctx context.Context, msgs []chat.Message, tools []chat.ToolDef, w io.Writer) (chat.Message, error) {
	if tools == nil {
		return chat.Message{Role: chat.RoleAssistant, Content: b.summary}, nil
	}
	return chat.Message{}, errors.New("model down")
}

func newSessionLogAgent(t *testing.T, log *DailySessionLog, obs Observer, backend ChatBackend) *Agent {
	t.Helper()
	return New(Config{
		Model:      "test-model",
		Backend:    backend,
		MaxTurns:   4,
		MaxTokens:  8192,
		Output:     io.Discard,
		Confirm:    func(_, _ string, _ int) ConfirmResult { return ConfirmYes },
		Observer:   obs,
		SessionLog: log,
		Autonomy:   AutonomyDefault,
	})
}

func finishedRecords(t *testing.T, raw []byte) []sessionRecord {
	t.Helper()
	var out []sessionRecord
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var rec sessionRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		if rec.Kind == "turn.finished" {
			out = append(out, rec)
		}
	}
	return out
}

type failChatBackend struct{}

func (failChatBackend) ChatStream(ctx context.Context, msgs []chat.Message, tools []chat.ToolDef, w io.Writer) (chat.Message, error) {
	return chat.Message{}, errors.New("backend down token=" + sessionLogSecret)
}
