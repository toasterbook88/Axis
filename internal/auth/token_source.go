package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// TokenSource yields the API token the daemon should accept right now.
// Callers take one snapshot per request and use it for every check in that
// request (bearer compare and forwarded-origin HMAC).
type TokenSource interface {
	// Current returns the token in effect now.
	Current() string
	// Refresh re-checks the backing store (if any) and returns the token in
	// effect afterwards. The API calls it once when a presented bearer does
	// not match Current, so a client that already re-read a rotated token
	// file is not refused until the next poll. It must never return a value
	// that failed validation; implementations rate-limit their own reads.
	Refresh() string
}

// StaticToken is a fixed token. It is used when AXIS_API_TOKEN is set (never
// reloaded) and by the string-token wrappers in internal/api.
type StaticToken string

// Current returns the fixed token.
func (s StaticToken) Current() string { return string(s) }

// Refresh returns the fixed token; there is nothing to re-read.
func (s StaticToken) Refresh() string { return string(s) }

var (
	// ErrTokenFileMissing means the token file does not exist.
	ErrTokenFileMissing = errors.New("token file missing")
	// ErrTokenFileEmpty means the token file is empty or whitespace only.
	ErrTokenFileEmpty = errors.New("token file empty")
	// ErrTokenFileInvalid means the content is not 64 hex chars (32 bytes).
	ErrTokenFileInvalid = errors.New("token file invalid")
	// ErrTokenFileNotRegular means the path (after following symlinks) is
	// not a regular file: a directory, FIFO, socket or device.
	ErrTokenFileNotRegular = errors.New("token file is not a regular file")
	// ErrTokenFileTooLarge means the file is larger than maxTokenFileSize.
	ErrTokenFileTooLarge = errors.New("token file too large")
)

// maxTokenFileSize bounds every read of the token file. A valid token is 64
// hex characters plus optional whitespace, so 4 KiB is far above any valid
// file and small enough to read every poll.
const maxTokenFileSize = 4096

// parseToken is the single definition of a valid API token: 64 hex
// characters (32 bytes) after trimming surrounding whitespace. Both the
// loader (LoadOrGenerateToken) and the reloader (FileToken) use it, so the
// daemon and its clients can never disagree about which files are usable.
func parseToken(data []byte) (string, error) {
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", ErrTokenFileEmpty
	}
	if decoded, err := hex.DecodeString(token); err != nil || len(decoded) != 32 {
		return "", ErrTokenFileInvalid
	}
	return token, nil
}

// readTokenFile reads and validates a token file, returning the raw bytes for
// change detection. Unlike LoadOrGenerateToken it never creates, regenerates,
// locks or writes anything: a daemon polling with LoadOrGenerateToken would
// silently rotate the token under every client when the file is empty or
// corrupt. No lock is needed: SaveToken writes via temp file plus rename.
//
// The read is bounded so a poll can never block or allocate without limit.
// Symlinks are followed (operators may point ~/.axis/token at a managed
// secret file), but the target must be a regular file. The file is opened
// with O_NONBLOCK so a FIFO cannot block the open, and the type check uses
// the opened descriptor (fstat), so a path swapped between check and read is
// still caught. A non-regular file returns ErrTokenFileNotRegular and a file
// over maxTokenFileSize returns ErrTokenFileTooLarge, both with nil data.
func readTokenFile(path string) ([]byte, string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", ErrTokenFileMissing
		}
		return nil, "", fmt.Errorf("opening token file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, "", fmt.Errorf("stat token file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, "", ErrTokenFileNotRegular
	}
	data, err := io.ReadAll(io.LimitReader(f, maxTokenFileSize+1))
	if err != nil {
		return nil, "", fmt.Errorf("reading token file: %w", err)
	}
	if len(data) > maxTokenFileSize {
		return nil, "", ErrTokenFileTooLarge
	}
	token, err := parseToken(data)
	return data, token, err
}

