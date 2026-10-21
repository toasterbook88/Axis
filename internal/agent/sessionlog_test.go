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
	if startedRecords(t, raw)[0].Prompt != "please use token=\"[REDACTED]\" now" {
		t.Fatalf("first prompt = %q", finished[0].Prompt)
	}
	if startedRecords(t, raw)[1].Prompt != "second question" {
		t.Fatalf("second prompt = %q", finished[1].Prompt)
	}
	if finished[0].Stop != "answer" {
		t.Fatalf("stop = %q", finished[0].Stop)
	}
	if finished[0].Answer != "done token=\"[REDACTED]\"" {
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
	for _, rec := range allSessionRecords(t, raw) {
		if rec.Tool == nil {
			continue
		}
		tool := *rec.Tool
		if tool.Name == "echo_secret" && tool.Event == "succeeded" && tool.OK {
			sawTool = true
			if strings.Contains(tool.Summary, sessionLogSecret) {
				t.Fatalf("tool summary leaked the secret: %q", tool.Summary)
			}
		}
	}
	if !sawTool {
		t.Fatalf("finished tools = %+v", allSessionRecords(t, raw))
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
	if len(lateFinished) != 1 || startedRecords(t, late)[0].Prompt != "late" {
		t.Fatalf("late records = %+v", lateFinished)
	}
	if len(earlyFinished) != 1 || startedRecords(t, early)[0].Prompt != "early" {
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
		{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{toolCall("m1", "missing_tool", `{}`)}},
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
	for _, rec := range allSessionRecords(t, raw) {
		if rec.Tool != nil && rec.Tool.Event == "output" {
			truncated = true
			if len(rec.Tool.Output) > sessionLogToolCap || rec.TruncatedBytes["tool.output"] != 50 {
				t.Fatalf("tool cap: %+v", rec)
			}
		}
	}
	if !truncated {
		t.Fatal("tool output absent")
	}

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

func allSessionRecords(t *testing.T, raw []byte) []sessionRecord {
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
		out = append(out, rec)
	}
	return out
}

type failChatBackend struct{}

func (failChatBackend) ChatStream(ctx context.Context, msgs []chat.Message, tools []chat.ToolDef, w io.Writer) (chat.Message, error) {
	return chat.Message{}, errors.New("backend down token=" + sessionLogSecret)
}

func recordsOfKind(t *testing.T, raw []byte, kind string) []sessionRecord {
	t.Helper()
	var out []sessionRecord
	for _, r := range allSessionRecords(t, raw) {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}
func finishedRecords(t *testing.T, raw []byte) []sessionRecord {
	return recordsOfKind(t, raw, "turn.finished")
}
func startedRecords(t *testing.T, raw []byte) []sessionRecord {
	return recordsOfKind(t, raw, "turn.started")
}
