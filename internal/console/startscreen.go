package console

import (
	"strings"
	"unicode"

	"github.com/toasterbook88/axis/internal/ui"
)

// StartScreen renders the splash shown above the editor until the first turn.
// It is a pure function of its inputs so it can be tested without a terminal.
type StartScreen interface {
	Render(width, height int, s StartState) []Line
}

// StartState is everything the splash needs; the model fills it per View.
type StartState struct {
	Version string
	Unicode bool          // false selects the pure-ASCII rendering
	Depth   ui.ColorDepth // terminal color capability for the logo and accents
	Frame   string        // current spinner frame for Running lines
	Status  []StatusLine  // startup progress, in display order
}

// StatusState is the lifecycle of one startup status line.
type StatusState int

const (
	StatusInfo StatusState = iota
	StatusRunning
	StatusDone
	StatusFailed
)

// StatusLine is one startup progress row. Persist lines are printed to
// scrollback when the splash is dismissed.
type StatusLine struct {
	Key     string
	State   StatusState
	Text    string
	Persist bool
}

// startTier is the responsive layout bucket.
type startTier int

const (
	tierCompact startTier = iota
	tierMedium
	tierFull
)

// Thresholds match the footer tiers so the screen and footer collapse together.
const (
	fullMinWidth   = 72
	mediumMinWidth = 44
	minTallHeight  = 16
)

// pickTier chooses a layout. Height 0 means "unknown" and counts as tall, so a
// terminal that cannot report its size still gets the full splash. A known
// height must also hold the tier's rendered rows (rows reports them): a
// splash taller than the terminal would scroll its own logo away, so it falls
// to the next tier down instead.
func pickTier(width, height int, rows func(startTier) int) startTier {
	tall := height == 0 || height >= minTallHeight
	tier := tierCompact
	switch {
	case !tall:
		return tierCompact
	case width >= fullMinWidth:
		tier = tierFull
	case width >= mediumMinWidth:
		tier = tierMedium
	}
	for height > 0 && tier > tierCompact && rows(tier) > height {
		tier--
	}
	return tier
}

var asciiReplacer = strings.NewReplacer(
	"·", "-", "…", "...", "—", "-", "✓", "ok", "▲", "^", "❯", ">", "─", "-",
)

// asciiFallback maps the glyphs the start screen uses to ASCII. Any other
// non-ASCII rune (for example a model name) becomes '?' so the result is
// guaranteed pure ASCII for terminals such as LC_ALL=C or the Linux console.
func asciiFallback(s string) string {
	s = asciiReplacer.Replace(s)
	return strings.Map(func(r rune) rune {
		if r > 0x7E {
			return '?'
		}
		return r
	}, s)
}

// HeroScreen is the Option B splash: gradient logo, tips, startup status.
type HeroScreen struct{}

// seg is a run of text with an optional pre-painted form carrying SGR.
type seg struct{ text, painted string }

func plainSeg(s string) seg { return seg{text: s, painted: s} }
func dimSeg(s string) seg   { return seg{text: s, painted: ui.Dim(s)} }

