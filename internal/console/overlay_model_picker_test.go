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
