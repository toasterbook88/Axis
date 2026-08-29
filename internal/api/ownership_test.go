package api

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

// shortSocketDir returns a directory with a short path. macOS caps unix socket
// paths at 104 characters, which t.TempDir() can exceed.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ax")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.Dial("unix", path); err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s never became connectable", path)
}

// TestServeWithContextRefusesSecondDaemon is the regression test for the
// duplicate-daemon failure: the previous implementation unlinked any existing
// socket unconditionally, so a second daemon silently orphaned the first, which
// kept serving a divergent cache on an unlinked inode.
func TestServeWithContextRefusesSecondDaemon(t *testing.T) {
	socketPath := filepath.Join(shortSocketDir(t), "s.sock")
	cache := &fakeCache{snap: &models.ClusterSnapshot{Status: models.SnapshotHealthy}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := make(chan error, 1)
	go func() { first <- ServeWithContext(ctx, socketPath, cache, "", false) }()
	waitForSocket(t, socketPath)

	err := ServeWithContext(context.Background(), socketPath, cache, "", false)
	if !errors.Is(err, ErrDaemonAlreadyRunning) {
		t.Fatalf("second daemon: got %v, want ErrDaemonAlreadyRunning", err)
	}

	// The refusal must not have disturbed the incumbent.
	waitForSocket(t, socketPath)

	cancel()
	select {
	case err := <-first:
		if err != nil {
			t.Fatalf("first daemon returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first daemon did not shut down")
	}
}

// TestAcquireUnixSocketReclaimsStaleSocket covers the other half: a socket file
// left behind by a crashed daemon has no listener, so it is safe to unlink.
func TestAcquireUnixSocketReclaimsStaleSocket(t *testing.T) {
	socketPath := filepath.Join(shortSocketDir(t), "s.sock")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	// Leave the socket file on disk with no process behind it.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("expected stale socket to remain: %v", err)
	}
	if socketIsLive(socketPath) {
		t.Fatal("stale socket reported as live")
	}

	got, release, err := acquireUnixSocket(socketPath)
	if err != nil {
		t.Fatalf("acquireUnixSocket on stale socket: %v", err)
	}
	defer release()
	defer got.Close()
}

func TestAcquireUnixSocketRefusesNonSocketFile(t *testing.T) {
	path := filepath.Join(shortSocketDir(t), "s.sock")
	if err := os.WriteFile(path, []byte("not a socket"), 0600); err != nil {
		t.Fatal(err)
	}

	_, _, err := acquireUnixSocket(path)
	if err == nil {
		t.Fatal("expected refusal for a regular file at the socket path")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("refusal must not delete the file: %v", statErr)
	}
}
