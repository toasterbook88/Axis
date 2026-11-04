package agent

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/persist"
)

// sessionLogToolCap bounds a tool result copied into the daily log.
// User and assistant text are kept whole. Tool output is capped because a
// single read can be larger than the conversation it came from.
const sessionLogToolCap = 4096

// chatCompactedToolSuffix is the ending Conversation.compactOldest writes
// onto an older tool result. It is shorter than sessionLogToolCap, so the
// logger cap alone does not see the cut.
const chatCompactedToolSuffix = "... [truncated]"

// DailySessionLog appends one private JSONL file per local day under dir:
//
//	<dir>/agent-YYYY-MM-DD.jsonl
//
// mode 0600. The CLI passes ~/.axis/logs. The file is the evaluation corpus.
// Lines are one of:
//
//	turn.started   the prompt, written before the model runs
//	tool           one redacted tool event, written as it happens
//	turn.finished  prompt, answer, redacted messages, tool results, tokens
//
// A later day is a new file. A second turn appends. Nothing here rewrites
// agent-history.json. Writes are best-effort: a log failure does not fail
// the turn. The first write error is reported through SetWarn.
//
// This is not an Observer. The UI observer keeps receiving loop events.
// Assistant text never enters Observer, so the finished record is written
// from the runner after Run returns.
type DailySessionLog struct {
	dir       string
	sessionID string
	resumed   bool
	warn      func(string)
	now       func() time.Time

	mu        sync.Mutex
	turn      int
	turnStart time.Time
	model     string
	autonomy  string
	day       string
	tools     []sessionTool
	maxTurns  bool
	warned    bool
	omitBody  bool
}

// OpenDailySessionLog creates dir at mode 0700 and returns a logger that
// appends agent-YYYY-MM-DD.jsonl inside it. dir is the logs directory, not
// the JSONL path.
func OpenDailySessionLog(dir string) (*DailySessionLog, error) {
	if err := persist.EnsurePrivateDir(dir); err != nil {
		return nil, fmt.Errorf("session log dir: %w", err)
	}
	return &DailySessionLog{
		dir:       dir,
		sessionID: newSessionID(),
	}, nil
}

// SetWarn installs a best-effort error sink. The first write failure is
// reported once. A nil logger ignores the call.
func (l *DailySessionLog) SetWarn(fn func(string)) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.warn = fn
	l.mu.Unlock()
}

// SetResumed marks every record from this process as a resume of the
// interactive history file. It does not copy that file into the log.
func (l *DailySessionLog) SetResumed(resumed bool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.resumed = resumed
	l.mu.Unlock()
}

func (l *DailySessionLog) beginTurn(a *Agent, prompt string) {
	if l == nil || a == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.turn++
	l.omitBody = false
	l.turnStart = l.clock()
	l.day = l.turnStart.Format("2006-01-02")
	l.model = a.Model()
	l.autonomy = string(a.Autonomy())
	l.tools = nil
	l.maxTurns = false
	l.writeLocked(sessionRecord{
		Kind:     "turn.started",
		Turn:     l.turn,
		Model:    l.model,
		Autonomy: l.autonomy,
		Prompt:   redactForSurface(prompt),
		Resumed:  l.resumed,
	})
}

func (l *DailySessionLog) finishTurn(a *Agent, prompt string, runErr error, startIn, startOut int) {
	if l == nil || a == nil {
		return
	}
	l.mu.Lock()
	turn := l.turn
	started := l.turnStart
	hitMax := l.maxTurns
	tools := append([]sessionTool(nil), l.tools...)
	resumed := l.resumed
	omit := l.omitBody
	l.omitBody = false
	l.mu.Unlock()

	stop := "answer"
	errText := ""
	if runErr != nil {
		stop = "error"
		errText, _ = clipSessionText(redactForSurface(runErr.Error()), 1024)
	} else if hitMax {
		stop = "max_turns"
	}
	in, out, _ := a.UsageStats()
	dIn, dOut := in-startIn, out-startOut
	if dIn < 0 {
		dIn = 0
	}
	if dOut < 0 {
		dOut = 0
	}
	var msgs []sessionMessage
	var answer string
	// A turn-0 model failure rolls the prompt back. The summary then belongs
	// to older turns, so this record keeps the prompt and the error only.
	if !omit {
		msgs, answer = turnTranscript(a, prompt)
	}
	l.append(sessionRecord{
		Kind:      "turn.finished",
		Turn:      turn,
		Model:     a.Model(),
		Autonomy:  string(a.Autonomy()),
		ElapsedMS: l.clock().Sub(started).Milliseconds(),
		Stop:      stop,
		Error:     errText,
		TokensIn:  dIn,
		TokensOut: dOut,
		Prompt:    redactForSurface(prompt),
		Answer:    redactForSurface(answer),
		Messages:  msgs,
		Tools:     tools,
		Resumed:   resumed,
	})
}