// scrub replaces control characters (newlines, ESC) so untrusted status text
// can neither add rows nor smuggle escape sequences into a fixed-height layout.
func scrub(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// build joins segs into one Line clipped to max columns. Painted is set only
// when it differs from Text and nothing was clipped: clipping a string that
// already contains SGR could cut an escape in half, so a clipped line falls
// back to plain Text with the given style.
func build(max int, style Style, segs ...seg) Line {
	var text, painted strings.Builder
	for _, s := range segs {
		text.WriteString(s.text)
		painted.WriteString(s.painted)
	}
	full := text.String()
	clipped := clipWidth(full, max)
	l := Line{Text: clipped, Style: style}
	if clipped == full && painted.String() != full {
		l.Painted = painted.String()
	}
	return l
}

// Render lays out the splash for the given terminal size.
func (h HeroScreen) Render(width, height int, s StartState) []Line {
	tier := pickTier(width, height, func(t startTier) int { return len(h.renderTier(t, width, s)) })
	return h.renderTier(tier, width, s)
}

// renderTier lays out the splash in one tier.
func (HeroScreen) renderTier(tier startTier, width int, s StartState) []Line {
	max := width - 1
	if max < 0 {
		max = 0
	}
	// fix applies the ASCII mapping and strips control characters before any
	// width math so measuring and clipping see the final glyphs.
	fix := func(t string) string {
		t = scrub(t)
		if !s.Unicode {
			t = asciiFallback(t)
		}
		return t
	}
	sep := fix(" · ")
	label := "agent"
	if s.Version != "" {
		label += sep + "v" + fix(s.Version)
	}

	var out []Line
	add := func(l Line) { out = append(out, l) }
	blank := func() { add(Line{}) }

	switch tier {
	case tierCompact:
		head := "▲ AXIS"
		if s.Version != "" {
			head += " v" + fix(s.Version)
		}
		mark := fix("▲ AXIS")
		accent := ui.FgRGB(0, 240, 255, s.Depth)
		pm := mark
		if accent != "" {
			pm = accent + mark + resetSGR
		}
		rest := strings.TrimPrefix(fix(head), mark)
		add(build(max, StylePlain, seg{mark, pm}, plainSeg(rest)))
		add(build(max, StyleMuted, dimSeg("/help for commands")))
		if st, ok := compactStatus(s.Status); ok {
			add(statusLine(max, s, st, fix))
		}
		return out
	case tierMedium:
		out = append(out, s.logoRows(max, "")...)
		add(build(max, StyleMuted, dimSeg(label)))
		blank()
		add(build(max, StyleMuted, dimSeg(fix("1. Ask about the fleet or models."))))
		add(build(max, StyleMuted, dimSeg(fix("2. /help · /model"))))
	default:
		out = append(out, s.logoRows(max, "  "+label)...)
		blank()
		for _, t := range []string{
			"Tips for getting started:",
			"1. Ask about the fleet, models, or placement.",
			"2. /help for commands, /model to switch models.",
			"3. ! runs a shell command, @ completes a node name.",
		} {
			add(build(max, StyleMuted, dimSeg(fix(t))))
		}
	}
	blank()
	for _, st := range s.Status {
		add(statusLine(max, s, st, fix))
	}
	if len(s.Status) > 0 {
		blank()
	}
	return out
}

const resetSGR = "\x1b[0m"

// logoRows returns the logo rows clipped to max. suffix, when non-empty, is
// appended muted to the final row so the version sits beside the logo.
func (s StartState) logoRows(max int, suffix string) []Line {
	rows := ui.ASCIILogoLines()
	painted := rows
	if s.Unicode {
		rows = ui.LogoLines(ui.ColorNone)
		painted = ui.LogoLines(s.Depth)
	} else if accent := ui.FgRGB(0, 240, 255, s.Depth); accent != "" {
		// The ASCII logo ships unpainted; give it the gradient's start color.
		painted = make([]string, len(rows))
		for i, r := range rows {
			painted[i] = accent + r + resetSGR
		}
	}
	out := make([]Line, len(rows))
	for i := range rows {
		segs := []seg{{rows[i], painted[i]}}
		if i == len(rows)-1 && suffix != "" {
			t := scrub(suffix)
			if !s.Unicode {
				t = asciiFallback(t)
			}
			segs = append(segs, dimSeg(t))
		}
		out[i] = build(max, StylePlain, segs...)
	}
	return out
}

// compactStatus picks the single status the compact tier can afford: a failure
// outranks progress because the operator must see it.
func compactStatus(all []StatusLine) (StatusLine, bool) {
	for _, st := range all {
		if st.State == StatusFailed {
			return st, true
		}
	}
	for _, st := range all {
		if st.State == StatusRunning {
			return st, true
		}
	}
	return StatusLine{}, false
}

func statusLine(max int, s StartState, st StatusLine, fix func(string) string) Line {
	text := fix(st.Text)
	switch st.State {
	case StatusRunning:
		if s.Frame != "" {
			text = fix(s.Frame) + " " + text
		}
		return build(max, StyleMuted, dimSeg(text))
	case StatusDone:
		mark := fix("✓")
		return build(max, StylePlain, seg{mark, ui.Green(mark)}, plainSeg(" "+text))
	case StatusFailed:
		return build(max, StyleBad, plainSeg(text))
	}
	return build(max, StyleMuted, dimSeg(text))
}
