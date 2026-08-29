package api

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/toasterbook88/axis/internal/lockutil"
)

// ErrDaemonAlreadyRunning is returned when another process already owns the
// daemon address. Callers should exit rather than take the address over: a
// second daemon on the same socket path silently orphans the first, which then
// keeps serving on an unlinked inode with its own divergent snapshot cache.
var ErrDaemonAlreadyRunning = errors.New("another axis daemon already owns this address")

// socketProbeTimeout bounds the liveness probe of an existing socket. The
// probe only needs to complete a local connect, so this is generous.
const socketProbeTimeout = 250 * time.Millisecond

// lockPathFor returns the advisory lock path guarding a daemon address.
func lockPathFor(addr string) string {
	return addr + ".lock"
}

// socketIsLive reports whether some process is accepting connections on the
// unix socket at addr. A socket file whose listener is gone refuses connects
// with ECONNREFUSED, which is how a stale socket is distinguished from an
// owned one. Never unlink a socket for which this returns true.
func socketIsLive(addr string) bool {
	conn, err := net.DialTimeout("unix", addr, socketProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// acquireUnixSocket takes exclusive ownership of a unix socket address and
// returns a listener plus a release function for the ownership lock.
//
// Ownership is established in two layers because neither alone is sufficient:
// the advisory lock closes the probe/unlink/listen race between two daemons
// starting at once, and the liveness probe catches a daemon from an older
// binary that predates the lock (or a platform where advisory locking is a
// no-op).
func acquireUnixSocket(addr string) (net.Listener, func(), error) {
	if err := os.MkdirAll(filepath.Dir(addr), 0700); err != nil {
		return nil, nil, fmt.Errorf("creating unix socket directory: %w", err)
	}

	lock, err := lockutil.OpenLock(lockPathFor(addr))
	if err != nil {
		return nil, nil, err
	}
	release := func() {
		_ = lock.Unlock()
		_ = lock.Close()
	}

	held, err := lock.TryLockEx()
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("locking daemon address: %w", err)
	}
	if !held {
		release()
		return nil, nil, fmt.Errorf("%w: %s", ErrDaemonAlreadyRunning, addr)
	}

	if fi, err := os.Lstat(addr); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			release()
			return nil, nil, fmt.Errorf("refusing to remove non-socket file at %s", addr)
		}
		if socketIsLive(addr) {
			release()
			return nil, nil, fmt.Errorf("%w: %s", ErrDaemonAlreadyRunning, addr)
		}
		if err := os.Remove(addr); err != nil {
			release()
			return nil, nil, fmt.Errorf("removing stale socket: %w", err)
		}
	} else if !os.IsNotExist(err) {
		release()
		return nil, nil, fmt.Errorf("stat socket path: %w", err)
	}

	ln, err := net.Listen("unix", addr)
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("listen unix %s: %w", addr, err)
	}
	if err := os.Chmod(addr, 0600); err != nil {
		_ = ln.Close()
		release()
		return nil, nil, fmt.Errorf("chmod unix socket: %w", err)
	}
	return ln, release, nil
}