func (l *DailySessionLog) noteCalled(id, name, args string) {
	l.noteLive(sessionTool{ID: id, Name: name, Event: "called", Args: args})
}

func (l *DailySessionLog) noteSkipped(id, name, reason string) {
	l.noteResult(sessionTool{ID: id, Name: name, Event: "skipped", Error: reason})
}

func (l *DailySessionLog) noteSucceeded(id, name, summary string, resultLen int, elapsed time.Duration) {
	l.noteResult(sessionTool{
		ID:          id,
		Name:        name,
		Event:       "succeeded",
		OK:          true,
		LatencyMS:   elapsed.Milliseconds(),
		ResultBytes: resultLen,
		Summary:     summary,
	})
}

func (l *DailySessionLog) noteFailed(id, name string, err error, elapsed time.Duration) {
	msg := ""
	if err != nil {
		msg, _ = clipSessionText(redactForSurface(err.Error()), 1024)
	}
	l.noteResult(sessionTool{
		ID:        id,
		Name:      name,
		Event:     "failed",
		LatencyMS: elapsed.Milliseconds(),
		Error:     msg,
	})
}

func (l *DailySessionLog) noteShell(id, node, cwd, command string) {
	summary := ""
	if node != "" {
		summary = "node=" + node
	} else if cwd != "" {
		summary = "cwd=" + cwd
	}
	// OK stays false: this event is the start, not the result. Success and
	// failure are later noteSucceeded / noteFailed records.
	l.noteResult(sessionTool{ID: id, Name: "shell", Event: "shell", Args: command, Summary: summary})
}

// omitTranscript drops the copied conversation from the finished record.
// The prompt and the error are still stored. A nil logger ignores the call.
func (l *DailySessionLog) omitTranscript() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.omitBody = true
	l.mu.Unlock()
}

func (l *DailySessionLog) markMaxTurns() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.maxTurns = true
	l.mu.Unlock()
}

func (l *DailySessionLog) noteLive(tool sessionTool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := l.baseLocked()
	rec.Kind = "tool"
	rec.Tool = &tool
	l.writeLocked(rec)
}

func (l *DailySessionLog) noteResult(tool sessionTool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tools = append(l.tools, tool)
	rec := l.baseLocked()
	rec.Kind = "tool"
	rec.Tool = &tool
	l.writeLocked(rec)
}

func (l *DailySessionLog) append(rec sessionRecord) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writeLocked(rec)
}

func (l *DailySessionLog) baseLocked() sessionRecord {
	return sessionRecord{
		Turn:     l.turn,
		Model:    l.model,
		Autonomy: l.autonomy,
		Resumed:  l.resumed,
	}
}

func (l *DailySessionLog) writeLocked(rec sessionRecord) {
	now := l.clock()
	rec.SessionID = l.sessionID
	rec.TS = now.Format(time.RFC3339)
	// A turn that crosses midnight stays in the file it started in.
	if l.day != "" {
		rec.Day = l.day
	} else {
		rec.Day = now.Format("2006-01-02")
	}
	rec.Version = buildinfo.Version
	if rec.Turn == 0 {
		rec.Turn = l.turn
	}
	rec.Resumed = l.resumed
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rec); err != nil {
		l.warnOnce("session log encode failed: %v", err)
		return
	}
	path := filepath.Join(l.dir, "agent-"+rec.Day+".jsonl")
	f, err := persist.OpenPrivateFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY)
	if err != nil {
		l.warnOnce("session log open failed: %v", err)
		return
	}
	_, werr := f.Write(buf.Bytes())
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil {
		l.warnOnce("session log write failed: %v", werr)
		return
	}
	if serr != nil {
		l.warnOnce("session log sync failed: %v", serr)
		return
	}
	if cerr != nil {
		l.warnOnce("session log close failed: %v", cerr)
	}
}

