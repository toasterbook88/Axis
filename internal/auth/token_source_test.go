package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	tokA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tokB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tokC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// syncBuffer is a goroutine-safe log sink for slog handlers in tests.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func writeTokenFile(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func newTestFileToken(t *testing.T, path, initial string) (*FileToken, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	ft, err := NewFileToken(path, initial)
	if err != nil {
		t.Fatalf("NewFileToken: %v", err)
	}
	ft.logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return ft, logs
}

func TestReadTokenFileNeverWrites(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		content *string
		want    string
		wantErr error
	}{
		{name: "valid", content: strPtr(tokA + "\n"), want: tokA},
		{name: "missing", content: nil, wantErr: ErrTokenFileMissing},
		{name: "empty", content: strPtr(""), wantErr: ErrTokenFileEmpty},
		{name: "whitespace", content: strPtr("  \n\t"), wantErr: ErrTokenFileEmpty},
		{name: "short", content: strPtr(tokA[:63]), wantErr: ErrTokenFileInvalid},
		{name: "long", content: strPtr(tokA + "a"), wantErr: ErrTokenFileInvalid},
		{name: "nonhex", content: strPtr(strings.Repeat("z", 64)), wantErr: ErrTokenFileInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name)
			var before os.FileInfo
			if tc.content != nil {
				if err := os.WriteFile(path, []byte(*tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
				old := time.Now().Add(-time.Hour)
				if err := os.Chtimes(path, old, old); err != nil {
					t.Fatal(err)
				}
				before, _ = os.Stat(path)
			}
			_, got, err := readTokenFile(path)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
			// It never writes: missing stays missing, existing is byte-identical.
			after, statErr := os.Stat(path)
			if tc.content == nil {
				if !os.IsNotExist(statErr) {
					t.Fatalf("readTokenFile created the file: %v", statErr)
				}
				return
			}
			data, _ := os.ReadFile(path)
			if string(data) != *tc.content || !after.ModTime().Equal(before.ModTime()) {
				t.Fatalf("readTokenFile modified the file")
			}
		})
	}
}

func strPtr(s string) *string { return &s }

func TestNewFileTokenRefusesEmptyInitial(t *testing.T) {
	if _, err := NewFileToken(filepath.Join(t.TempDir(), "token"), ""); err == nil {
		t.Fatal("expected an error for an empty initial token")
	}
}

func TestStaticToken(t *testing.T) {
	if got := StaticToken(tokA).Current(); got != tokA {
		t.Fatalf("Current() = %q", got)
	}
	if got := StaticToken("").Current(); got != "" {
		t.Fatalf("Current() = %q, want empty", got)
	}
}

func TestFileTokenPollRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeTokenFile(t, path, tokA)
	ft, logs := newTestFileToken(t, path, tokA)

	ft.poll()
	if got := ft.Current(); got != tokA {
		t.Fatalf("Current() = %q, want A", got)
	}
	writeTokenFile(t, path, tokB+"\n")
	ft.poll()
	if got := ft.Current(); got != tokB {
		t.Fatalf("after rotation Current() = %q, want B", got)
	}
	if n := strings.Count(logs.String(), "api token reloaded"); n != 1 {
		t.Fatalf("reload Info count = %d, want 1; logs:\n%s", n, logs)
	}
	assertNoTokenInLogs(t, logs.String())
}

func TestFileTokenSameValueRewriteIsSilent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeTokenFile(t, path, tokA)
	ft, logs := newTestFileToken(t, path, tokA)
	ft.poll()
	writeTokenFile(t, path, tokA+"\n") // different bytes, same token
	ft.poll()
	writeTokenFile(t, path, tokA)
	ft.poll()
	if got := ft.Current(); got != tokA {
		t.Fatalf("Current() = %q, want A", got)
	}
	if s := logs.String(); s != "" {
		t.Fatalf("expected no logs, got:\n%s", s)
	}
}

