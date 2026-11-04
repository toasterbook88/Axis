package console

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"

	"github.com/toasterbook88/axis/internal/ui"
)

// Embedded mode hosts the console inside a full-screen program (axis tui).
//
// The standalone console commits finished entries to terminal scrollback
// with tea.Println, which is a no-op on the alternate screen. Embedded, the
// console keeps committed entries itself and draws them in-frame: the
// transcript tail above the live region, re-wrapped to the current width on
// every frame, scrollable with pgup/pgdown.

// transcriptLog holds committed entries. It is shared by pointer so the
// value-receiver commit path can append to it; only the event loop touches
// it, so it needs no lock.
type transcriptLog struct {
	entries []Entry
	// scroll is how many lines the view is lifted off the bottom. Zero
	// follows new output.
	scroll int
	// unframed counts entries added since the last frame. While scrolled
	// up, the frame raises scroll by their height so the text being read
	// stays put.
	unframed int
}

// maxEmbeddedEntries bounds memory for very long sessions; the oldest
// entries are dropped first.
const maxEmbeddedEntries = 5000

func (l *transcriptLog) add(e Entry) {
	l.entries = append(l.entries, e)
	l.unframed++
	if over := len(l.entries) - maxEmbeddedEntries; over > 0 {
		l.entries = append([]Entry(nil), l.entries[over:]...)
	}
}

// lines renders every committed entry for a pane width columns wide, one
// blank line between entries so turns read as separate blocks.
func (l *transcriptLog) lines(width int, unicode bool) []Line {
	var out []Line
	for _, e := range l.entries {
		rendered := entryLines(e, width, unicode)
		if len(rendered) == 0 {
			continue
		}
		if len(out) > 0 {
			out = append(out, Line{})
		}
		out = append(out, rendered...)
	}
	return out
}

// entryLines renders one entry with its kind bar. Entries wrap at the
// renderer's minimum width at least, so on a pane narrower than that each
// row is clipped to the pane rather than allowed to overflow it.
func entryLines(e Entry, width int, unicode bool) []Line {
	rendered := e.Render(max(width-2, minWidth))
	bar := entryBar(e.Kind(), unicode)
	out := make([]Line, 0, len(rendered))
	for _, line := range rendered {
		body := line
		body.Gutter = ""
		if body.Painted == "" {
			body.Painted = Paint(body)
		}
		row := Line{
			Text:    bar.plain + line.Gutter + line.Text,
			Painted: bar.painted + ui.Dim(line.Gutter) + body.Painted,
		}
		if visibleWidth(row.Painted) > width {
			row = Line{
				Text:    clipRunes(row.Text, width),
				Painted: lipgloss.NewStyle().MaxWidth(width).Render(row.Painted),
			}
		}
		out = append(out, row)
	}
	return out
}

// framedNew is how many transcript lines the unframed entries add, counting
// the separator before each.
func (l *transcriptLog) framedNew(width int, unicode bool) int {
	n := 0
	for _, e := range l.entries[max(len(l.entries)-l.unframed, 0):] {
		if h := len(entryLines(e, width, unicode)); h > 0 {
			n += h + 1
		}
	}
	return n
}

type bar struct{ plain, painted string }

// entryBar is the left margin for one entry kind: a colored rule for the
// operator, tools, reasoning, approvals and errors; plain indent for the
// agent's own answer so it reads as the main text.
func entryBar(k Kind, unicode bool) bar {
	thick, thin, dotted := "┃ ", "│ ", "┊ "
	if !unicode {
		thick, thin, dotted = "| ", "| ", ": "
	}
	switch k {
	case KindUser:
		return bar{thick, ui.Cyan(thick)}
	case KindTool:
		return bar{thin, ui.Dim(thin)}
	case KindThinking:
		return bar{dotted, ui.Dim(dotted)}
	case KindApproval:
		return bar{thick, ui.Yellow(thick)}
	case KindError:
		return bar{thick, ui.Red(thick)}
	default:
		return bar{"  ", "  "}
	}
}

// Embedded reports whether the console draws its transcript in-frame.
func (m Model) Embedded() bool { return m.log != nil }

// TranscriptEntries returns how many entries the embedded transcript holds.
func (m Model) TranscriptEntries() int {
	if m.log == nil {
		return 0
	}
	return len(m.log.entries)
}

// scrollBy moves the embedded view; positive lifts it toward older lines.
func (m Model) scrollBy(delta int) {
	if m.log == nil {
		return
	}
	// The offset is measured against everything committed so far, drawn or
	// not; only entries arriving after this key shift the anchor.
	m.log.unframed = 0
	m.log.scroll = max(m.log.scroll+delta, 0)
}

// embeddedFrame fits the transcript tail and the live region into exactly
// m.height lines. The live region (stream, tools, input, footer) is pinned to
// the bottom; the transcript fills what is left, newest last.
func (m Model) embeddedFrame(live []Line) string {
	width := m.layoutWidth()
	if m.log.scroll > 0 && m.log.unframed > 0 {
		m.log.scroll += m.log.framedNew(width, m.unicode)
	}
	m.log.unframed = 0
	all := m.log.lines(width, m.unicode)
	if m.height <= 0 {
		return PaintAll(append(all, live...))
	}
	room := m.height - len(live)
	if room <= 0 {
		return PaintAll(live[len(live)-m.height:])
	}

	maxScroll := max(len(all)-room, 0)
	if m.log.scroll > maxScroll {
		m.log.scroll = maxScroll
	}
	end := len(all) - m.log.scroll
	start := max(end-room, 0)
	view := append([]Line(nil), all[start:end]...)

	if m.log.scroll > 0 && len(view) > 0 {
		arrow := "↓"
		if !m.unicode {
			arrow = "v"
		}
		view[len(view)-1] = Line{
			Text:  clipRunes(fmt.Sprintf("%s %d more line(s) below · pgdown", arrow, m.log.scroll), width),
			Style: StyleMuted,
		}
	}
	for len(view) < room {
		view = append(view, Line{})
	}
	return PaintAll(append(view, live...))
}

// SetInputMsg replaces the composer's draft, cursor at the end. A host uses it
// to hand the operator a prefilled question; nothing is submitted.
type SetInputMsg struct{ Text string }

func (m Model) setInput(text string) Model {
	m.editor.SetText(text)
	m.input = m.editor.Text()
	m.atCompletion = atCompletionState{}
	return m
}

// embeddedScrollKey applies a transcript scroll key in embedded mode and
// reports whether it consumed the key.
func (m Model) embeddedScrollKey(key string) bool {
	if m.log == nil {
		return false
	}
	page := max(m.height/2, 1)
	switch key {
	case "pgup":
		m.scrollBy(page)
	case "pgdown":
		m.scrollBy(-page)
	case "shift+up":
		m.scrollBy(1)
	case "shift+down":
		m.scrollBy(-1)
	default:
		return false
	}
	return true
}

// embeddedView is View in embedded mode: the start screen until the first
// prompt or the first committed entry (help, a notice), then the in-frame
// transcript. Either way the frame is exactly the pane height with the live
// region (input, footer) pinned to the bottom.
func (m Model) embeddedView(live []Line) string {
	if !m.startVisible() || len(m.log.entries) > 0 {
		return m.embeddedFrame(live)
	}
	start := m.startLines(len(live))
	for m.height > 0 && len(start)+len(live) < m.height {
		start = append(start, Line{})
	}
	frame := append(start, live...)
	if m.height > 0 && len(frame) > m.height {
		frame = frame[len(frame)-m.height:]
	}
	return PaintAll(frame)
}