const (
	// tokenPollInterval matches the daemon's config/state/skills watchers
	// (internal/daemon watchConfigPollInterval).
	tokenPollInterval = 500 * time.Millisecond
	// tokenRefreshMinInterval bounds on-demand re-reads triggered by a
	// mismatched bearer, so a client sending bad tokens cannot turn every
	// request into a file read.
	tokenRefreshMinInterval = 250 * time.Millisecond
)

// FileToken serves the last good token read from a file and swaps to a new
// valid token at the first poll (or refresh) after the file changes (strict:
// the old token is not accepted after the swap). An unusable file (missing,
// empty, invalid, not a regular file, too large, unreadable) keeps the last
// good token and logs one warning per distinct state. It never writes the file
// and never logs token material.
//
// Build it with NewFileToken. The zero value is not usable: Current would
// dereference a nil token. That is deliberate; returning "" instead would turn
// withAuthSource into an unauthenticated pass-through.
type FileToken struct {
	path         string
	interval     time.Duration
	refreshEvery time.Duration
	now          func() time.Time
	cur          atomic.Pointer[string]
	logger       *slog.Logger

	mu          sync.Mutex // serializes reads; guards the fields below
	lastKey     string     // fingerprint of the last observed file state
	bad         bool       // the last observed state was unusable
	lastRefresh time.Time  // when Refresh last read the file
}

// NewFileToken builds a FileToken seeded with the token already loaded at
// startup. An empty initial token is refused so the source can never be empty
// (an empty token would turn withAuthSource into a pass-through).
func NewFileToken(path, initial string) (*FileToken, error) {
	if initial == "" {
		return nil, errors.New("file token source needs a non-empty initial token")
	}
	ft := &FileToken{
		path:         path,
		interval:     tokenPollInterval,
		refreshEvery: tokenRefreshMinInterval,
		now:          time.Now,
	}
	ft.cur.Store(&initial)
	return ft, nil
}

// Current returns the token in effect now.
func (f *FileToken) Current() string { return *f.cur.Load() }

// Refresh re-reads the file at most once per refresh interval (250 ms) and
// returns the token in effect afterwards. Calls inside the interval return
// Current without touching the file. Like a poll, it only ever installs a
// token that passed parseToken.
func (f *FileToken) Refresh() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	if f.lastRefresh.IsZero() || now.Sub(f.lastRefresh) >= f.refreshEvery {
		f.lastRefresh = now
		f.pollLocked()
	}
	return f.Current()
}

func (f *FileToken) log() *slog.Logger {
	if f.logger != nil {
		return f.logger
	}
	return slog.Default()
}

// Run polls the file every poll interval until ctx is cancelled.
func (f *FileToken) Run(ctx context.Context) {
	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f.poll()
		}
	}
}

// poll checks the file once.
func (f *FileToken) poll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pollLocked()
}

// pollLocked checks the file once; f.mu must be held. Change detection is a
// sha256 fingerprint of the raw bytes (not mtime, which misses same-second
// rewrites).
func (f *FileToken) pollLocked() {
	data, token, err := readTokenFile(f.path)
	var key, reason string
	switch {
	case errors.Is(err, ErrTokenFileMissing):
		key, reason = "missing", "missing"
	case errors.Is(err, ErrTokenFileNotRegular):
		key, reason = "not-regular", "not a regular file"
	case errors.Is(err, ErrTokenFileTooLarge):
		key, reason = "too-large", "too large"
	case data == nil && err != nil:
		key, reason = "read-error", "read error"
	default:
		sum := sha256.Sum256(data)
		key = "content:" + hex.EncodeToString(sum[:])
		switch {
		case errors.Is(err, ErrTokenFileEmpty):
			reason = "empty"
		case err != nil:
			reason = "invalid"
		}
	}
	if key == f.lastKey {
		return
	}
	f.lastKey = key

	if reason != "" {
		f.bad = true
		f.log().Warn("api token file unusable; keeping last good token", "path", f.path, "reason", reason)
		return
	}

	wasBad := f.bad
	f.bad = false
	if token == f.Current() {
		if wasBad {
			f.log().Info("api token file usable again; token unchanged", "path", f.path)
		}
		return
	}
	f.cur.Store(&token)
	f.log().Info("api token reloaded", "path", f.path)
}