func TestFileTokenKeepsLastGoodAndNeverWrites(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		mutate       func(t *testing.T, path string)
	}{
		{"missing", "missing", func(t *testing.T, path string) {
			// Move it aside rather than delete it; the reader must see ENOENT.
			if err := os.Rename(path, path+".aside"); err != nil {
				t.Fatal(err)
			}
		}},
		{"empty", "empty", func(t *testing.T, path string) { writeTokenFile(t, path, "") }},
		{"corrupt", "invalid", func(t *testing.T, path string) { writeTokenFile(t, path, "not-a-token") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			writeTokenFile(t, path, tokA)
			ft, logs := newTestFileToken(t, path, tokA)
			ft.poll()
			tc.mutate(t, path)
			before, _ := os.ReadFile(path)
			for i := 0; i < 10; i++ {
				ft.poll()
			}
			if got := ft.Current(); got != tokA {
				t.Fatalf("Current() = %q, want last good A", got)
			}
			after, err := os.ReadFile(path)
			if tc.name == "missing" {
				if !os.IsNotExist(err) {
					t.Fatalf("FileToken recreated the token file (err=%v)", err)
				}
			} else if !bytes.Equal(before, after) {
				t.Fatalf("FileToken rewrote the token file: %q -> %q", before, after)
			}
			s := logs.String()
			if n := strings.Count(s, "api token file unusable"); n != 1 {
				t.Fatalf("warn count = %d, want 1; logs:\n%s", n, s)
			}
			if !strings.Contains(s, "reason="+tc.reason) {
				t.Fatalf("expected reason=%s in logs:\n%s", tc.reason, s)
			}
			assertNoTokenInLogs(t, s)
		})
	}
}

func TestFileTokenLogsOncePerDistinctBadStateAndRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeTokenFile(t, path, tokA)
	ft, logs := newTestFileToken(t, path, tokA)
	ft.poll()

	writeTokenFile(t, path, "garbage-1")
	for i := 0; i < 10; i++ {
		ft.poll()
	}
	writeTokenFile(t, path, "garbage-2")
	for i := 0; i < 10; i++ {
		ft.poll()
	}
	if n := strings.Count(logs.String(), "api token file unusable"); n != 2 {
		t.Fatalf("warn count = %d, want 2; logs:\n%s", n, logs)
	}
	writeTokenFile(t, path, tokB)
	ft.poll()
	ft.poll()
	if got := ft.Current(); got != tokB {
		t.Fatalf("Current() = %q, want B", got)
	}
	s := logs.String()
	if n := strings.Count(s, "level=INFO"); n != 1 {
		t.Fatalf("info count = %d, want 1; logs:\n%s", n, s)
	}
	// A later bad state of the same kind as before logs again.
	writeTokenFile(t, path, "garbage-1")
	ft.poll()
	if n := strings.Count(logs.String(), "api token file unusable"); n != 3 {
		t.Fatalf("warn count = %d, want 3; logs:\n%s", n, logs)
	}
	assertNoTokenInLogs(t, logs.String())
}

func TestFileTokenRegeneratedFileIsPickedUp(t *testing.T) {
	// Deleting the file and letting a client regenerate it becomes a rotation.
	path := filepath.Join(t.TempDir(), "token")
	writeTokenFile(t, path, tokA)
	ft, _ := newTestFileToken(t, path, tokA)
	ft.poll()
	if err := os.Rename(path, path+".aside"); err != nil {
		t.Fatal(err)
	}
	ft.poll()
	writeTokenFile(t, path, tokC)
	ft.poll()
	if got := ft.Current(); got != tokC {
		t.Fatalf("Current() = %q, want C", got)
	}
}

func TestFileTokenRunSwapsWithinTwoPollsAndStopsOnCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeTokenFile(t, path, tokA)
	ft, _ := newTestFileToken(t, path, tokA)
	ft.interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ft.Run(ctx)
		close(done)
	}()

	writeTokenFile(t, path, tokB)
	deadline := time.Now().Add(2 * time.Second)
	for ft.Current() != tokB {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("Run did not swap to the rotated token")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after ctx cancel")
	}
}

func TestFileTokenConcurrentReadsDuringRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	writeTokenFile(t, path, tokA)
	ft, _ := newTestFileToken(t, path, tokA)
	ft.interval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ft.Run(ctx)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if got := ft.Current(); got != tokA && got != tokB {
					t.Errorf("Current() returned a value that was never written: %q", got)
					return
				}
			}
		}()
	}
	for i := 0; i < 100; i++ {
		if i%2 == 0 {
			writeTokenFile(t, path, tokB)
		} else {
			writeTokenFile(t, path, tokA)
		}
		ft.poll() // concurrent with Run's own polls
	}
	close(stop)
	wg.Wait()
}

func assertNoTokenInLogs(t *testing.T, logs string) {
	t.Helper()
	for _, tok := range []string{tokA, tokB, tokC} {
		if strings.Contains(logs, tok[:8]) {
			t.Fatalf("logs contain token material:\n%s", logs)
		}
	}
}
