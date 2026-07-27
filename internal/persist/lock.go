package persist

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// LockFile acquires an exclusive advisory lock for the store at path, using a
// sibling "<path>.lock" file. The returned release function is idempotent.
//
// LOCK ORDERING. A caller taking more than one store lock MUST acquire them
// in this order, and release in reverse:
//
//  1. state.Path()
//  2. skills.Path()
//
// DEADLOCK. flock is associated with the open file description, so a second
// Open+Flock of the same lock file blocks even within one process. Never call
// state.Update or skills.Update while holding a lock — including indirectly
// via state.Load, which persists pending migrations through Update.
func LockFile(path string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening lock for %s: %w", filepath.Base(path), err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking %s: %w", filepath.Base(path), err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
		})
	}, nil
}
