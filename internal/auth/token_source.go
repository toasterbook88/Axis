package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// TokenSource yields the API token the daemon should accept right now.
// Callers take one snapshot per request and use it for every check in that
// request (bearer compare and forwarded-origin HMAC).
type TokenSource interface {
	Current() string
}

// StaticToken is a fixed token. It is used when AXIS_API_TOKEN is set (never
// reloaded) and by the string-token wrappers in internal/api.
type StaticToken string

// Current returns the fixed token.
func (s StaticToken) Current() string { return string(s) }

var (
	// ErrTokenFileMissing means the token file does not exist.
	ErrTokenFileMissing = errors.New("token file missing")
	// ErrTokenFileEmpty means the token file is empty or whitespace only.
	ErrTokenFileEmpty = errors.New("token file empty")
	// ErrTokenFileInvalid means the content is not 64 hex chars (32 bytes).
	ErrTokenFileInvalid = errors.New("token file invalid")
)

// readTokenFile reads and validates a token file, returning the raw bytes for
// change detection. Unlike LoadOrGenerateToken it never creates, regenerates,
// locks or writes anything: a daemon polling with LoadOrGenerateToken would
// silently rotate the token under every client when the file is empty or
// corrupt. No lock is needed: SaveToken writes via temp file plus rename.
func readTokenFile(path string) ([]byte, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", ErrTokenFileMissing
		}
		return nil, "", fmt.Errorf("reading token file: %w", err)
	}
	token, err := parseToken(data)
	return data, token, err
}

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

// tokenPollInterval matches the daemon's config/state/skills watchers
// (internal/daemon watchConfigPollInterval). Tests shorten a FileToken's
// interval field directly.
const tokenPollInterval = 500 * time.Millisecond

// FileToken serves the last good token read from a file and swaps to a new
// valid token at the first poll after the file changes (strict: the old token
// is not accepted after the swap). An unusable file (missing, empty, invalid,
// unreadable) keeps the last good token and logs one warning per distinct
// state. It never writes the file and never logs token material.
type FileToken struct {
	path     string
	interval time.Duration
	cur      atomic.Pointer[string]
	logger   *slog.Logger

	mu      sync.Mutex // serializes poll; guards lastKey and bad
	lastKey string     // fingerprint of the last observed file state
	bad     bool       // the last observed state was unusable
}

// NewFileToken builds a FileToken seeded with the token already loaded at
// startup. An empty initial token is refused so the source can never be empty
// (an empty token would turn withAuth into a pass-through).
func NewFileToken(path, initial string) (*FileToken, error) {
	if initial == "" {
		return nil, errors.New("file token source needs a non-empty initial token")
	}
	ft := &FileToken{path: path, interval: tokenPollInterval}
	ft.cur.Store(&initial)
	return ft, nil
}

// Current returns the token in effect now.
func (f *FileToken) Current() string { return *f.cur.Load() }

func (f *FileToken) log() *slog.Logger {
	if f.logger != nil {
		return f.logger
	}
	return slog.Default()
}

// Run polls the file every poll interval (500 ms) until ctx is cancelled.
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

// poll checks the file once. Change detection is a sha256 fingerprint of the
// raw bytes (not mtime, which misses same-second rewrites).
func (f *FileToken) poll() {
	f.mu.Lock()
	defer f.mu.Unlock()

	data, token, err := readTokenFile(f.path)
	var key, reason string
	switch {
	case errors.Is(err, ErrTokenFileMissing):
		key, reason = "missing", "missing"
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
