package ui

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"golang.org/x/term"
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

func TestSelectNonTTYFallbackNeverBlocks(t *testing.T) {
	outBuf := &bytes.Buffer{}
	termVal := &mockTerminal{
		isTTY: false,
		out:   outBuf,
	}

	options := []SelectOption{
		{ID: "opt-1", Label: "Option A", Detail: "A detail"},
		{ID: "opt-2", Label: "Option B", Disabled: true},
	}

	res, err := Select(context.Background(), termVal, "Select option title:", options)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Selected {
		t.Error("expected Selected to be false for non-TTY fallback")
	}

	out := outBuf.String()
	if !strings.Contains(out, "Select option title:") {
		t.Errorf("missing title in output: %q", out)
	}
	if !strings.Contains(out, "Option A: A detail") {
		t.Errorf("missing Option A detail: %q", out)
	}
	if !strings.Contains(out, "Option B (disabled)") {
		t.Errorf("missing Option B status: %q", out)
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

func TestReadKeyUnixDecode(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	_, _ = w.Write([]byte{3, 13, 27})

	action, err := readKey(r, int(r.Fd()), true)
	if err != nil || action != ActionCancel {
		t.Errorf("expected ActionCancel for Ctrl+C, got %v, err %v", action, err)
	}

	action, err = readKey(r, int(r.Fd()), true)
	if err != nil || action != ActionEnter {
		t.Errorf("expected ActionEnter for Enter, got %v, err %v", action, err)
	}

	action, err = readKey(r, int(r.Fd()), true)
	if err != nil || action != ActionCancel {
		t.Errorf("expected ActionCancel for standalone ESC timeout, got %v, err %v", action, err)
	}
}

type mockFileTerminal struct {
	in    *os.File
	out   *os.File
	isTTY bool
}

func (m *mockFileTerminal) In() io.Reader  { return m.in }
func (m *mockFileTerminal) Out() io.Writer { return m.out }
func (m *mockFileTerminal) IsTTY() bool    { return m.isTTY }

func TestDefaultSelectorSelect(t *testing.T) {
	// Restorer for package variables
	oldMakeRaw := termMakeRaw
	oldRestore := termRestore
	oldGetSize := termGetSize
	defer func() {
		termMakeRaw = oldMakeRaw
		termRestore = oldRestore
		termGetSize = oldGetSize
	}()

	termMakeRaw = func(fd int) (*term.State, error) {
		return &term.State{}, nil
	}
	termRestore = func(fd int, state *term.State) error {
		return nil
	}
	termGetSize = func(fd int) (int, int, error) {
		return 80, 24, nil
	}

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	defer inR.Close()
	defer inW.Close()

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	defer outR.Close()
	defer outW.Close()

	termVal := &mockFileTerminal{
		in:    inR,
		out:   outW,
		isTTY: true,
	}

	selector := NewDefaultSelector(termVal)

	options := []SelectOption{
		{ID: "1", Label: "A"},
		{ID: "2", Label: "B"},
		{ID: "3", Label: "C", Disabled: true},
		{ID: "4", Label: "D"},
	}

	// Write key sequences: Down (skip disabled C to get to D), then Enter
	// Down is ESC [ B (27, 91, 66)
	// Enter is 13
	_, _ = inW.Write([]byte{27, '[', 'B', 27, '[', 'B', 13})

	res, err := selector.Select(context.Background(), "Title", options)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Selected {
		t.Error("expected Selected to be true")
	}
	if res.ID != "4" {
		t.Errorf("expected to select D (ID: 4) after skipping disabled C, got %q", res.ID)
	}
}

func TestSelectPanicRecovery(t *testing.T) {
	oldMakeRaw := termMakeRaw
	oldRestore := termRestore
	oldGetSize := termGetSize
	defer func() {
		termMakeRaw = oldMakeRaw
		termRestore = oldRestore
		termGetSize = oldGetSize
	}()

	restored := false
	termMakeRaw = func(fd int) (*term.State, error) {
		return &term.State{}, nil
	}
	termRestore = func(fd int, state *term.State) error {
		restored = true
		return nil
	}
	termGetSize = func(fd int) (int, int, error) {
		panic("test panic")
	}

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	defer inR.Close()
	defer inW.Close()

	termVal := &mockFileTerminal{
		in:    inR,
		out:   inR, // dummy
		isTTY: true,
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		if r != "test panic" {
			t.Errorf("unexpected panic value: %v", r)
		}
		if !restored {
			t.Error("expected termRestore to be called during panic recovery")
		}
	}()

	_, _ = Select(context.Background(), termVal, "Title", []SelectOption{{ID: "1", Label: "A"}})
}

func TestSelectTTYKeysAndPaging(t *testing.T) {
	oldMakeRaw := termMakeRaw
	oldRestore := termRestore
	oldGetSize := termGetSize
	defer func() {
		termMakeRaw = oldMakeRaw
		termRestore = oldRestore
		termGetSize = oldGetSize
	}()

	termMakeRaw = func(fd int) (*term.State, error) {
		return &term.State{}, nil
	}
	termRestore = func(fd int, state *term.State) error {
		return nil
	}
	termGetSize = func(fd int) (int, int, error) {
		// Height 8 (small terminal to force viewport limits)
		return 80, 8, nil
	}

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	defer inR.Close()
	defer inW.Close()

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	defer outR.Close()
	defer outW.Close()

	termVal := &mockFileTerminal{
		in:    inR,
		out:   outW,
		isTTY: true,
	}

	// Create 15 options to exceed the page size (max 5 in size 8 term)
	var options []SelectOption
	for i := 1; i <= 15; i++ {
		options = append(options, SelectOption{
			ID:    strings.Repeat("a", i),
			Label: strings.Repeat("L", i),
		})
	}

	// 1. Move Up (wrapping to option 15), then ESC (cancellation)
	// Up is ESC [ A (27, 91, 65)
	// ESC is 27
	_, _ = inW.Write([]byte{27, '[', 'A', 27})

	res, err := Select(context.Background(), termVal, "Title", options)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Selected {
		t.Error("expected Selected to be false on ESC")
	}

	// 2. Test Down wrapping and selecting
	// Write Down key 15 times, then Enter
	var keys []byte
	for i := 0; i < 15; i++ {
		keys = append(keys, 27, '[', 'B')
	}
	keys = append(keys, 13) // Enter
	_, _ = inW.Write(keys)

	res, err = Select(context.Background(), termVal, "Title", options)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Selected {
		t.Error("expected Selected to be true on Enter")
	}
	if res.Index != 0 {
		t.Errorf("expected index 0 after wrapping 15 times, got %d", res.Index)
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
