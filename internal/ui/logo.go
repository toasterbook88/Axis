package ui

import (
	"fmt"
	"io"
	"strings"
)

// axisLogo is a 9-row ANSI Shadow figlet rendering of "AXIS". Lines are stored
// at their natural width; the gradient maps columns against the widest row so
// every row shares the same horizontal color position. Backslashes are literal
// (raw string) — do not escape them.
var axisLogo = []string{
	`_____/\\\\\\\\\_____/\\\_______/\\\__/\\\\\\\\\\\_____/\\\\\\\\\\\___`,
	`___/\\\\\\\\\\\\\__\///\\\___/\\\/__\/////\\\///____/\\\/////////\\\_`,
	`__/\\\/////////\\\___\///\\\\\\/________\/\\\______\//\\\______\///__`,
	`_\/\\\_______\/\\\_____\//\\\\__________\/\\\_______\////\\\_________`,
	`_\/\\\\\\\\\\\\\\\______\/\\\\__________\/\\\__________\////\\\______`,
	`_\/\\\/////////\\\______/\\\\\\_________\/\\\_____________\////\\\___`,
	`_\/\\\_______\/\\\____/\\\////\\\_______\/\\\______/\\\______\//\\\__`,
	`_\/\\\_______\/\\\__/\\\/___\///\\\__/\\\\\\\\\\\_\///\\\\\\\\\\\/___`,
	`_\///________\///__\///_______\///__\///////////____\///////////_____`,
}

// logo gradient endpoints: bright cyan (top-left) → magenta (bottom-right).
const (
	logoStartR, logoStartG, logoStartB = 0.0, 240.0, 255.0
	logoEndR, logoEndG, logoEndB       = 185.0, 30.0, 255.0
)

// PrintLogo prints a styled 24-bit ANSI color gradient logo of AXIS followed by
// a centered version label. When color is disabled the letterforms are printed
// plainly (they render in any UTF-8 terminal).
func PrintLogo(w io.Writer, version string) {
	rows := len(axisLogo)
	width := 0
	for _, l := range axisLogo {
		if r := len([]rune(l)); r > width {
			width = r
		}
	}

	for row := range rows {
		line := axisLogo[row]
		if !Enabled() {
			fmt.Fprintln(w, strings.TrimRight(line, " "))
			continue
		}
		runes := []rune(line)
		rowFrac := float64(row) / float64(rows-1)
		for col := range runes {
			ch := runes[col]
			if ch == ' ' || ch == '_' {
				// Keep the dotted ground line and inter-glyph spacing uncolored
				// so the letterforms pop against the terminal background.
				fmt.Fprint(w, string(ch))
				continue
			}
			// 2D diagonal gradient: dominant horizontal sweep (cyan→magenta)
			// with a subtle vertical accent for depth.
			colFrac := float64(col) / float64(width-1)
			t := 0.75*colFrac + 0.25*rowFrac
			if t > 1 {
				t = 1
			}
			r := logoStartR + t*(logoEndR-logoStartR)
			g := logoStartG + t*(logoEndG-logoStartG)
			b := logoStartB + t*(logoEndB-logoStartB)
			fmt.Fprintf(w, "\033[38;2;%d;%d;%dm%c", int(r), int(g), int(b), ch)
		}
		fmt.Fprint(w, "\033[0m\n")
	}

	// Centered version label beneath the logo.
	plain := "v" + version
	pad := (width - len([]rune(plain))) / 2
	if pad < 0 {
		pad = 0
	}
	if Enabled() {
		fmt.Fprintf(w, "%s%s\n", strings.Repeat(" ", pad), Dim(plain))
	} else {
		fmt.Fprintf(w, "%s%s\n", strings.Repeat(" ", pad), plain)
	}
}
