package auth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Each case leaves the token path pointing at something that is not a usable
// regular file. readTokenFile must return quickly with the mapped error, and
// a FileToken must keep its last good token and log the reason once.
func TestReadTokenFileRejectsNonRegularAndOversized(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, path string)
		wantErr error
		reason  string
	}{
		{"directory", func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}, ErrTokenFileNotRegular, "not a regular file"},
		{"fifo", func(t *testing.T, path string) {
			if runtime.GOOS != "linux" {
				t.Skip("FIFO case runs on linux only")
			}
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Skipf("mkfifo: %v", err)
			}
		}, ErrTokenFileNotRegular, "not a regular file"},
		{"symlink-to-dev-zero", func(t *testing.T, path string) {
			if _, err := os.Stat("/dev/zero"); err != nil {
				t.Skip("/dev/zero not available")
			}
			if err := os.Symlink("/dev/zero", path); err != nil {
				t.Skipf("symlink: %v", err)
			}
		}, ErrTokenFileNotRegular, "not a regular file"},
		{"oversized", func(t *testing.T, path string) {
			// A valid token followed by padding past the cap.
			big := tokA + strings.Repeat(" ", maxTokenFileSize)
			if err := os.WriteFile(path, []byte(big), 0o600); err != nil {
				t.Fatal(err)
			}
		}, ErrTokenFileTooLarge, "too large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			tc.setup(t, path)

			type result struct {
				data []byte
				err  error
			}
			done := make(chan result, 1)
			go func() {
				data, _, err := readTokenFile(path)
				done <- result{data, err}
			}()
			select {
			case res := <-done:
				if !errors.Is(res.err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", res.err, tc.wantErr)
				}
				if res.data != nil {
					t.Fatalf("returned %d bytes for a rejected file", len(res.data))
				}
			case <-time.After(2 * time.Second):
				t.Fatal("readTokenFile blocked")
			}

			ft, logs := newTestFileToken(t, path, tokA)
			for i := 0; i < 5; i++ {
				ft.poll()
			}
			if got := ft.Current(); got != tokA {
				t.Fatalf("Current() = %q, want last good A", got)
			}
			s := logs.String()
			if n := strings.Count(s, "api token file unusable"); n != 1 {
				t.Fatalf("warn count = %d, want 1; logs:\n%s", n, s)
			}
			if !strings.Contains(s, tc.reason) {
				t.Fatalf("expected reason %q in logs:\n%s", tc.reason, s)
			}
		})
	}
}

// Symlinks are followed: a link to a regular file holding a valid token is a
// usable token file.
func TestReadTokenFileFollowsSymlinkToRegularFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "managed-secret")
	if err := os.WriteFile(target, []byte(tokB+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "token")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink: %v", err)
	}
	_, got, err := readTokenFile(path)
	if err != nil || got != tokB {
		t.Fatalf("got %q, %v; want B", got, err)
	}
}

func TestReadTokenFileAcceptsExactlyMaxSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	content := tokA + strings.Repeat("\n", maxTokenFileSize-len(tokA))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, got, err := readTokenFile(path); err != nil || got != tokA {
		t.Fatalf("got %q, %v; want A", got, err)
	}
}

// Refresh picks up a rotation immediately, without waiting for a poll.
func TestFileTokenRefreshPicksUpRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeTokenFile(t, path, tokA)
	ft, logs := newTestFileToken(t, path, tokA)

	writeTokenFile(t, path, tokB)
	if got := ft.Refresh(); got != tokB {
		t.Fatalf("Refresh() = %q, want B", got)
	}
	if got := ft.Current(); got != tokB {
		t.Fatalf("Current() after Refresh = %q, want B", got)
	}
	assertNoTokenInLogs(t, logs.String())
}

// countReads wraps ft's reader and returns a counter of file reads.
func countReads(ft *FileToken) *atomic.Int32 {
	var n atomic.Int32
	ft.read = func(path string) ([]byte, string, error) {
		n.Add(1)
		return readTokenFile(path)
	}
	return &n
}

// A flood of mismatches against an unchanged file costs no re-reads.
func TestFileTokenRefreshDoesNotRereadUnchangedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeTokenFile(t, path, tokA)
	ft, _ := newTestFileToken(t, path, tokA)
	reads := countReads(ft)

	ft.Refresh() // first call has no stamp yet and reads once
	for i := 0; i < 1000; i++ {
		if got := ft.Refresh(); got != tokA {
			t.Fatalf("Refresh() = %q, want A", got)
		}
	}
	if n := reads.Load(); n != 1 {
		t.Fatalf("reads = %d, want 1 for an unchanged file", n)
	}
}

// A wrong bearer just before a rotation must not delay the new token: the
// next Refresh sees the changed stamp and reads, with no time slot to wait for.
func TestFileTokenRefreshAfterWrongBearerStillSeesRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeTokenFile(t, path, tokA)
	ft, _ := newTestFileToken(t, path, tokA)
	reads := countReads(ft)

	ft.Refresh() // a wrong bearer at T-ε
	writeTokenFile(t, path, tokB)
	if got := ft.Refresh(); got != tokB {
		t.Fatalf("Refresh() right after rotation = %q, want B", got)
	}
	if n := reads.Load(); n != 2 {
		t.Fatalf("reads = %d, want 2", n)
	}
}

// Concurrent Refresh calls after one change read the file once.
func TestFileTokenConcurrentRefreshReadsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeTokenFile(t, path, tokA)
	ft, _ := newTestFileToken(t, path, tokA)
	ft.poll() // establish the stamp
	reads := countReads(ft)

	writeTokenFile(t, path, tokB)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if got := ft.Refresh(); got != tokB {
				t.Errorf("Refresh() = %q, want B", got)
			}
		}()
	}
	close(start)
	wg.Wait()
	if n := reads.Load(); n != 1 {
		t.Fatalf("reads = %d, want 1 for 64 concurrent refreshes of one change", n)
	}
}

// Refresh never installs an unusable file's content.
func TestFileTokenRefreshKeepsLastGoodOnBadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeTokenFile(t, path, tokA)
	ft, _ := newTestFileToken(t, path, tokA)
	writeTokenFile(t, path, "not-a-token")
	if got := ft.Refresh(); got != tokA {
		t.Fatalf("Refresh() = %q, want last good A", got)
	}
}

func TestStaticTokenRefresh(t *testing.T) {
	if got := StaticToken(tokA).Refresh(); got != tokA {
		t.Fatalf("Refresh() = %q", got)
	}
}
