package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/lockutil"
)

func readSessionSegments(t testing.TB, dir string) ([]byte, int64, int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var size int64
	var count int
	for _, entry := range entries {
		if !sessionSegmentName.MatchString(entry.Name()) {
			continue
		}
		data, e := os.ReadFile(filepath.Join(dir, entry.Name()))
		if e != nil {
			t.Fatal(e)
		}
		raw = append(raw, data...)
		size += int64(len(data))
		count++
	}
	return raw, size, count
}

func TestDailySessionLogQuotaRotationAgeAndMidnight(t *testing.T) {
	dir := t.TempDir()
	l, err := OpenDailySessionLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.policy = sessionLogPolicy{totalBytes: 3000, segmentBytes: 1000, days: 2}
	now := time.Date(2026, 10, 21, 23, 59, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	old := filepath.Join(dir, "agent-2026-10-19.jsonl")
	if err = os.WriteFile(old, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(dir, "task-example.log")
	if err = os.WriteFile(unrelated, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	a := newSessionLogAgent(t, l, nil, &scriptedBackend{})
	l.beginTurn(a, "cross-midnight")
	now = now.Add(2 * time.Minute)
	l.finishTurn(a, "cross-midnight", nil, 0, 0)
	records, _, _ := readSessionSegments(t, dir)
	finished := finishedRecords(t, records)
	if len(finished) != 1 || finished[0].Day != "2026-10-21" {
		t.Fatalf("turn day lost: %+v", finished)
	}
	if _, err = os.Stat(filepath.Join(dir, "agent-2026-10-22.jsonl")); err != nil {
		t.Fatal("midnight write must use current-day segment:", err)
	}
	for i := 0; i < 30; i++ {
		l.append(sessionRecord{Kind: "turn.started", Prompt: strings.Repeat("x", 200)})
	}
	_, size, count := readSessionSegments(t, dir)
	if l.dropped != 0 || size > l.policy.totalBytes || count < 2 {
		t.Fatalf("dropped=%d bytes=%d segments=%d", l.dropped, size, count)
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if !sessionSegmentName.MatchString(entry.Name()) {
			continue
		}
		info, _ := entry.Info()
		if info.Size() > 1000 || info.Mode().Perm() != 0600 {
			t.Fatalf("unbounded or public segment: %s %d %o", entry.Name(), info.Size(), info.Mode())
		}
	}
	if _, err = os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("age retention: %v", err)
	}
	data, err := os.ReadFile(unrelated)
	if err != nil || string(data) != "keep" {
		t.Fatal("retention touched unrelated logs")
	}
}

func TestDailySessionLogBoundsAndRetainsNormalText(t *testing.T) {
	normal := strings.Repeat("normal prompt. ", 1500)
	raw, err := encodeSessionRecord(sessionRecord{Kind: "turn.started", Prompt: normal})
	if err != nil {
		t.Fatal(err)
	}
	var rec sessionRecord
	if err = json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Prompt != normal || len(rec.TruncatedBytes) != 0 {
		t.Fatal("ordinary prompt truncated")
	}
	giant := strings.Repeat("界", sessionLogTextCap)
	raw, err = encodeSessionRecord(sessionRecord{Kind: "turn.started", Prompt: giant})
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(rec.Prompt) || len(rec.Prompt) > sessionLogTextCap || rec.TruncatedBytes["prompt"] != len(giant)-len(rec.Prompt) {
		t.Fatalf("bad truncation: len=%d omitted=%v", len(rec.Prompt), rec.TruncatedBytes)
	}
	raw, err = encodeSessionRecord(sessionRecord{Kind: "tool", Tool: &sessionTool{Output: strings.Repeat("y", sessionLogToolCap+30)}})
	if err != nil {
		t.Fatal(err)
	}
	rec = sessionRecord{}
	if err = json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if len(raw) > sessionLogRecordCap || rec.TruncatedBytes["tool.output"] != 30 || len(rec.Tool.Output) != sessionLogToolCap {
		t.Fatalf("output cap wrong: %+v", rec)
	}
	// Escaping can expand JSON far beyond the input bytes; it is still bounded.
	raw, err = encodeSessionRecord(sessionRecord{Kind: "turn.started", Prompt: strings.Repeat("\u0085", sessionLogTextCap), Answer: strings.Repeat("\u2028", sessionLogTextCap)})
	if err != nil || len(raw) > sessionLogRecordCap {
		t.Fatalf("escape expansion: %d %v", len(raw), err)
	}
}

func TestDailySessionLogRedactsCredentialsAcrossPayloads(t *testing.T) {
	for _, text := range []string{
		`discovery.secret: "sensitive value with spaces"`,
		`DISCOVERY_SECRET='sensitive value with spaces'`,
		`{"discovery":{"secret":"sensitive value with spaces"},"nested":{"api_key":"sensitive value with spaces"}}`,
		"Authorization: Basic c2Vuc2l0aXZlOnZhbHVl\nordinary text",
		`{"headers":{"Authorization":"Bearer sensitive value with spaces"}}`,
		`password="escaped \" sensitive value with spaces"`,
	} {
		t.Run(text, func(t *testing.T) {
			raw, err := encodeSessionRecord(sessionRecord{Kind: "tool", Prompt: text, Answer: text, Error: text, Tool: &sessionTool{Args: text, Summary: text, Error: text}})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "sensitive") || strings.Contains(string(raw), "c2Vuc2l0aXZlOnZhbHVl") {
				t.Fatalf("credential leaked: %s", raw)
			}
			if !bytes.Contains(raw, []byte("REDACTED")) {
				t.Fatal("redaction not explicit")
			}
		})
	}
}

func TestDailySessionLogRejectsSymlinksAndWarnsWithoutFailingTurn(t *testing.T) {
	outside := t.TempDir()
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "logs")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDailySessionLog(link); err == nil {
		t.Fatal("accepted directory symlink")
	}
	l, err := OpenDailySessionLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	target := filepath.Join(outside, "do-not-write")
	if err = os.WriteFile(target, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, filepath.Join(dir, "agent-2026-10-21.jsonl")); err != nil {
		t.Fatal(err)
	}
	warns := 0
	l.SetWarn(func(s string) {
		warns++
		if !strings.Contains(s, "omitted") {
			t.Errorf("unclear warning: %s", s)
		}
	})
	a := newSessionLogAgent(t, l, nil, &scriptedBackend{responses: []chat.Message{{Role: chat.RoleAssistant, Content: "answer survives"}}})
	if err = a.Run(context.Background(), "hello"); err != nil {
		t.Fatal("logger changed Run result:", err)
	}
	if warns != 1 || l.dropped != 2 {
		t.Fatalf("warning/loss counts=%d/%d", warns, l.dropped)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "safe" {
		t.Fatal("followed symlink")
	}
	if err = os.Remove(filepath.Join(dir, "agent-2026-10-21.jsonl")); err != nil {
		t.Fatal(err)
	}
	l.append(sessionRecord{Kind: "recovered"})
	data, _, _ = readSessionSegments(t, dir)
	records := allSessionRecords(t, data)
	if len(records) != 1 || records[0].Dropped != 2 {
		t.Fatalf("loss invisible after recovery: %+v", records)
	}
}

func TestDailySessionLogLockTimeoutAndCancellation(t *testing.T) {
	dir := t.TempDir()
	l, err := OpenDailySessionLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := lockutil.OpenLock(filepath.Join(dir, ".agent-session.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = lock.LockEx(); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	l.append(sessionRecord{Kind: "blocked"})
	elapsed := time.Since(started)
	if elapsed > time.Second || l.dropped != 1 {
		t.Fatalf("unbounded lock wait: %v dropped=%d", elapsed, l.dropped)
	}
	if err = lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	a := newSessionLogAgent(t, l, nil, &sessionCanceledBackend{})
	if err = a.Run(context.Background(), "cancel"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation changed: %v", err)
	}
	raw, _, _ := readSessionSegments(t, dir)
	finished := finishedRecords(t, raw)
	if len(finished) != 1 || finished[0].Stop != "canceled" {
		t.Fatalf("cancel outcome: %+v", finished)
	}
}

type sessionCanceledBackend struct{}

func (*sessionCanceledBackend) ChatStream(context.Context, []chat.Message, []chat.ToolDef, io.Writer) (chat.Message, error) {
	return chat.Message{}, context.Canceled
}

func TestDailySessionLogConcurrentProcesses(t *testing.T) {
	if dir := os.Getenv("AXIS_SESSIONLOG_TEST_CHILD"); dir != "" {
		l, err := OpenDailySessionLog(dir)
		if err != nil {
			t.Fatal(err)
		}
		l.SetWarn(func(message string) { t.Log("child log warning:", message) })
		l.policy = sessionLogPolicy{totalBytes: 1 << 20, segmentBytes: 2048, days: 30}
		for i := 0; i < 25; i++ {
			l.append(sessionRecord{Kind: "child", Prompt: fmt.Sprintf("%d %d", os.Getpid(), i)})
		}
		if l.dropped != 0 {
			t.Fatal("child dropped records", l.dropped)
		}
		return
	}
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestDailySessionLogConcurrentProcesses$")
			cmd.Env = append(os.Environ(), "AXIS_SESSIONLOG_TEST_CHILD="+dir)
			if raw, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("child: %v %s", err, raw)
			}
		}()
	}
	wg.Wait()
	raw, size, count := readSessionSegments(t, dir)
	if records := allSessionRecords(t, raw); len(records) != 100 || size > 1<<20 || count < 2 {
		t.Fatalf("lost/interleaved records: count=%d bytes=%d segments=%d", len(records), size, count)
	}
}