func (l *DailySessionLog) warnOnce(format string, args ...any) {
	if l.warned {
		return
	}
	l.warned = true
	if l.warn != nil {
		l.warn(fmt.Sprintf(format, args...))
	}
}

func (l *DailySessionLog) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("session-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func turnTranscript(a *Agent, prompt string) ([]sessionMessage, string) {
	if a == nil || a.conv == nil {
		return nil, ""
	}
	all := a.conv.Messages()
	start := -1
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Role == chat.RoleUser && all[i].Content == prompt {
			start = i
			break
		}
	}
	// Compaction can delete the original prompt and leave its summary plus the
	// protected tail. A model failure rolls the prompt back with no summary,
	// and that miss must not reuse an older turn.
	if start < 0 {
		for i := len(all) - 1; i >= 0; i-- {
			if all[i].Role == chat.RoleUser && strings.HasPrefix(all[i].Content, compactedSummaryPrefix) {
				start = i
				break
			}
		}
	}
	if start < 0 {
		return nil, ""
	}
	var out []sessionMessage
	answer := ""
	for _, m := range all[start:] {
		if m.Role == chat.RoleSystem {
			continue
		}
		content := redactForSurface(m.Content)
		sm := sessionMessage{Role: m.Role, ToolCallID: m.ToolCallID}
		if m.Role == chat.RoleTool {
			clipped, truncated := clipSessionText(content, sessionLogToolCap)
			content = clipped
			sm.Truncated = truncated || strings.HasSuffix(content, chatCompactedToolSuffix)
		}
		sm.Content = content
		for _, tc := range m.ToolCalls {
			sm.ToolCalls = append(sm.ToolCalls, sessionToolCall{
				ID:   tc.ID,
				Name: tc.Function.Name,
				Args: redactForSurface(string(tc.Function.Arguments)),
			})
		}
		if m.Role == chat.RoleAssistant && len(m.ToolCalls) == 0 && content != "" {
			answer = content
		}
		out = append(out, sm)
	}
	return out, answer
}

func clipSessionText(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut == 0 {
		cut = max
	}
	return s[:cut], true
}

type sessionRecord struct {
	Kind      string           `json:"kind"`
	SessionID string           `json:"session_id"`
	Turn      int              `json:"turn,omitempty"`
	TS        string           `json:"ts"`
	Day       string           `json:"day"`
	Version   string           `json:"version,omitempty"`
	Model     string           `json:"model,omitempty"`
	Autonomy  string           `json:"autonomy,omitempty"`
	ElapsedMS int64            `json:"elapsed_ms,omitempty"`
	Stop      string           `json:"stop,omitempty"`
	Error     string           `json:"error,omitempty"`
	TokensIn  int              `json:"tokens_in,omitempty"`
	TokensOut int              `json:"tokens_out,omitempty"`
	Prompt    string           `json:"prompt,omitempty"`
	Answer    string           `json:"answer,omitempty"`
	Resumed   bool             `json:"resumed"`
	Messages  []sessionMessage `json:"messages,omitempty"`
	Tools     []sessionTool    `json:"tools,omitempty"`
	Tool      *sessionTool     `json:"tool,omitempty"`
}

type sessionMessage struct {
	Role       string            `json:"role"`
	Content    string            `json:"content,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	Truncated  bool              `json:"truncated,omitempty"`
	ToolCalls  []sessionToolCall `json:"tool_calls,omitempty"`
}

type sessionToolCall struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	Args string `json:"args,omitempty"`
}

type sessionTool struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Event       string `json:"event"`
	OK          bool   `json:"ok"`
	LatencyMS   int64  `json:"latency_ms,omitempty"`
	ResultBytes int    `json:"result_bytes,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Error       string `json:"error,omitempty"`
	Args        string `json:"args,omitempty"`
}
