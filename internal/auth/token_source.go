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
	// file is accepted before the next poll. It never returns a value its
	// backing store did not supply (FileToken only installs values that pass
	// parseToken; StaticToken returns what it was built with). Implementations
	// must keep it cheap when nothing changed, because any caller presenting a
	// wrong bearer triggers it.
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

// readTokenBytes is the one bounded reader for the token file, shared by the
// loader (LoadOrGenerateToken) and the reloader (FileToken), so they can never
// disagree about which files are usable. It never creates, locks or writes.
//
// Symlinks are followed (operators may point ~/.axis/token at a managed
// secret file), but the target must be a regular file. The file is opened
// with O_NONBLOCK so a FIFO cannot block the open, and the type check uses
// the opened descriptor (fstat), so a path swapped between check and read is
// still caught. Errors: ErrTokenFileMissing (ENOENT), ErrTokenFileNotRegular,
// ErrTokenFileTooLarge (over maxTokenFileSize), or a wrapped open/read error.
// Data is nil on every error.
func readTokenBytes(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrTokenFileMissing
		}
		return nil, fmt.Errorf("opening token file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat token file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, ErrTokenFileNotRegular
	}
	data, err := io.ReadAll(io.LimitReader(f, maxTokenFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading token file: %w", err)
	}
	if len(data) > maxTokenFileSize {
		return nil, ErrTokenFileTooLarge
	}
	return data, nil
}

// readTokenFile reads (readTokenBytes) and validates (parseToken) a token
// file, returning the raw bytes for change detection. Unlike
// LoadOrGenerateToken it never regenerates: a daemon polling with
// LoadOrGenerateToken would silently rotate the token under every client when
// the file is empty or corrupt. No lock is needed: SaveToken writes via temp
// file plus rename.
func readTokenFile(path string) ([]byte, string, error) {
	data, err := readTokenBytes(path)
	if err != nil {
		return nil, "", err
	}
	token, err := parseToken(data)
	return data, token, err
}

// fileStamp is the cheap identity of the file the token path resolves to at
// one moment: device, inode, size and mtime (ns) from stat(2), following
// symlinks. Any atomic replace (temp file plus rename, as SaveToken and the
// documented rotation do) yields a new inode, so it always changes the stamp.
type fileStamp struct {
	dev, ino uint64
	size     int64
	mtimeNs  int64
	mode     os.FileMode
	state    string // "ok", "missing" or "stat-error"
}

func statTokenFile(path string) fileStamp {
	info, err := os.Stat(path)
	switch {
	case os.IsNotExist(err):
		return fileStamp{state: "missing"}
	case err != nil:
		return fileStamp{state: "stat-error"}
	}
	st := fileStamp{state: "ok", size: info.Size(), mtimeNs: info.ModTime().UnixNano(), mode: info.Mode()}
	if sys, ok := info.Sys().(*syscall.Stat_t); ok {
		st.dev, st.ino = uint64(sys.Dev), uint64(sys.Ino) // Dev is int32 on darwin
	}
	return st
}

const (
	// tokenPollInterval matches the daemon's config/state/skills watchers
	// (internal/daemon watchConfigPollInterval).
	tokenPollInterval = 500 * time.Millisecond
)

// FileToken serves the last good token read from a file and swaps to a new
// valid token at the first poll or refresh after the file changes (strict:
// the old token is not accepted after the swap). An unusable file (missing,
// empty, invalid, not a regular file, too large, unreadable) keeps the last
// good token and logs one warning per distinct state. It never writes the file
// and never logs token material.
//
// Build it with NewFileToken. The zero value is not usable: Current would
// dereference a nil token. That is deliberate; returning "" instead would turn
// withAuthSource into an unauthenticated pass-through.
type FileToken struct {
	path     string
	interval time.Duration
	read     func(string) ([]byte, string, error) // readTokenFile; tests count calls
	cur      atomic.Pointer[string]
	logger   *slog.Logger

	// readStamp is the stamp taken just before the last read. Refresh
	// compares against it without taking mu.
	readStamp atomic.Pointer[fileStamp]

	mu      sync.Mutex // serializes reads; guards the fields below
	lastKey string     // fingerprint of the last observed file state
	bad     bool       // the last observed state was unusable
}

// NewFileToken builds a FileToken seeded with the token already loaded at
// startup. An empty initial token is refused so the source can never be empty
// (an empty token would turn withAuthSource into a pass-through).
func NewFileToken(path, initial string) (*FileToken, error) {
	if initial == "" {
		return nil, errors.New("file token source needs a non-empty initial token")
	}
	ft := &FileToken{path: path, interval: tokenPollInterval, read: readTokenFile}
	ft.cur.Store(&initial)
	return ft, nil
}

// Current returns the token in effect now.
func (f *FileToken) Current() string { return *f.cur.Load() }

// Refresh re-reads the file only if it changed since the last read, and
// returns the token in effect afterwards.
//
// "Changed" is a stat(2) of the path (fileStamp: device, inode, size, mtime)
// compared with the stamp taken before the last read. The common case, a
// wrong or stale bearer against an unchanged file, costs one stat and never
// takes mu, so it cannot queue behind a slow read or force re-reads. When the
// stamp did change, callers serialize on mu and re-check, so concurrent
// callers cause one read. Like a poll, it only installs a token that passed
// parseToken.
//
// What it misses (the poll still catches it within one interval): an
// in-place rewrite that keeps the size and lands within the filesystem's
// mtime granularity of the previous read on the same inode.
func (f *FileToken) Refresh() string {
	if f.unchangedSinceRead() {
		return f.Current()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unchangedSinceRead() { // another caller read it while we waited
		return f.Current()
	}
	f.pollLocked()
	return f.Current()
}

func (f *FileToken) unchangedSinceRead() bool {
	last := f.readStamp.Load()
	return last != nil && *last == statTokenFile(f.path)
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
	// Stamp before reading: if the file changes between the two, the stored
	// stamp is the older one and the next Refresh reads again (never the
	// reverse, which could hide a change).
	stamp := statTokenFile(f.path)
	data, token, err := f.read(f.path)
	f.readStamp.Store(&stamp)
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