func BenchmarkDailySessionLogToolAppend(b *testing.B) {
	l, err := OpenDailySessionLog(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	rec := sessionRecord{Kind: "tool", Tool: &sessionTool{Name: "example", Args: strings.Repeat("x", 1000)}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.append(rec)
	}
	b.StopTimer()
	if l.dropped != 0 {
		b.Fatal("dropped records", l.dropped)
	}
}

func TestDailySessionLogTrajectorySurvivesConversationRemoval(t *testing.T) {
	l, err := OpenDailySessionLog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := newSessionLogAgent(t, l, nil, &scriptedBackend{})
	l.beginTurn(a, "one-copy-prompt")
	l.noteCalled("call-1", "example", `{"ordinary":"argument"}`)
	l.noteResponse(chat.Message{Role: chat.RoleAssistant, Content: "intermediate explanation", ToolCalls: []chat.ToolCall{toolCall("call-1", "example", `{}`)}})
	l.noteToolOutput("call-1", "example", "one-copy-tool-output", true)
	// Compaction or the backend error rollback can remove every original message.
	a.conv.ReplaceRange(0, a.conv.Len(), nil)
	l.noteResponse(chat.Message{Role: chat.RoleAssistant, Content: "one-copy-answer"})
	l.finishTurn(a, "one-copy-prompt", nil, 0, 0)
	raw, _, _ := readSessionSegments(t, l.dir)
	for _, s := range []string{"one-copy-prompt", "one-copy-tool-output", "one-copy-answer", "intermediate explanation"} {
		if count := bytes.Count(raw, []byte(s)); count != 1 {
			t.Fatalf("%s occurs %d times", s, count)
		}
	}
}

func TestDailySessionLogUpdatesModelAttribution(t *testing.T) {
	l, err := OpenDailySessionLog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l.SetProvenance(SessionLogProvenance{Provider: "provider-a", TargetModel: "test-model", ProjectCommit: "abc123", CWD: "/work/example"})
	a := newSessionLogAgent(t, l, nil, &scriptedBackend{})
	a.SetModel("model-b")
	a.SetSessionLogTarget(ModelTarget{Model: "model-b", ProviderName: "provider-b", Protocol: ProtocolOpenAI, Node: "node.example"})
	l.beginTurn(a, "check switched provider")
	raw, _, _ := readSessionSegments(t, l.dir)
	records := startedRecords(t, raw)
	if len(records) != 1 || records[0].Model != "model-b" || records[0].Provenance.Provider != "provider-b" || records[0].Provenance.ProjectCommit != "abc123" {
		t.Fatalf("stale or discarded provenance: %+v", records)
	}
}

func TestDailySessionLogProvenanceIsObservedAndBounded(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	git("init", "-q")
	if err := os.WriteFile("example.txt", []byte("example"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "example.txt")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "test")
	want := git("rev-parse", "HEAD")
	p := CaptureSessionLogProvenance(ModelTarget{Model: "example-model", ProviderName: "example-provider"})
	if p.ProjectCommit != want || p.ProjectDirty == nil || *p.ProjectDirty || p.CapturedAt == "" || p.CWD != dir {
		t.Fatalf("unobserved provenance: %+v", p)
	}
	if err := os.WriteFile("new.txt", []byte("untracked"), 0600); err != nil {
		t.Fatal(err)
	}
	p = CaptureSessionLogProvenance(ModelTarget{})
	if p.ProjectDirty == nil || !*p.ProjectDirty {
		t.Fatal("untracked changes incorrectly called clean")
	}
	// An unavailable or hung git probe is unknown and stays bounded.
	fake := t.TempDir()
	if err := os.WriteFile(filepath.Join(fake, "git"), []byte("#!/bin/sh\nexec /bin/sleep 5\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fake)
	start := time.Now()
	p = CaptureSessionLogProvenance(ModelTarget{})
	if elapsed := time.Since(start); elapsed > time.Second || p.ProjectCommit != "" || p.ProjectDirty != nil {
		t.Fatalf("hung probe not bounded: %v %+v", elapsed, p)
	}
}

func TestDailySessionLogAcceptsCanonicalParentAlias(t *testing.T) {
	parent := t.TempDir()
	alias := filepath.Join(t.TempDir(), "parent-alias")
	if err := os.Symlink(parent, alias); err != nil {
		t.Fatal(err)
	}
	l, err := OpenDailySessionLog(filepath.Join(alias, "logs"))
	if err != nil {
		t.Fatalf("macOS-style parent alias rejected: %v", err)
	}
	l.append(sessionRecord{Kind: "alias"})
	if l.dropped != 0 {
		t.Fatal("alias write failed")
	}
	_, _, count := readSessionSegments(t, filepath.Join(parent, "logs"))
	if count != 1 {
		t.Fatal("did not use canonical directory")
	}
}

func TestDailySessionLogRotationNeverReusesPrunedSuffix(t *testing.T) {
	dir := t.TempDir()
	l, err := OpenDailySessionLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	l.policy = sessionLogPolicy{totalBytes: 3000, segmentBytes: 1000, days: 30}
	for _, suffix := range []string{"000001", "000003"} {
		if err = os.WriteFile(filepath.Join(dir, "agent-2026-10-21-"+suffix+".jsonl"), bytes.Repeat([]byte("x"), 1000), 0600); err != nil {
			t.Fatal(err)
		}
	}
	l.append(sessionRecord{Kind: "newest", Prompt: strings.Repeat("n", 100)})
	if _, err = os.Stat(filepath.Join(dir, "agent-2026-10-21-000004.jsonl")); err != nil {
		t.Fatal("rotation reused low suffix:", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "agent-2026-10-21-000002.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("filled suffix gap")
	}
}

func FuzzDailySessionLogEncoder(f *testing.F) {
	for _, seed := range []string{"ordinary prompt", "discovery.secret=example-secret", `{"nested":{"token":"example-secret"}}`, `{"content":"{\"api_key\":\"example-secret\"}"}`, "partial {\"api_key\":", strings.Repeat("界", 100)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 512<<10 {
			return
		}
		raw, err := encodeSessionRecord(sessionRecord{Kind: "tool", Prompt: text, Answer: text, Tool: &sessionTool{Output: text, Args: text}})
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > sessionLogRecordCap || !json.Valid(raw) || !utf8.Valid(raw) {
			t.Fatal("invalid or unbounded record")
		}
	})
}

func TestDailySessionLogRetriesOnlyCompetingLockCreation(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	calls := 0
	open := func(name string, flags int, mode os.FileMode) (*os.File, error) {
		calls++
		if calls == 1 {
			winner, e := root.OpenFile(name, os.O_CREATE|os.O_RDWR, mode)
			if e != nil {
				t.Fatal(e)
			}
			_ = winner.Close()
			return nil, &os.PathError{Op: "openat", Path: name, Err: os.ErrNotExist}
		}
		if flags&os.O_CREATE != 0 {
			t.Fatal("retry retained creation flag")
		}
		return root.OpenFile(name, flags, mode)
	}
	f, err := privateSessionFileWithOpen(root, ".agent-session.lock", os.O_CREATE|os.O_RDWR, open)
	if err != nil || calls != 2 {
		t.Fatalf("recognized Darwin create race: calls=%d err=%v", calls, err)
	}
	if f != nil {
		_ = f.Close()
	}
	for _, tc := range []struct {
		name    string
		failure error
	}{{"absent.jsonl", os.ErrNotExist}, {".agent-session.lock", os.ErrPermission}} {
		calls = 0
		_, err = privateSessionFileWithOpen(root, tc.name, os.O_CREATE|os.O_RDWR, func(name string, flags int, mode os.FileMode) (*os.File, error) { calls++; return nil, tc.failure })
		if !errors.Is(err, tc.failure) || calls != 1 {
			t.Fatalf("unrecognized error retried: %s %v %d", tc.name, err, calls)
		}
	}
	// ENOENT while the lock really is absent must remain a visible failure.
	if err = root.Remove(".agent-session.lock"); err != nil {
		t.Fatal(err)
	}
	calls = 0
	_, err = privateSessionFileWithOpen(root, ".agent-session.lock", os.O_CREATE|os.O_RDWR, func(string, int, os.FileMode) (*os.File, error) { calls++; return nil, os.ErrNotExist })
	if !errors.Is(err, os.ErrNotExist) || calls != 1 {
		t.Fatal("absent lock error swallowed")
	}
}
