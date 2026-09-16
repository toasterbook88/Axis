package console

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

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

func renderPlain(lines []Line) string {
	return strings.Join(PlainAll(lines), "\n")
}

func TestModelPickerOverlayRender(t *testing.T) {
	reply := make(chan string, 1)
	o := newTestPicker(reply)

	lines := o.Render(80)
	plain := renderPlain(lines)

	if !strings.Contains(plain, "Select active model for task routing") {
		t.Fatalf("title missing from border:\n%s", plain)
	}
	if !strings.Contains(plain, "└") {
		t.Fatal("bottom border missing")
	}
	if n := strings.Count(plain, "▸"); n != 1 {
		t.Fatalf("cursor glyph count %d, want 1\n%s", n, plain)
	}
	for _, want := range []string{"qwen3.8-27b", "cloud-x", "esc cancel"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("render missing %q\n%s", want, plain)
		}
	}
	// top border + 2 items + blank separator + hint + bottom border
	if len(lines) != 6 {
		t.Fatalf("line count %d, want 6\n%s", len(lines), plain)
	}
}

func TestModelPickerOverlayRenderDisabled(t *testing.T) {
	o := NewModelPickerOverlay("pick", []PickerItem{
		{ID: "x", Label: "x", Detail: "Local [x]"},
		{ID: "dead", Label: "dead", Detail: "Remote node (unreachable)", Disabled: true},
	}, make(chan string, 1))

	plain := renderPlain(o.Render(80))
	if !strings.Contains(plain, "dead (unreachable)") {
		t.Fatalf("disabled row must carry the unreachable marker:\n%s", plain)
	}
	if strings.Contains(plain, "(unreachable) (unreachable)") {
		t.Fatal("unreachable marker must not double-append")
	}
}

func TestModelPickerOverlayRenderScrollWindow(t *testing.T) {
	var items []PickerItem
	for i := 0; i < 15; i++ {
		items = append(items, PickerItem{ID: fmt.Sprintf("m%02d", i), Label: fmt.Sprintf("model-%02d", i)})
	}
	o := NewModelPickerOverlay("pick", items, make(chan string, 1))

	for i := 0; i < 10; i++ {
		o.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	plain := renderPlain(o.Render(80))
	if !strings.Contains(plain, "model-10") {
		t.Fatalf("cursor row must be inside the render window:\n%s", plain)
	}
	if !strings.Contains(plain, "3 above") {
		t.Fatalf("elided rows above must be marked:\n%s", plain)
	}
	if strings.Contains(plain, "model-00") || strings.Contains(plain, "model-01") || strings.Contains(plain, "model-02") {
		t.Fatalf("rows above the window must be elided:\n%s", plain)
	}
}

func TestModelPickerOverlayEmptyCatalogEnterNoop(t *testing.T) {
	reply := make(chan string, 1)
	o := NewModelPickerOverlay("pick", nil, reply)

	updated, _ := o.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if o.Done() || updated == nil {
		t.Fatal("enter on an empty catalog must be a no-op, not a panic")
	}
}

func TestModelPickerOverlayNilReplyResolveSafe(t *testing.T) {
	o := NewModelPickerOverlay("pick", []PickerItem{{ID: "a", Label: "a"}}, nil)

	updated, _ := o.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated != nil || !o.Done() {
		t.Fatal("nil reply must still resolve and dismiss")
	}
}

func TestModelPickerOverlayUpdateAfterDoneIgnoresKeys(t *testing.T) {
	reply := make(chan string, 1)
	o := newTestPicker(reply)
	o.Update(tea.KeyMsg{Type: tea.KeyEnter})

	updated, cmd := o.Update(tea.KeyMsg{Type: tea.KeyDown})
	if updated != nil || cmd != nil {
		t.Fatal("Update after resolution must be inert")
	}
	if got := <-reply; got != "local:qwen3.8:8082" {
		t.Fatalf("first resolve %q", got)
	}
	select {
	case extra := <-reply:
		t.Fatalf("no second resolve expected, got %q", extra)
	default:
	}
}

func TestModelPickerOverlayRenderNarrowWidth(t *testing.T) {
	o := newTestPicker(make(chan string, 1))
	for _, l := range o.Render(34) {
		if n := utf8.RuneCountInString(l.Text); n > 33 {
			t.Fatalf("row %q occupies %d cells, overflows the %d-wide box", l.Text, n, 34-2)
		}
	}
}
