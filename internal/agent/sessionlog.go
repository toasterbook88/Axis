package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/chat"
)

// sessionLogToolCap bounds a tool result copied into the daily log.
// Tool output is capped because a single read can be larger than the
// conversation it came from. Prompt and answer text have their own larger cap.
const sessionLogToolCap = 16 << 10

// DailySessionLog writes private, bounded local JSONL segments. Prompts are
// stored once at turn.started, tool events as they happen, and answers, tool
// outputs and usage at turn.finished. It never publishes to events or Cortex.
// Logging is best effort and never changes a Run result. See docs/runbooks/agent-session-logs.md.
type DailySessionLog struct {
	dirInfo   os.FileInfo
	answer    string
	dir       string
	sessionID string
	resumed   bool
	warn      func(string)
	now       func() time.Time

	mu         sync.Mutex
	turn       int
	turnStart  time.Time
	model      string
	autonomy   string
	day        string
	policy     sessionLogPolicy
	provenance SessionLogProvenance
	dropped    int
	maxTurns   bool
	warned     bool
}

// OpenDailySessionLog creates dir at mode 0700 and returns a logger that
// appends agent-YYYY-MM-DD.jsonl inside it. dir is the logs directory, not
// the JSONL path.
func OpenDailySessionLog(dir string) (*DailySessionLog, error) {
	resolved, err := ensureSessionLogDir(dir)
	if err != nil {
		return nil, fmt.Errorf("session log dir: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("session log directory identity: %w", err)
	}
	return &DailySessionLog{
		dir:       resolved,
		dirInfo:   info,
		sessionID: newSessionID(),
		policy:    defaultSessionLogPolicy(),
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
	l.turnStart = l.clock()
	l.day = l.turnStart.Format("2006-01-02")
	l.model = a.Model()
	l.autonomy = string(a.Autonomy())
	l.maxTurns = false
	l.answer = ""
	l.writeLocked(sessionRecord{
		Kind:     "turn.started",
		Turn:     l.turn,
		Model:    l.model,
		Autonomy: l.autonomy,
		Prompt:   prompt,
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
	resumed := l.resumed
	answer := l.answer
	l.mu.Unlock()

	stop := "answer"
	errText := ""
	if runErr != nil {
		stop = "error"
		if errors.Is(runErr, context.Canceled) {
			stop = "canceled"
		}
		if errors.Is(runErr, context.DeadlineExceeded) {
			stop = "timeout"
		}
		errText = runErr.Error()
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
		Answer:    answer,
		Resumed:   resumed,
	})
}

func (l *DailySessionLog) noteCalled(id, name, args string) {
	l.noteLive(sessionTool{ID: id, Name: name, Event: "called", Args: args})
}

func (l *DailySessionLog) noteApproval(name string, result ConfirmResult) {
	decision := "denied"
	switch result {
	case ConfirmYes:
		decision = "approved"
	case ConfirmAlways:
		decision = "approved_for_session"
	case ConfirmNever:
		decision = "blocked"
	}
	l.noteLive(sessionTool{Name: name, Event: "approval", OK: result == ConfirmYes || result == ConfirmAlways, Decision: decision})
}

func (l *DailySessionLog) noteSkipped(id, name, reason string) {
	l.noteLive(sessionTool{ID: id, Name: name, Event: "skipped", Error: reason})
}

func (l *DailySessionLog) noteSucceeded(id, name, summary string, resultLen int, elapsed time.Duration) {
	l.noteLive(sessionTool{
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
		msg = err.Error()
	}
	l.noteLive(sessionTool{
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
	l.noteLive(sessionTool{ID: id, Name: "shell", Event: "shell", OK: true, Args: command, Summary: summary})
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
	// A turn that crosses midnight keeps its start day in the record; the
	// segment file is still chosen from the write time.
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
	rec.Commit = buildinfo.ResolvedCommit()
	rec.BuildDate = buildinfo.ResolvedDate()
	rec.Platform = runtime.GOOS + "/" + runtime.GOARCH
	rec.Provenance = &l.provenance
	rec.Dropped = l.dropped
	payload, err := encodeSessionRecord(rec)
	if err == nil {
		err = l.storeRecord(payload, now)
	}
	if err != nil {
		l.dropped++
		l.warnOnce("session log record omitted (logging is best effort): %v", err)
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

func (l *DailySessionLog) noteResponse(m chat.Message) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(m.ToolCalls) == 0 {
		l.answer = m.Content
		return
	}
	if m.Content != "" {
		rec := l.baseLocked()
		rec.Kind = "assistant"
		rec.Answer = m.Content
		l.writeLocked(rec)
	}
}

func (l *DailySessionLog) noteToolOutput(id, name, output string, ok bool) {
	l.noteLive(sessionTool{ID: id, Name: name, Event: "output", Output: output, ResultBytes: len(output), OK: ok})
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
	Commit         string                `json:"build_commit,omitempty"`
	BuildDate      string                `json:"build_date,omitempty"`
	Platform       string                `json:"platform"`
	Provenance     *SessionLogProvenance `json:"provenance,omitempty"`
	Dropped        int                   `json:"previous_records_omitted,omitempty"`
	TruncatedBytes map[string]int        `json:"truncated_bytes,omitempty"`

	Kind      string       `json:"kind"`
	SessionID string       `json:"session_id"`
	Turn      int          `json:"turn,omitempty"`
	TS        string       `json:"ts"`
	Day       string       `json:"day"`
	Version   string       `json:"version,omitempty"`
	Model     string       `json:"model,omitempty"`
	Autonomy  string       `json:"autonomy,omitempty"`
	ElapsedMS int64        `json:"elapsed_ms,omitempty"`
	Stop      string       `json:"stop,omitempty"`
	Error     string       `json:"error,omitempty"`
	TokensIn  int          `json:"tokens_in,omitempty"`
	TokensOut int          `json:"tokens_out,omitempty"`
	Prompt    string       `json:"prompt,omitempty"`
	Answer    string       `json:"answer,omitempty"`
	Resumed   bool         `json:"resumed"`
	Tool      *sessionTool `json:"tool,omitempty"`
}

type sessionTool struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Event       string `json:"event"`
	OK          bool   `json:"ok"`
	LatencyMS   int64  `json:"latency_ms,omitempty"`
	ResultBytes int    `json:"result_bytes,omitempty"`
	Output      string `json:"output,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Error       string `json:"error,omitempty"`
	Args        string `json:"args,omitempty"`
	// Decision is set on "approval" events: approved, approved_for_session,
	// denied or blocked. It records the confirm result, which may come from an
	// autonomy policy rather than the operator.
	Decision string `json:"decision,omitempty"`
}
