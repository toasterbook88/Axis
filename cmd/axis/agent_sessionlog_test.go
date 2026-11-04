package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/chat"
)

type resumeTestBackend struct{}

func (resumeTestBackend) ChatStream(context.Context, []chat.Message, []chat.ToolDef, io.Writer) (chat.Message, error) {
	return chat.Message{Role: chat.RoleAssistant, Content: "ok"}, nil
}

func newResumeTestAgent(t *testing.T, logDir string) (*agent.Agent, *agent.DailySessionLog) {
	t.Helper()
	log, err := agent.OpenDailySessionLog(logDir)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	a := agent.New(agent.Config{
		Model:      "test-model",
		Backend:    resumeTestBackend{},
		MaxTurns:   1,
		MaxTokens:  8192,
		Output:     io.Discard,
		SessionLog: log,
	})
	return a, log
}

// loggedResumed runs one turn and returns the resumed flag of every record.
func loggedResumed(t *testing.T, a *agent.Agent, logDir string) []bool {
	t.Helper()
	if err := a.Run(context.Background(), "next question"); err != nil {
		t.Fatalf("run: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(logDir, "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatalf("log files = %v, err %v", files, err)
	}
	var out []bool
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var rec struct {
				Resumed bool `json:"resumed"`
			}
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("decode %q: %v", line, err)
			}
			out = append(out, rec.Resumed)
		}
	}
	return out
}

// --resume marks the session log resumed only when prior messages loaded.
// A missing or unreadable history must not label a fresh run.
func TestResumeConversationMarksLogOnlyWhenHistoryLoads(t *testing.T) {
	dir := t.TempDir()
	saved := filepath.Join(dir, "saved.json")
	conv := chat.NewConversation(8192)
	conv.Append(chat.Message{Role: chat.RoleUser, Content: "earlier question"})
	conv.Append(chat.Message{Role: chat.RoleAssistant, Content: "earlier answer"})
	if err := conv.SaveToFile(saved); err != nil {
		t.Fatalf("save history: %v", err)
	}
	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	cases := []struct {
		name        string
		path        string
		wantResumed bool
		wantErr     bool
	}{
		{name: "loaded", path: saved, wantResumed: true},
		{name: "missing", path: filepath.Join(dir, "missing.json")},
		{name: "corrupt", path: corrupt, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logDir := filepath.Join(t.TempDir(), "logs")
			a, log := newResumeTestAgent(t, logDir)
			n, err := resumeConversation(a, tc.path, log)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantResumed && n == 0 {
				t.Fatal("history loaded no messages")
			}
			flags := loggedResumed(t, a, logDir)
			if len(flags) == 0 {
				t.Fatal("no session log records")
			}
			for _, got := range flags {
				if got != tc.wantResumed {
					t.Fatalf("resumed = %v, want %v (records %v)", got, tc.wantResumed, flags)
				}
			}
		})
	}
}

func TestResumeConversationToleratesNilLog(t *testing.T) {
	a, _ := newResumeTestAgent(t, filepath.Join(t.TempDir(), "logs"))
	if _, err := resumeConversation(a, filepath.Join(t.TempDir(), "missing.json"), nil); err != nil {
		t.Fatalf("nil log: %v", err)
	}
}
