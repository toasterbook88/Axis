package console

import (
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

// Render draws the picker modal. Populated in the render task.
func (o *ModelPickerOverlay) Render(width int) []Line {
	return nil
}
