package console

import "strings"

// ephemeralBudget is the display-column budget for one ephemeral row. The
// last column stays empty so a full-width write cannot auto-wrap into
// scrollback. An unknown size uses the same 80-column assumption as the
// splash, rather than the transcript's 20-column floor, which would crush
// the live row on a normal terminal before the first resize.
func (m Model) ephemeralBudget() int {
	w := m.width
	if w <= 0 {
		w = defaultWidth
	}
	if w < 2 {
		return 1
	}
	return w - 1
}

// liveActivity is the in-flight turn: the answer being streamed, each pending
// tool, and one phase word. The phase word is printed only when nothing else
// already says what the turn is doing, so a tool call is not also "working".
// An open overlay keeps the tool rows and suppresses the phase word.
func (m Model) liveActivity() []Line {
	budget := m.ephemeralBudget()
	frame := spinnerFrame(m.spinner, m.unicode)
	var lines []Line
	showing := false

	if text := m.stream.String(); text != "" {
		_, answer, inThought := parseStreamThought(text)
		// A closed thought with no answer tokens yet is still the thinking
		// phase. The thought body stays out of this region either way.
		if inThought || strings.TrimSpace(answer) == "" {
			lines = append(lines, clipLine(frame+" thinking", budget, StyleMuted))
			showing = true
		} else {
			for _, l := range wrap(answer, budget) {
				lines = append(lines, Line{Text: l})
			}
			showing = true
		}
	}

	for _, pt := range m.pendingTools {
		// Fields collapses newlines so a tool argument cannot inject an
		// extra ephemeral row. The spinner and the name stay adjacent.
		row := frame + " " + strings.Join(strings.Fields(pt.name), " ")
		if args := strings.Join(strings.Fields(pt.args), " "); args != "" {
			row += "  " + args
		}
		lines = append(lines, clipLine(row, budget, StyleAccent))
		showing = true
	}

	if m.overlay != nil {
		return lines
	}
	switch m.state {
	case turnCancelling:
		lines = append(lines, clipLine(frame+" cancelling", budget, StyleMuted))
	case turnRunning:
		if !showing {
			lines = append(lines, clipLine(frame+" working", budget, StyleMuted))
		}
	}
	return lines
}

// clipLine clips text to budget display columns. A wide rune that would
// straddle the edge is dropped whole.
func clipLine(text string, budget int, style Style) Line {
	return Line{Text: clipWidth(text, budget), Style: style}
}
