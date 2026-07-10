//go:build darwin || linux

package lockutil

import (
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenLockAndAdvisoryLocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "axis.lock")

	first, err := OpenLock(path)
	if err != nil {
		t.Fatalf("OpenLock() error = %v", err)
	}
	defer first.Close()

	info, err := first.Stat()
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("lock file mode = %o, want 600", got)
	}

	second, err := OpenLock(path)
	if err != nil {
		t.Fatalf("second OpenLock() error = %v", err)
	}
	defer second.Close()

	if err := first.LockEx(); err != nil {
		t.Fatalf("LockEx() error = %v", err)
	}
	if err := Flock(int(second.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
		t.Fatalf("contended Flock() error = %v, want EWOULDBLOCK", err)
	}
	if err := first.Unlock(); err != nil {
		t.Fatalf("Unlock() error = %v", err)
	}
	if err := second.LockEx(); err != nil {
		t.Fatalf("LockEx() after unlock error = %v", err)
	}
	if err := second.Unlock(); err != nil {
		t.Fatalf("second Unlock() error = %v", err)
	}
}

func TestOpenLockReportsOpenFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "axis.lock")

	if _, err := OpenLock(path); err == nil {
		t.Fatal("OpenLock() error = nil, want open failure")
	}
}
