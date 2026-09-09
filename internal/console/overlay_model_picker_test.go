package console

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func newTestPicker(reply chan<- string) *ModelPickerOverlay {
	return NewModelPickerOverlay("Select active model for task routing:", []PickerItem{
		{ID: "local:qwen3.8:8082", Label: "qwen3.8-27b", Detail: "Local node [qwen3.8-27b] (http://127.0.0.1:8082)"},
		{ID: "litellm:cloud-x", Label: "cloud-x", Detail: "Cloud [x]"},
	}, reply)
}

func TestModelPickerOverlayEscCancels(t *testing.T) {
	reply := make(chan string, 1)
	o := newTestPicker(reply)

	updated, cmd := o.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if updated != nil || cmd != nil {
		t.Fatalf("expected overlay to dismiss itself on esc, got updated=%v cmd=%v", updated, cmd)
	}
	if !o.Done() {
		t.Fatal("expected overlay to be marked done")
	}
	if got := <-reply; got != "" {
		t.Fatalf("expected empty id on cancel, got %q", got)
	}
}

func TestModelPickerOverlayIgnoresNonKeyMsgs(t *testing.T) {
	reply := make(chan string, 1)
	o := newTestPicker(reply)

	updated, cmd := o.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if updated == nil || cmd != nil {
		t.Fatalf("non-key messages must be ignored, got updated=%v cmd=%v", updated, cmd)
	}
	if o.Done() {
		t.Fatal("non-key message must not resolve the overlay")
	}
}

func TestModelPickerOverlayNavigation(t *testing.T) {
	reply := make(chan string, 1)
	o := newTestPicker(reply)

	if o.Cursor() != 0 {
		t.Fatalf("cursor starts at 0, got %d", o.Cursor())
	}
	o.Update(tea.KeyMsg{Type: tea.KeyUp})
	if o.Cursor() != 0 {
		t.Fatal("up at the top row must not move")
	}
	o.Update(tea.KeyMsg{Type: tea.KeyDown})
	if o.Cursor() != 1 {
		t.Fatalf("down moved to %d, want 1", o.Cursor())
	}
	o.Update(tea.KeyMsg{Type: tea.KeyDown})
	if o.Cursor() != 1 {
		t.Fatal("down at the bottom row must not move")
	}

	o.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := <-reply; got != "litellm:cloud-x" {
		t.Fatalf("enter selected %q, want litellm:cloud-x", got)
	}
	if !o.Done() {
		t.Fatal("enter must resolve the overlay")
	}
}

func TestModelPickerOverlayEnterOnDisabledNoop(t *testing.T) {
	reply := make(chan string, 1)
	o := NewModelPickerOverlay("pick", []PickerItem{
		{ID: "a", Label: "a"},
		{ID: "dead", Label: "dead", Disabled: true},
	}, reply)

	o.Update(tea.KeyMsg{Type: tea.KeyDown}) // cursor lands on the disabled row
	updated, _ := o.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if o.Done() || updated == nil {
		t.Fatal("enter on a disabled row must not resolve")
	}
	select {
	case got := <-reply:
		t.Fatalf("no reply expected, got %q", got)
	default:
	}
}

func TestModelPickerOverlayCtrlNavAliases(t *testing.T) {
	reply := make(chan string, 1)
	o := newTestPicker(reply)

	o.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if o.Cursor() != 1 {
		t.Fatalf("ctrl+n moved to %d, want 1", o.Cursor())
	}
	o.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if o.Cursor() != 0 {
		t.Fatalf("ctrl+p moved to %d, want 0", o.Cursor())
	}
	select {
	case got := <-reply:
		t.Fatalf("no reply expected, got %q", got)
	default:
	}
}
