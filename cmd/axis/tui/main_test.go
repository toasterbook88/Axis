package tui

import (
	"os"
	"testing"
)

// TestMain pins the terminal environment the golden frames assume. NewModel picks Unicode or
// ASCII glyphs from TERM and the locale, so without this the tests fail wherever TERM=dumb or
// TERM=linux, as in agent shells. Tests of the ASCII frame set asciiGlyphs explicitly.
func TestMain(m *testing.M) {
	_ = os.Setenv("TERM", "xterm-256color")
	_ = os.Setenv("LANG", "C.UTF-8")
	_ = os.Unsetenv("LC_ALL")
	_ = os.Unsetenv("LC_CTYPE")
	os.Exit(m.Run())
}
