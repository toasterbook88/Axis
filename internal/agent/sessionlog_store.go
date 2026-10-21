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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/toasterbook88/axis/internal/lockutil"
	"github.com/toasterbook88/axis/internal/persist"
)

// Limits apply to this logger's agent-*.jsonl files only. The lock is shared
// by every process writing to the directory; no file remains open between writes.
type sessionLogPolicy struct {
	totalBytes, segmentBytes int64
	days                     int
}

func defaultSessionLogPolicy() sessionLogPolicy { return sessionLogPolicy{1 << 30, 8 << 20, 30} }

const sessionLogRecordCap = 1 << 20
const sessionLogTextCap = 256 << 10

// SessionLogProvenance contains observed local context, never credentials or
// remote URLs. Empty fields mean unknown. The cwd is private runtime data.
type SessionLogProvenance struct {
	CapturedAt    string `json:"captured_at,omitempty"`
	CWD           string `json:"cwd,omitempty"`
	ProjectCommit string `json:"project_commit,omitempty"`
	ProjectDirty  *bool  `json:"project_dirty,omitempty"`
	Hostname      string `json:"hostname,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
	Node          string `json:"node,omitempty"`
	TargetModel   string `json:"target_model,omitempty"`
}

// CaptureSessionLogProvenance runs bounded read-only git probes. Failure leaves
// fields unknown; it does not prevent an agent session.
func CaptureSessionLogProvenance(target ModelTarget) SessionLogProvenance {
	p := SessionLogProvenance{Provider: target.ProviderName, Protocol: target.Protocol, Node: target.Node, TargetModel: target.Model}
	p.CapturedAt = time.Now().Format(time.RFC3339)
	p.CWD, _ = os.Getwd()
	p.Hostname, _ = os.Hostname()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	git := func(args ...string) (string, error) {
		// Ignore core.fsmonitor so starting the agent never runs a
		// repository-configured command.
		cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.fsmonitor=false"}, args...)...)
		cmd.WaitDelay = 50 * time.Millisecond
		// Discard large status output: a dirty boolean needs only the first byte.
		var out boundedSessionBuffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			return "", err
		}
		return strings.TrimSpace(out.String()), nil
	}
	if v, err := git("rev-parse", "HEAD"); err == nil {
		p.ProjectCommit = v
	}
	if v, err := git("status", "--porcelain"); err == nil {
		dirty := v != ""
		p.ProjectDirty = &dirty
	}
	return p
}

type boundedSessionBuffer struct{ bytes.Buffer }

func (b *boundedSessionBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if left := 4096 - b.Len(); left > 0 {
		if len(p) > left {
			p = p[:left]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}

// SetProvenance updates context supplied by the CLI's resolved model target.
func (l *DailySessionLog) SetProvenance(p SessionLogProvenance) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.provenance = p
}

func ensureSessionLogDir(dir string) (string, error) {
	// Canonicalize the parent: macOS /var and /tmp are legitimate symlinks.
	// The final log directory itself must not be a symlink. os.Root confines
	// subsequent access to this resolved directory rather than its aliases.
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(abs), persist.PrivateDirMode); err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	resolved := filepath.Join(parent, filepath.Base(abs))
	info, err := os.Lstat(resolved)
	if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return "", fmt.Errorf("session log directory must be a real directory")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err = persist.EnsurePrivateDir(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

var sessionSegmentName = regexp.MustCompile(`^agent-(\d{4}-\d{2}-\d{2})(?:-(\d{6}))?\.jsonl$`)

type sessionSegment struct {
	name string
	size int64
	day  time.Time
	seq  int
}

func privateSessionFile(root *os.Root, name string, flags int) (*os.File, error) {
	return privateSessionFileWithOpen(root, name, flags, root.OpenFile)
}
func privateSessionFileWithOpen(root *os.Root, name string, flags int, open func(string, int, os.FileMode) (*os.File, error)) (*os.File, error) {
	if st, err := root.Lstat(name); err == nil && (!st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("refusing nonregular session log file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := open(name, flags, 0600)
	// Darwin can report ENOENT when concurrent O_CREATE calls first establish
	// the lock. Retry only that recognized race: a regular lock must now exist,
	// and the single retry opens it WITHOUT creation. Never retry absent paths,
	// nonregular files, other errors, or segment writes.
	if errors.Is(err, os.ErrNotExist) && name == ".agent-session.lock" && flags&os.O_CREATE != 0 {
		if info, statErr := root.Lstat(name); statErr == nil && info.Mode().IsRegular() {
			f, err = open(name, flags&^os.O_CREATE, 0600)
		}
	}
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("session log is not a regular file")
	}
	// A hard link is a regular file, so the checks above pass it. Appending
	// through one would write outside the log directory.
	if sessionLogLinkCount(st) > 1 {
		_ = f.Close()
		return nil, fmt.Errorf("refusing hard-linked session log file")
	}
	if err = f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func (l *DailySessionLog) storeRecord(payload []byte, now time.Time) error {
	if int64(len(payload)) > l.policy.segmentBytes || int64(len(payload)) > l.policy.totalBytes {
		return fmt.Errorf("record exceeds storage budget")
	}
	// The directory may have been replaced after construction. Pin its identity
	// so reopening a same-user replacement/symlink cannot redirect any mutation.
	if info, err := os.Lstat(l.dir); err != nil || !info.IsDir() {
		return fmt.Errorf("session log directory replaced")
	}
	root, err := os.OpenRoot(l.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil {
		return err
	}
	if l.dirInfo == nil || !os.SameFile(l.dirInfo, info) {
		return fmt.Errorf("session log directory identity changed")
	}
	lockFile, err := privateSessionFile(root, ".agent-session.lock", os.O_CREATE|os.O_RDWR)
	if err != nil {
		return err
	}
	defer lockFile.Close()
	lock := lockutil.File{File: lockFile}
	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		ok, e := lock.TryLockEx()
		if e != nil {
			return e
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("session log lock busy after 250ms")
		}
		time.Sleep(time.Millisecond)
	}
	defer lock.Unlock()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return err
	}
	day := now.Format("2006-01-02")
	cutoff, err := time.Parse("2006-01-02", now.AddDate(0, 0, -l.policy.days+1).Format("2006-01-02"))
	if err != nil {
		return err
	}
	var segments []sessionSegment
	var total int64
	current := "agent-" + day + ".jsonl"
	var currentSize int64
	currentSeq := -1
	for _, entry := range entries {
		match := sessionSegmentName.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		info, e := root.Lstat(entry.Name())
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing nonregular session segment")
		}
		date, e := time.Parse("2006-01-02", match[1])
		if e != nil {
			continue
		}
		if date.Before(cutoff) {
			if e = root.Remove(entry.Name()); e != nil {
				return e
			}
			continue
		}
		seq, _ := strconv.Atoi(match[2])
		segments = append(segments, sessionSegment{entry.Name(), info.Size(), date, seq})
		total += info.Size()
		if match[1] == day && seq > currentSeq {
			current = entry.Name()
			currentSize = info.Size()
			currentSeq = seq
		}
	}
	sort.Slice(segments, func(i, j int) bool {
		if segments[i].day.Equal(segments[j].day) {
			return segments[i].seq < segments[j].seq
		}
		return segments[i].day.Before(segments[j].day)
	})
	if currentSize+int64(len(payload)) > l.policy.segmentBytes {
		// Never reuse a lower suffix while a later segment survives retention.
		next := currentSeq + 1
		if next < 1 {
			next = 1
		}
		if next > 999999 {
			return fmt.Errorf("session segment limit reached")
		}
		current = fmt.Sprintf("agent-%s-%06d.jsonl", day, next)

	}
	for _, seg := range segments {
		if total+int64(len(payload)) <= l.policy.totalBytes {
			break
		}
		if seg.name == current {
			continue
		}
		if err = root.Remove(seg.name); err != nil {
			return err
		}
		total -= seg.size
	}
	if total+int64(len(payload)) > l.policy.totalBytes {
		return fmt.Errorf("session log quota reached")
	}
	file, err := privateSessionFile(root, current, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return err
	}
	n, err := file.Write(payload)
	if err == nil && n != len(payload) {
		err = io.ErrShortWrite
	}
	// Flush only turn boundaries, not every tool event. A crash may lose recent
	// tool records; the finished record and explicit warning are best-effort.
	if err == nil && bytes.Contains(payload, []byte(`"kind":"turn.finished"`)) {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// Pattern masking supplements the existing egress redactor for quoted JSON,
// dotted/underscored assignments, YAML secrets and complete auth headers.
var sessionSecretAssignment = regexp.MustCompile(`(?i)((?:[a-z0-9_.-]*(?:secret|password|passwd|token|credential|api[_-]?key|access[_-]?key|private[_-]?key)[a-z0-9_.-]*)["']?\s*[:=]\s*)("(?:\\.|[^"\\])*"|'[^']*'|[^\s,;}]+)`)
var sessionAuthorization = regexp.MustCompile(`(?im)((?:authorization|proxy-authorization|x-api-key|cookie|set-cookie)["']?\s*:\s*)[^\r\n]+`)

var sessionPrivateKey = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)
var sessionURLCredentials = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]+:[^/@\s]+@`)

// Decode structured arguments before masking: nested file contents may be
// JSON-escaped strings, for which matching raw outer bytes misses secrets.
// Limit work and redact an overcomplex subtree instead of copying it unsafely.
var sessionCredentialKey = regexp.MustCompile(`(?i)(?:^|[_.-])(?:secrets?|password|passwd|passphrase|token|access[_-]?token|refresh[_-]?token|id[_-]?token|auth[_-]?token|credentials?|key|api[_-]?key|access[_-]?key|private[_-]?key|authorization|proxy-authorization|cookie|set-cookie)$`)

const sessionStructuredCap = 1 << 20

func redactSessionText(s string) string {
	budget := 10000
	return redactSessionValue(s, 0, &budget)
}
func redactSessionValue(s string, depth int, budget *int) string {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[' || trimmed[0] == '"') {
		if depth >= 8 || len(s) > sessionStructuredCap {
			return "[REDACTED STRUCTURED PAYLOAD: LIMIT]"
		}
		decoder := json.NewDecoder(strings.NewReader(s))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err == nil {
			// Require exactly one JSON document; preserve non-JSON prose otherwise.
			var trailing any
			if err = decoder.Decode(&trailing); errors.Is(err, io.EOF) {
				changed := false
				value = redactSessionJSON(value, depth, budget, &changed)
				if changed {
					if encoded, err := json.Marshal(value); err == nil {
						return string(encoded)
					}
				}
				return s
			} else if trimmed[0] == '{' || trimmed[0] == '[' {
				return "[REDACTED MALFORMED STRUCTURED PAYLOAD]"
			}
		} else if trimmed[0] == '{' || trimmed[0] == '[' {
			return "[REDACTED MALFORMED STRUCTURED PAYLOAD]"
		}
	}
	return redactSessionPlainText(s)
}
func redactSessionJSON(value any, depth int, budget *int, changed *bool) any {
	*budget = *budget - 1
	if depth >= 8 || *budget < 0 {
		*changed = true
		return "[REDACTED STRUCTURED PAYLOAD: LIMIT]"
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if sessionCredentialKey.MatchString(key) || sessionSecretAssignment.MatchString(key+"=x") {
				typed[key] = "[REDACTED]"
				*changed = true
				continue
			}
			typed[key] = redactSessionJSON(child, depth+1, budget, changed)
		}
	case []any:
		for i, child := range typed {
			typed[i] = redactSessionJSON(child, depth+1, budget, changed)
		}
	case string:
		clean := redactSessionValue(typed, depth+1, budget)
		if clean != typed {
			*changed = true
		}
		return clean
	}
	return value
}

func redactSessionPlainText(s string) string {
	s = sessionPrivateKey.ReplaceAllString(s, "[REDACTED PRIVATE KEY]")
	s = sessionURLCredentials.ReplaceAllString(s, "${1}[REDACTED]@")
	s = sessionSecretAssignment.ReplaceAllString(s, `${1}"[REDACTED]"`)
	s = sessionAuthorization.ReplaceAllString(s, `${1}[REDACTED]`)
	return redactForSurface(s)
}

func encodeSessionRecord(rec sessionRecord) ([]byte, error) {
	rec.TruncatedBytes = map[string]int{}
	clip := func(path string, p *string, max int) {
		*p = redactSessionText(*p)
		original := len(*p)
		*p, _ = clipSessionText(*p, max)
		if original > len(*p) {
			rec.TruncatedBytes[path] = original - len(*p)
		}
	}
	clip("prompt", &rec.Prompt, sessionLogTextCap)
	clip("answer", &rec.Answer, sessionLogTextCap)
	clip("error", &rec.Error, 4096)
	clip("model", &rec.Model, 1024)
	clip("autonomy", &rec.Autonomy, 1024)
	if rec.Provenance != nil {
		p := *rec.Provenance
		rec.Provenance = &p
		clip("captured_at", &p.CapturedAt, 128)
		clip("cwd", &p.CWD, 4096)
		clip("project_commit", &p.ProjectCommit, 128)
		clip("hostname", &p.Hostname, 1024)
		clip("provider", &p.Provider, 1024)
		clip("protocol", &p.Protocol, 1024)
		clip("node", &p.Node, 1024)
		clip("target_model", &p.TargetModel, 1024)
	}
	if rec.Tool != nil {
		tool := *rec.Tool
		rec.Tool = &tool
		clip("tool.id", &tool.ID, 1024)
		clip("tool.name", &tool.Name, 1024)
		clip("tool.args", &tool.Args, sessionLogToolCap)
		clip("tool.output", &tool.Output, sessionLogToolCap)
		clip("tool.summary", &tool.Summary, sessionLogToolCap)
		clip("tool.error", &tool.Error, 4096)
	}
	encode := func() ([]byte, error) {
		var b bytes.Buffer
		e := json.NewEncoder(&b)
		e.SetEscapeHTML(false)
		err := e.Encode(rec)
		return b.Bytes(), err
	}
	payload, err := encode()
	if err != nil {
		return nil, err
	}
	for len(payload) > sessionLogRecordCap {
		field, path := &rec.Answer, "answer"
		if len(rec.Prompt) > len(*field) {
			field, path = &rec.Prompt, "prompt"
		}
		if len(*field) == 0 {
			return nil, fmt.Errorf("encoded session record exceeds 1MiB")
		}
		before := len(*field)
		*field, _ = clipSessionText(*field, before/2)
		rec.TruncatedBytes[path] += before - len(*field)
		payload, err = encode()
		if err != nil {
			return nil, err
		}
	}
	return payload, nil
}

// SetSessionLogTarget refreshes logging attribution after a resolved /model
// selection. It does not probe or change the backend itself.
func (a *Agent) SetSessionLogTarget(target ModelTarget) {
	if a.sessionLog == nil {
		return
	}
	l := a.sessionLog
	l.mu.Lock()
	defer l.mu.Unlock()
	l.provenance.Provider = target.ProviderName
	l.provenance.Protocol = target.Protocol
	l.provenance.Node = target.Node
	l.provenance.TargetModel = target.Model
}

// sessionLogLinkCount reports the file's hard-link count, or 1 when unknown.
func sessionLogLinkCount(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}
