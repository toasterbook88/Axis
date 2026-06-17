package agent

import (
	"bytes"
	"strings"
	"testing"
)

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
