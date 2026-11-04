package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// axisLogo is the 6-row Unicode ANSI Shadow figlet of "AXIS" (27 columns).
// Lines are stored at their natural width; the gradient maps columns against
// the widest row so every row shares the same horizontal color position.
// Strokes are block and box-drawing runes. There is no '_' ground line.
var axisLogo = []string{
	` █████╗ ██╗  ██╗██╗███████╗`,
	`██╔══██╗╚██╗██╔╝██║██╔════╝`,
	`███████║ ╚███╔╝ ██║███████╗`,
	`██╔══██║ ██╔██╗ ██║╚════██║`,
	`██║  ██║██╔╝ ██╗██║███████║`,
	`╚═╝  ╚═╝╚═╝  ╚═╝╚═╝╚══════╝`,
}

// logo gradient endpoints: bright cyan (top-left) → magenta (bottom-right).
const (
	logoStartR, logoStartG, logoStartB = 0.0, 240.0, 255.0
	logoEndR, logoEndG, logoEndB       = 185.0, 30.0, 255.0
)

// asciiLogo is the 5-row '#' fallback for terminals that cannot render the
// block and box-drawing glyphs (LC_ALL=C, Linux console). Letters A X I S are
// joined by one space.
var asciiLogo = []string{
	` ###  #   # #####  ####`,
	`#   #  # #    #   #    `,
	`#####   #     #    ### `,
	`#   #  # #    #       #`,
	`#   # #   # ##### #### `,
}

// maxRunes returns the widest row, in runes. Gradient columns are mapped
// against this so every row shares the same horizontal color position.
func maxRunes(rows []string) int {
	w := 0
	for _, l := range rows {
		if r := len([]rune(l)); r > w {
			w = r
		}
	}
	return w
}

// LogoWidth is the display width of the Unicode logo.
func LogoWidth() int { return maxRunes(axisLogo) }

// ASCIILogoWidth is the display width of the ASCII logo.
func ASCIILogoWidth() int { return maxRunes(asciiLogo) }

// LogoLines returns the Unicode logo rows painted at depth d. ColorNone yields
// the plain rows; otherwise every row ends with a reset so a caller can place
// rows anywhere without color bleeding into neighboring text.
func LogoLines(d ColorDepth) []string { return paintLogo(axisLogo, d) }

// ASCIILogoLines returns the plain 5-row '#' logo. It is unpainted; callers
// that want a gradient apply it themselves per depth.
func ASCIILogoLines() []string { return append([]string(nil), asciiLogo...) }

// paintLogo applies the diagonal gradient (t = 0.75*col + 0.25*row) from cyan
// to magenta, emitting color through FgRGB so it degrades per depth. Spaces
// and '_' stay uncolored so letterforms pop against the background.
func paintLogo(rows []string, d ColorDepth) []string {
	out := make([]string, len(rows))
	if d == ColorNone {
		copy(out, rows)
		return out
	}
	width := maxRunes(rows)
	for row, line := range rows {
		var sb strings.Builder
		rowFrac := float64(row) / float64(len(rows)-1)
		for col, ch := range []rune(line) {
			if ch == ' ' || ch == '_' {
				sb.WriteRune(ch)
				continue
			}
			colFrac := float64(col) / float64(width-1)
			t := 0.75*colFrac + 0.25*rowFrac
			if t > 1 {
				t = 1
			}
			r := logoStartR + t*(logoEndR-logoStartR)
			g := logoStartG + t*(logoEndG-logoStartG)
			b := logoStartB + t*(logoEndB-logoStartB)
			sb.WriteString(FgRGB(uint8(r), uint8(g), uint8(b), d))
			sb.WriteRune(ch)
		}
		sb.WriteString("\033[0m")
		out[row] = sb.String()
	}
	return out
}

// PrintLogo prints the AXIS logo followed by a centered version label. Color
// depth and glyph set follow the environment: the gradient degrades to 256 or
// 16 colors, and terminals without Unicode get the ASCII logo. When color is
// disabled the letterforms are printed plainly.
func PrintLogo(w io.Writer, version string) {
	depth := ColorNone
	if Enabled() {
		depth = DetectColorDepth(os.Getenv)
	}
	rows, width := axisLogo, LogoWidth()
	if !UnicodeSupported(os.Getenv) {
		rows, width = asciiLogo, ASCIILogoWidth()
	}

	for _, line := range paintLogo(rows, depth) {
		if depth == ColorNone {
			fmt.Fprintln(w, strings.TrimRight(line, " "))
		} else {
			fmt.Fprintln(w, line)
		}
	}

	// Centered version label beneath the logo.
	plain := "v" + version
	pad := max((width-len([]rune(plain)))/2, 0)
	if Enabled() {
		fmt.Fprintf(w, "%s%s\n", strings.Repeat(" ", pad), Dim(plain))
	} else {
		fmt.Fprintf(w, "%s%s\n", strings.Repeat(" ", pad), plain)
	}
}
