package ui

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

type mockTerminal struct {
	in    *bytes.Buffer
	out   *bytes.Buffer
	isTTY bool
}

func (m *mockTerminal) In() io.Reader  { return m.in }
func (m *mockTerminal) Out() io.Writer { return m.out }
func (m *mockTerminal) IsTTY() bool    { return m.isTTY }

func TestSelectEmptyOptions(t *testing.T) {
	termVal := &mockTerminal{isTTY: false, out: &bytes.Buffer{}}
	_, err := Select(context.Background(), termVal, "Test Label", nil)
	if err == nil {
		t.Fatal("expected error when options are empty")
	}
	if !strings.Contains(err.Error(), "no options provided") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestSelectTerminalRawModeError(t *testing.T) {
	prevTTY := fileIsTerminal
	defer func() { fileIsTerminal = prevTTY }()
	fileIsTerminal = func(*os.File) bool { return true }

	invalidFile := os.NewFile(^uintptr(0), "invalid")
	termVal := NewStdTerminal(invalidFile, invalidFile)
	options := []SelectOption{{ID: "opt-1", Label: "Option A"}}

	_, err := Select(context.Background(), termVal, "Title", options)
	if err == nil {
		t.Fatal("expected error from term.MakeRaw on invalid file descriptor")
	}
	if !strings.Contains(err.Error(), "failed to make raw terminal") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestSanitizeAndTruncate(t *testing.T) {
	inputs := []struct {
		str      string
		limit    int
		expected string
	}{
		{"hello world", 5, "he..."},
		{"\x1b[31mred\x1b[0m text", 20, "red text"},
		{"control\u0007char", 20, "controlchar"},
		{"abc", 2, "ab"},
		{"a", 0, ""},
	}

	for _, tc := range inputs {
		got := sanitizeAndTruncate(tc.str, tc.limit)
		if got != tc.expected {
			t.Errorf("sanitizeAndTruncate(%q, %d) = %q, expected %q", tc.str, tc.limit, got, tc.expected)
		}
	}
}

func TestStdTerminalIsTTY(t *testing.T) {
	// Verify StdTerminal IsTTY logic
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	prevTTY := fileIsTerminal
	defer func() { fileIsTerminal = prevTTY }()

	fileIsTerminal = func(f *os.File) bool {
		return f == r || f == w
	}

	st := NewStdTerminal(r, w)
	if !st.IsTTY() {
		t.Error("expected IsTTY to return true when both are terminals")
	}

	// Test non-os.File types
	st2 := NewStdTerminal(bytes.NewBuffer(nil), w)
	if st2.IsTTY() {
		t.Error("expected IsTTY to return false for non-*os.File inputs")
	}
}
