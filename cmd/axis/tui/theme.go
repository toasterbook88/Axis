package tui

import "github.com/charmbracelet/lipgloss"

// Palette. 256-color codes; lipgloss downsamples for 16-color terminals and
// drops color entirely under NO_COLOR. The cyan → magenta accents match the
// gradient AXIS logo in internal/ui.
var (
	colAccent  = lipgloss.Color("45")  // cyan
	colAccent2 = lipgloss.Color("171") // magenta
	colOK      = lipgloss.Color("78")
	colWarn    = lipgloss.Color("214")
	colBad     = lipgloss.Color("203")
	colText    = lipgloss.Color("252")
	colMuted   = lipgloss.Color("245")
	colFaint   = lipgloss.Color("240")
	colTrack   = lipgloss.Color("237")
	colSelBg   = lipgloss.Color("236")
)

var (
	styleText   = lipgloss.NewStyle().Foreground(colText)
	styleMuted  = lipgloss.NewStyle().Foreground(colMuted)
	styleFaint  = lipgloss.NewStyle().Foreground(colFaint)
	styleAccent = lipgloss.NewStyle().Foreground(colAccent)
	styleBold   = lipgloss.NewStyle().Foreground(colText).Bold(true)
	styleOK     = lipgloss.NewStyle().Foreground(colOK)
	styleWarn   = lipgloss.NewStyle().Foreground(colWarn)
	styleBad    = lipgloss.NewStyle().Foreground(colBad)
	styleKey    = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	styleColHdr = lipgloss.NewStyle().Foreground(colMuted).Bold(true)
	styleTabOn  = lipgloss.NewStyle().Foreground(colAccent).Bold(true).Underline(true)
	styleTabOff = lipgloss.NewStyle().Foreground(colMuted)
)

// level grades a measurement for color.
type level int

const (
	levelOK level = iota
	levelWarn
	levelBad
	levelNone // not observed
)

func (l level) style() lipgloss.Style {
	switch l {
	case levelWarn:
		return styleWarn
	case levelBad:
		return styleBad
	case levelNone:
		return styleFaint
	default:
		return styleOK
	}
}

// usageLevel grades a 0..1 utilization fraction.
func usageLevel(frac float64) level {
	switch {
	case frac >= 0.9:
		return levelBad
	case frac >= 0.7:
		return levelWarn
	default:
		return levelOK
	}
}

// glyphSet holds every non-ASCII character the dashboard draws, so terminals
// that cannot render Unicode (TERM=linux, a C locale) get a pure-ASCII frame.
type glyphSet struct {
	unicode                bool
	ok, warn, bad, unknown string
	cursor                 string
	barFull, barEmpty      string
	sep                    string
	ellipsis               string
	rule                   string
	refreshing             string
	warning                string
	dash                   string
	navKeys, tabKeys       string // footer legend
	helpNav, helpTab       string // help overlay legend
	border                 lipgloss.Border
}

var unicodeGlyphs = glyphSet{
	unicode: true,
	ok:      "●", warn: "▲", bad: "✖", unknown: "○",
	cursor:  "▌",
	barFull: "━", barEmpty: "━",
	sep:        " · ",
	ellipsis:   "…",
	rule:       "─",
	refreshing: "↻",
	warning:    "⚠",
	dash:       "—",
	navKeys:    "↑↓", tabKeys: "←→",
	helpNav: "↑ k  ↓ j", helpTab: "← h  → l",
	border: lipgloss.RoundedBorder(),
}

var asciiGlyphs = glyphSet{
	ok: "*", warn: "^", bad: "x", unknown: "o",
	cursor:  ">",
	barFull: "=", barEmpty: "-",
	sep:        " | ",
	ellipsis:   "~",
	rule:       "-",
	refreshing: "~",
	warning:    "!",
	dash:       "-",
	navKeys:    "j/k", tabKeys: "h/l",
	helpNav: "j  k", helpTab: "h  l",
	border: lipgloss.Border{
		Top: "-", Bottom: "-", Left: "|", Right: "|",
		TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
	},
}
