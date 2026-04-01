package ui

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

func init() {
	// Force color output in tests so we can verify ANSI codes are present/absent.
	color.NoColor = false
}

func TestInitNoColor(t *testing.T) {
	prev := color.NoColor
	prevEnv := os.Getenv("NO_COLOR")
	defer func() {
		color.NoColor = prev
		os.Setenv("NO_COLOR", prevEnv)
	}()

	Init(true)
	if Enabled() {
		t.Error("expected color disabled after Init(true)")
	}

	// With NO_COLOR unset and noColor=false, color should be enabled.
	os.Unsetenv("NO_COLOR")
	Init(false)
	if !Enabled() {
		t.Error("expected color enabled after Init(false) with NO_COLOR unset")
	}

	// With NO_COLOR set, Init(false) should still disable color.
	os.Setenv("NO_COLOR", "1")
	Init(false)
	if Enabled() {
		t.Error("expected color disabled when NO_COLOR env is set")
	}
}

func TestStatusIcon(t *testing.T) {
	ok := StatusIcon(true)
	if !strings.Contains(ok, "✓") {
		t.Errorf("expected check mark, got %q", ok)
	}
	fail := StatusIcon(false)
	if !strings.Contains(fail, "✗") {
		t.Errorf("expected cross mark, got %q", fail)
	}
}

func TestColorFunctions(t *testing.T) {
	// These should return non-empty strings.
	for name, fn := range map[string]func(...interface{}) string{
		"Bold":   Bold,
		"Green":  Green,
		"Yellow": Yellow,
		"Red":    Red,
		"Cyan":   Cyan,
		"Dim":    Dim,
	} {
		got := fn("hello")
		if got == "" {
			t.Errorf("%s returned empty string", name)
		}
		if !strings.Contains(got, "hello") {
			t.Errorf("%s output %q missing 'hello'", name, got)
		}
	}
}

func TestTableRender(t *testing.T) {
	tbl := NewTable("NAME", "STATUS", "RAM")
	tbl.AddRow("node-a", "complete", "8192 MB")
	tbl.AddRow("node-b", "error", "—")

	var buf bytes.Buffer
	tbl.Render(&buf)
	out := buf.String()

	if tbl.RowCount() != 2 {
		t.Errorf("expected 2 rows, got %d", tbl.RowCount())
	}
	for _, want := range []string{"NAME", "STATUS", "RAM", "node-a", "node-b", "8192 MB"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q", want)
		}
	}
}

func TestTableEmpty(t *testing.T) {
	tbl := NewTable("A", "B")
	var buf bytes.Buffer
	tbl.Render(&buf)
	if tbl.RowCount() != 0 {
		t.Errorf("expected 0 rows")
	}
}

func TestSpinnerNoColor(t *testing.T) {
	prev := color.NoColor
	color.NoColor = true
	defer func() { color.NoColor = prev }()

	var buf bytes.Buffer
	s := &Spinner{w: &buf}
	s.Start("loading")
	// In no-color mode, Start just prints the message and returns.
	out := buf.String()
	if !strings.Contains(out, "loading") {
		t.Errorf("expected message in no-color mode, got %q", out)
	}
}

func TestSpinnerStartStop(t *testing.T) {
	prev := color.NoColor
	color.NoColor = false
	defer func() { color.NoColor = prev }()

	var buf bytes.Buffer
	s := &Spinner{w: &buf}
	s.Start("working...")
	s.Update("still working...")
	s.Stop("done!")

	out := buf.String()
	if !strings.Contains(out, "done!") {
		t.Errorf("expected final message, got %q", out)
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

func TestFprintWarning(t *testing.T) {
	var buf bytes.Buffer
	FprintWarning(&buf, "degraded cluster")
	if !strings.Contains(buf.String(), "degraded cluster") {
		t.Errorf("missing warning message")
	}
}

func TestFprintSuccess(t *testing.T) {
	var buf bytes.Buffer
	FprintSuccess(&buf, "all good")
	if !strings.Contains(buf.String(), "all good") {
		t.Errorf("missing success message")
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

func TestApplyHelpTemplate(t *testing.T) {
	cmd := &cobra.Command{Use: "test", Short: "a test command"}
	ApplyHelpTemplate(cmd)
	// Just verify it doesn't panic and sets a template.
	if cmd.UsageTemplate() == "" {
		t.Error("expected non-empty usage template")
	}
}

func TestNewSpinner(t *testing.T) {
	s := NewSpinner()
	if s == nil {
		t.Fatal("expected non-nil spinner")
	}
	if s.w == nil {
		t.Error("expected non-nil writer")
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
