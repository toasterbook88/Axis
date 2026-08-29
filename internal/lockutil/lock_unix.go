//go:build darwin || linux

package lockutil

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Flock applies or removes an advisory lock on the open file described by fd.
// It is a thin wrapper around unix.Flock so callers do not need to import
// platform-specific syscall packages directly.
func Flock(fd int, how int) error {
	return unix.Flock(fd, how)
}

// File wraps an *os.File with advisory lock helpers.
type File struct {
	*os.File
}

// LockEx acquires an exclusive advisory lock on the file.
func (f *File) LockEx() error {
	return Flock(int(f.Fd()), unix.LOCK_EX)
}

// Unlock releases the advisory lock on the file.
func (f *File) Unlock() error {
	return Flock(int(f.Fd()), unix.LOCK_UN)
}

// OpenLock opens path for locking, creating it if necessary.
func OpenLock(path string) (*File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	return &File{File: f}, nil
}

// TryLockEx attempts to acquire an exclusive advisory lock without blocking.
// It reports whether the lock was acquired. A false return with a nil error
// means another process holds the lock.
func (f *File) TryLockEx() (bool, error) {
	err := Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return false, err
}
