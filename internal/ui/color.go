// Package ui is INTERNAL-ONLY — terminal colors, tables, spinners, and help templates.
// Pure infrastructure with no operator-facing contract.
package ui

import (
	"io"
	"os"

	"github.com/fatih/color"
	"github.com/mattn/go-isatty"
)

// Init configures the UI subsystem. Call once from PersistentPreRun.
// Color is disabled when noColor is true or the NO_COLOR env var is set.
// Otherwise color is explicitly enabled.
func Init(noColor bool) {
	color.NoColor = noColor || os.Getenv("NO_COLOR") != "" || !StdoutIsTerminal()
}

// Enabled reports whether color output is active.
func Enabled() bool { return !color.NoColor }

var fileIsTerminal = func(f *os.File) bool {
	if f == nil {
		return false
	}
	fd := f.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// StdinIsTerminal reports whether stdin is attached to a terminal.
func StdinIsTerminal() bool { return fileIsTerminal(os.Stdin) }

// StdoutIsTerminal reports whether stdout is attached to a terminal.
func StdoutIsTerminal() bool { return fileIsTerminal(os.Stdout) }

// StderrIsTerminal reports whether stderr is attached to a terminal.
func StderrIsTerminal() bool { return fileIsTerminal(os.Stderr) }

// --- Semantic color printers ---

var (
	bold   = color.New(color.Bold)
	green  = color.New(color.FgGreen)
	yellow = color.New(color.FgYellow)
	red    = color.New(color.FgRed)
	cyan   = color.New(color.FgCyan)
	dim    = color.New(color.Faint)
)

func Bold(a ...interface{}) string   { return bold.Sprint(a...) }
func Green(a ...interface{}) string  { return green.Sprint(a...) }
func Yellow(a ...interface{}) string { return yellow.Sprint(a...) }
func Red(a ...interface{}) string    { return red.Sprint(a...) }
func Cyan(a ...interface{}) string   { return cyan.Sprint(a...) }
func Dim(a ...interface{}) string    { return dim.Sprint(a...) }

// Boldf returns a bold-formatted string.
func Boldf(format string, a ...interface{}) string { return bold.Sprintf(format, a...) }

// StatusIcon returns a colored status symbol.
func StatusIcon(ok bool) string {
	if ok {
		return Green("✓")
	}
	return Red("✗")
}

// FprintBold writes bold text to w.
func FprintBold(w io.Writer, a ...interface{}) { bold.Fprint(w, a...) }
