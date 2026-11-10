package auth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// Refresh reads the file at most once per refresh interval.
func TestFileTokenRefreshIsRateLimited(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeTokenFile(t, path, tokA)
	ft, _ := newTestFileToken(t, path, tokA)
	clock := time.Unix(1_700_000_000, 0)
	ft.now = func() time.Time { return clock }

	if got := ft.Refresh(); got != tokA {
		t.Fatalf("first Refresh() = %q, want A", got)
	}
	writeTokenFile(t, path, tokB)
	clock = clock.Add(tokenRefreshMinInterval - time.Millisecond)
	if got := ft.Refresh(); got != tokA {
		t.Fatalf("Refresh() inside the interval = %q, want A (no read)", got)
	}
	clock = clock.Add(time.Millisecond)
	if got := ft.Refresh(); got != tokB {
		t.Fatalf("Refresh() after the interval = %q, want B", got)
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
