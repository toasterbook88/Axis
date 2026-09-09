package console

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// PickerItem is one selectable row in the model picker.
type PickerItem struct {
	ID       string // ModelChoice.ID — what the switch consumes
	Label    string // model name
	Detail   string // provider/node/endpoint line
	Disabled bool   // unreachable or unsupported targets are shown but not selectable
}

// ModelPickerOverlay presents the interactive /model chooser as a floating
// modal. Unlike approvals, it carries no timeout: it is operator-initiated,
// the overlay owns the keyboard while open, and Esc dismisses without a
// switch, so nothing can be left blocked against an operator's intent.
type ModelPickerOverlay struct {
	title string
	items []PickerItem
	reply chan<- string // selected PickerItem.ID, or "" on cancel

	cursor int
	done   bool
}

// compile-time check that ModelPickerOverlay satisfies Overlay
var _ Overlay = (*ModelPickerOverlay)(nil)

// NewModelPickerOverlay constructs the modal. The reply channel receives the
// selected PickerItem.ID, or "" when the operator dismisses it.
func NewModelPickerOverlay(title string, items []PickerItem, reply chan<- string) *ModelPickerOverlay {
	return &ModelPickerOverlay{title: title, items: items, reply: reply}
}

// Done reports whether the picker has resolved.
func (o *ModelPickerOverlay) Done() bool { return o.done }

// Cursor returns the currently highlighted row index.
func (o *ModelPickerOverlay) Cursor() int { return o.cursor }

func (o *ModelPickerOverlay) resolve(id string) {
	if o.done {
		return
	}
	o.done = true
	if o.reply != nil {
		select {
		case o.reply <- id:
		default:
		}
	}
}

// Update handles a message while the picker owns the keyboard. Resolving
// keys return (nil, nil) so the model dismisses the overlay.
func (o *ModelPickerOverlay) Update(msg tea.Msg) (Overlay, tea.Cmd) {
	if o.done {
		return nil, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return o, nil
	}
	switch key.String() {
	case "esc", "ctrl+c", "q":
		o.resolve("")
		return nil, nil
	case "up", "k", "ctrl+p":
		if o.cursor > 0 {
			o.cursor--
		}
	case "down", "j", "ctrl+n":
		if o.cursor < len(o.items)-1 {
			o.cursor++
		}
	case "enter":
		if it := o.items[o.cursor]; !it.Disabled {
			o.resolve(it.ID)
			return nil, nil
		}
	}
	return o, nil
}

// pickerWindowRows caps how many item rows render at once; the window
// follows the cursor so the highlighted row stays visible.
const pickerWindowRows = 12

// clipRunes truncates s to n runes, appending "…" when it bites.
func clipRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

// Render draws the picker modal in the same bordered style as the approval
// overlay: accent border, cursor glyph on the active row, muted rows for
// disabled entries, and a bounded scroll window for long catalogs.
func (o *ModelPickerOverlay) Render(width int) []Line {
	width = effectiveWidth(width)
	boxWidth := width - 2
	if boxWidth < 30 {
		boxWidth = 30
	}

	var lines []Line

	title := strings.TrimSpace(o.title)
	if title == "" {
		title = "Select Model"
	}
	titlePrefix := "┌─ " + title + " ─"
	if runes := utf8.RuneCountInString(titlePrefix); runes > boxWidth {
		titlePrefix = clipRunes(titlePrefix, boxWidth)
	} else {
		titlePrefix += strings.Repeat("─", boxWidth-runes)
	}
	lines = append(lines, Line{Text: titlePrefix, Style: StyleAccent})

	avail := boxWidth - 6 // "│ " + two-cell cursor column + padding
	if avail < 10 {
		avail = 10
	}

	start := o.cursor - 5
	if start < 0 {
		start = 0
	}
	if maxStart := len(o.items) - pickerWindowRows; start > maxStart {
		start = maxStart
	}
	if start < 0 {
		start = 0
	}
	end := start + pickerWindowRows
	if end > len(o.items) {
		end = len(o.items)
	}

	if start > 0 {
		lines = append(lines, Line{Text: fmt.Sprintf("│   … %d above", start), Style: StyleMuted})
	}
	for i := start; i < end; i++ {
		it := o.items[i]
		glyph := "  "
		style := StylePlain
		if i == o.cursor {
			glyph = "▸ "
			style = StyleAccent
		}
		if it.Disabled {
			style = StyleMuted
		}
		text := it.Label
		if it.Disabled && !strings.Contains(text, "unreachable") {
			text += " (unreachable)"
		}
		if it.Detail != "" {
			text += " — " + it.Detail
		}
		lines = append(lines, Line{Text: clipRunes("│ "+glyph+text, boxWidth-1), Style: style})
	}
	if end < len(o.items) {
		lines = append(lines, Line{Text: fmt.Sprintf("│   … %d below", len(o.items)-end), Style: StyleMuted})
	}

	lines = append(lines, Line{Text: "│", Style: StyleMuted})
	lines = append(lines, Line{Text: "│ ↑/↓ navigate  enter select  esc cancel", Style: StyleStrong})
	lines = append(lines, Line{Text: fmt.Sprintf("└%s", strings.Repeat("─", boxWidth-1)), Style: StyleAccent})

	return lines
}
