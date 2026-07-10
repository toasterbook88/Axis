package agent

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// failingWriter returns errAfter once it has written at least limit bytes,
// simulating an underlying writer that fails partway through output.
type failingWriter struct {
	written int
	limit   int
}

var errWriteFailed = errors.New("write failed")

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.written >= f.limit {
		return 0, errWriteFailed
	}
	f.written += len(p)
	return len(p), nil
}

func TestColorWriterBlock(t *testing.T) {
	var buf bytes.Buffer
	cw := NewColorWriter(&buf)

	_, _ = cw.Write([]byte("some text before\n```go\nfmt.Println(123)\n```\ntext after"))
	_ = cw.Close()

	out := buf.String()
	if !strings.Contains(out, "some text before") {
		t.Error("expected text before code block to be preserved")
	}
	if !strings.Contains(out, "text after") {
		t.Error("expected text after code block to be preserved")
	}
	if !strings.Contains(out, "\033[36;1m") {
		t.Error("expected bold cyan ANSI code for entering code block")
	}
	if !strings.Contains(out, "\033[0m") {
		t.Error("expected reset ANSI code on exit")
	}
}

func TestColorWriterInline(t *testing.T) {
	var buf bytes.Buffer
	cw := NewColorWriter(&buf)

	_, _ = cw.Write([]byte("use `go test` to run it"))
	_ = cw.Close()

	out := buf.String()
	if !strings.Contains(out, "\033[33m") {
		t.Error("expected yellow ANSI code for inline backtick")
	}
}

func TestColorWriterChunked(t *testing.T) {
	var buf bytes.Buffer
	cw := NewColorWriter(&buf)

	// Write backticks chunk-by-chunk to test prefix buffering
	_, _ = cw.Write([]byte("he"))
	_, _ = cw.Write([]byte("llo `"))
	_, _ = cw.Write([]byte("`"))
	_, _ = cw.Write([]byte("`code"))
	_, _ = cw.Write([]byte("```done"))
	_ = cw.Close()

	out := buf.String()
	if !strings.Contains(out, "\033[36;1m") {
		t.Error("expected entering code block in chunked mode")
	}
}

func TestColorWriterPropagatesWriteError(t *testing.T) {
	// Fails on the escape-sequence write emitted when entering a code block.
	cw := NewColorWriter(&failingWriter{limit: 0})

	_, err := cw.Write([]byte("```go\n"))
	if !errors.Is(err, errWriteFailed) {
		t.Fatalf("expected write error to propagate, got %v", err)
	}
}

func TestColorWriterClosePropagatesWriteError(t *testing.T) {
	// Enter a code block successfully, then fail on the reset write during Close.
	fw := &failingWriter{limit: 1000}
	cw := NewColorWriter(fw)

	if _, err := cw.Write([]byte("```go\n")); err != nil {
		t.Fatalf("unexpected write error: %v", err)
	}
	fw.written = fw.limit // force the next write to fail
	if err := cw.Close(); !errors.Is(err, errWriteFailed) {
		t.Fatalf("expected Close to propagate write error, got %v", err)
	}
}
