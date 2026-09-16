package ui

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/fatih/color"
)

func init() {
	// Force color output in tests so we can verify ANSI codes are present/absent.
	color.NoColor = false
}

func TestTableEmpty(t *testing.T) {
	tbl := NewTable("A", "B")
	var buf bytes.Buffer
	tbl.Render(&buf)
	if tbl.RowCount() != 0 {
		t.Errorf("expected 0 rows")
	}
}

func TestFprintError(t *testing.T) {
	var buf bytes.Buffer
	FprintError(&buf, "file not found", "check the path")
	out := buf.String()
	if !strings.Contains(out, "file not found") {
		t.Errorf("missing error message in %q", out)
	}
	if !strings.Contains(out, "check the path") {
		t.Errorf("missing hint in %q", out)
	}
}

func TestFprintErrorNoHint(t *testing.T) {
	var buf bytes.Buffer
	FprintError(&buf, "boom", "")
	out := buf.String()
	if strings.Contains(out, "hint:") {
		t.Errorf("unexpected hint in %q", out)
	}
}

func TestBoldf(t *testing.T) {
	got := Boldf("count: %d", 42)
	if !strings.Contains(got, "42") {
		t.Errorf("Boldf missing formatted value: %q", got)
	}
}

func TestFprintBold(t *testing.T) {
	var buf bytes.Buffer
	FprintBold(&buf, "bold text")
	if !strings.Contains(buf.String(), "bold text") {
		t.Errorf("FprintBold missing text: %q", buf.String())
	}
}

func TestInitDisablesColorWhenStdoutIsNotTTY(t *testing.T) {
	prev := color.NoColor
	prevEnv := os.Getenv("NO_COLOR")
	prevTTY := fileIsTerminal
	defer func() {
		color.NoColor = prev
		os.Setenv("NO_COLOR", prevEnv)
		fileIsTerminal = prevTTY
	}()

	os.Unsetenv("NO_COLOR")
	fileIsTerminal = func(*os.File) bool { return false }

	Init(false)
	if Enabled() {
		t.Error("expected color disabled when stdout is not a TTY")
	}
}

func TestPrintErrorToStderr(t *testing.T) {
	// PrintError writes to os.Stderr; just verify no panic.
	PrintError("test error", "")
}

func TestPrintWarningToStderr(t *testing.T) {
	PrintWarning("test warning")
}

func TestPrintSuccessToStderr(t *testing.T) {
	PrintSuccess("test success")
}

func TestStripANSI(t *testing.T) {
	styled := Bold("hello") + Green(" world")
	stripped := stripANSI(styled)
	if stripped != "hello world" {
		t.Errorf("stripANSI(%q) = %q, want 'hello world'", styled, stripped)
	}
}
