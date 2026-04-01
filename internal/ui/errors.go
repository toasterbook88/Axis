package ui

import (
	"fmt"
	"io"
	"os"
)

// PrintError writes a red error with an optional hint to stderr.
func PrintError(msg string, hint string) {
	FprintError(os.Stderr, msg, hint)
}

// FprintError writes a formatted error to w.
func FprintError(w io.Writer, msg string, hint string) {
	fmt.Fprintf(w, "%s %s\n", Red("✗"), msg)
	if hint != "" {
		fmt.Fprintf(w, "  %s %s\n", Dim("hint:"), hint)
	}
}

// PrintWarning writes a yellow warning to stderr.
func PrintWarning(msg string) {
	FprintWarning(os.Stderr, msg)
}

// FprintWarning writes a yellow warning to w.
func FprintWarning(w io.Writer, msg string) {
	fmt.Fprintf(w, "%s %s\n", Yellow("⚠"), msg)
}

// PrintSuccess writes a green success message to stderr.
func PrintSuccess(msg string) {
	FprintSuccess(os.Stderr, msg)
}

// FprintSuccess writes a green success message to w.
func FprintSuccess(w io.Writer, msg string) {
	fmt.Fprintf(w, "%s %s\n", Green("✓"), msg)
}
