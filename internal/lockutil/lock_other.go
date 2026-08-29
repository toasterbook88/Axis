//go:build !darwin && !linux

package lockutil

import (
	"fmt"
	"os"
)

// Flock is a no-op on platforms where advisory locking is not supported by the
// build target. The package still compiles so callers can remain portable.
func Flock(fd int, how int) error {
	return nil
}

// File wraps an *os.File with advisory lock helpers.
type File struct {
	*os.File
}

// LockEx is a no-op on unsupported platforms.
func (f *File) LockEx() error {
	return nil
}

// Unlock is a no-op on unsupported platforms.
func (f *File) Unlock() error {
	return nil
}

// OpenLock opens path for locking, creating it if necessary.
func OpenLock(path string) (*File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	return &File{File: f}, nil
}

// TryLockEx always reports success on platforms without advisory locking.
// Callers must not rely on it alone for mutual exclusion there; pair it with a
// liveness probe of the resource being guarded.
func (f *File) TryLockEx() (bool, error) {
	return true, nil
}
