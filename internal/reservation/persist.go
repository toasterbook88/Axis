package reservation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/toasterbook88/axis/internal/persist"
)

// Path returns the path to the ledger persistence file.
func Path() string {
	return persist.AxisPath("ledger.json")
}

// diskFormat represents the serialized ledger.
type diskFormat struct {
	Entries []*Entry `json:"entries"`
}

// LockFile acquires an exclusive lock on the ledger lockfile with a 500ms timeout.
// It respects context cancellation.
func (l *Ledger) LockFile(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lockFileLocked(ctx)
}

func (l *Ledger) lockFileLocked(ctx context.Context) error {
	if l.lockFile != nil {
		return nil
	}
	path := Path() + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating config directory for lock: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("opening lock file: %w", err)
	}

	start := time.Now()
	timeout := 500 * time.Millisecond
	for {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				f.Close()
				return err
			}
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			f.Close()
			return fmt.Errorf("acquiring file lock: %w", err)
		}
		if time.Since(start) > timeout {
			f.Close()
			return fmt.Errorf("failed to acquire ledger lock within 500ms — the ledger is currently busy")
		}
		time.Sleep(10 * time.Millisecond)
	}

	l.lockFile = f
	return nil
}

// UnlockFile releases the exclusive lock on the ledger lockfile.
func (l *Ledger) UnlockFile() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.unlockFileLocked()
}

func (l *Ledger) unlockFileLocked() {
	if l.lockFile != nil {
		_ = syscall.Flock(int(l.lockFile.Fd()), syscall.LOCK_UN)
		_ = l.lockFile.Close()
		l.lockFile = nil
	}
}

// Load reads the ledger from disk, replacing current entries.
func (l *Ledger) Load() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	wasLocked := l.lockFile != nil
	if !wasLocked {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := l.lockFileLocked(ctx); err != nil {
			return err
		}
		defer l.unlockFileLocked()
	}

	path := Path()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var df diskFormat
	if err := json.Unmarshal(data, &df); err != nil {
		warnErr := persist.QuarantineCorruptFile(path, err)
		return warnErr
	}

	l.entries = make(map[string]*Entry)
	l.totalReserved = 0
	for _, e := range df.Entries {
		l.entries[e.ID] = e
		l.totalReserved += e.RAMMB
	}

	// Startup reconciliation pass
	reclaimed := l.reclaimLocked()
	if reclaimed > 0 {
		l.logger.Info("startup reconciliation complete", "reclaimed", reclaimed)
	}

	return nil
}

// Save writes the ledger to disk.
func (l *Ledger) Save() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	wasLocked := l.lockFile != nil
	if !wasLocked {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := l.lockFileLocked(ctx); err != nil {
			return err
		}
		defer l.unlockFileLocked()
	}

	return l.saveLocked()
}

func (l *Ledger) saveLocked() error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	out := make([]*Entry, 0, len(l.entries))
	for _, e := range l.entries {
		out = append(out, e)
	}

	df := diskFormat{Entries: out}
	data, err := json.MarshalIndent(df, "", "  ")
	if err != nil {
		return err
	}
	return persist.WriteFileAtomic(path, data, 0o644)
}
