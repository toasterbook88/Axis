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

	"github.com/toasterbook88/axis/internal/persist"
)

// The startup loader uses the same bounded reader as the reloader: a
// non-regular or oversized token file is an error, returned quickly (no hang
// while holding the token lock), and the path is left exactly as it was.
func TestLoadOrGenerateTokenRefusesNonRegularAndOversized(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, path string)
		wantErr error
	}{
		{"directory", func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}, ErrTokenFileNotRegular},
		{"fifo", func(t *testing.T, path string) {
			if runtime.GOOS != "linux" {
				t.Skip("FIFO case runs on linux only")
			}
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Skipf("mkfifo: %v", err)
			}
		}, ErrTokenFileNotRegular},
		{"symlink-to-dev-zero", func(t *testing.T, path string) {
			if _, err := os.Stat("/dev/zero"); err != nil {
				t.Skip("/dev/zero not available")
			}
			if err := os.Symlink("/dev/zero", path); err != nil {
				t.Skipf("symlink: %v", err)
			}
		}, ErrTokenFileNotRegular},
		{"oversized-valid-token-plus-whitespace", func(t *testing.T, path string) {
			// Trimmed, this is a valid token; the reloader rejects it as too
			// large, so the loader must too.
			if err := os.WriteFile(path, []byte(tokA+strings.Repeat(" ", maxTokenFileSize)), 0o600); err != nil {
				t.Fatal(err)
			}
		}, ErrTokenFileTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv(persist.AxisHomeEnv, home)
			t.Setenv(TokenEnvVar, "")
			path := filepath.Join(home, "token")
			tc.setup(t, path)
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}

			done := make(chan error, 1)
			go func() {
				_, err := LoadOrGenerateToken()
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("LoadOrGenerateToken blocked")
			}

			after, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("token path removed: %v", err)
			}
			if after.Mode() != before.Mode() || !os.SameFile(before, after) {
				t.Fatal("LoadOrGenerateToken replaced the token path")
			}
		})
	}
}

// The loader's generation paths are unchanged: missing generates, a regular
// file with corrupt content regenerates, and a valid file (also via a symlink
// to a regular file) is returned as is.
func TestLoadOrGenerateTokenGenerationPathsUnchanged(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv(persist.AxisHomeEnv, home)
		t.Setenv(TokenEnvVar, "")
		tok, err := LoadOrGenerateToken()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseToken([]byte(tok)); err != nil {
			t.Fatalf("generated token invalid: %v", err)
		}
	})
	t.Run("corrupt-regular-file", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv(persist.AxisHomeEnv, home)
		t.Setenv(TokenEnvVar, "")
		if err := os.WriteFile(filepath.Join(home, "token"), []byte("not-a-token"), 0o600); err != nil {
			t.Fatal(err)
		}
		tok, err := LoadOrGenerateToken()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseToken([]byte(tok)); err != nil {
			t.Fatalf("regenerated token invalid: %v", err)
		}
	})
	t.Run("symlink-to-valid-regular-file", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv(persist.AxisHomeEnv, home)
		t.Setenv(TokenEnvVar, "")
		target := filepath.Join(t.TempDir(), "managed-secret")
		if err := os.WriteFile(target, []byte(tokB+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(home, "token")); err != nil {
			t.Skipf("symlink: %v", err)
		}
		tok, err := LoadOrGenerateToken()
		if err != nil || tok != tokB {
			t.Fatalf("got %q, %v; want B", tok, err)
		}
	})
}
