package console

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/toasterbook88/axis/internal/agent"
)

func TestApprovalOverlayApproveYes(t *testing.T) {
	reply := make(chan agent.ConfirmResult, 1)
	overlay := NewApprovalOverlay("shell", "cat ~/.axis/config.yaml", 20, reply)

	updated, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if updated != nil || cmd != nil {
		t.Fatalf("expected overlay to dismiss itself, got updated=%v, cmd=%v", updated, cmd)
	}
	if !overlay.Done() {
		t.Fatal("expected overlay to be marked done")
	}
	select {
	case res := <-reply:
		if res != agent.ConfirmYes {
			t.Fatalf("expected ConfirmYes, got %v", res)
		}
	default:
		t.Fatal("expected reply on channel")
	}
}

func TestApprovalOverlayDenyNo(t *testing.T) {
	reply := make(chan agent.ConfirmResult, 1)
	overlay := NewApprovalOverlay("shell", "rm -rf /tmp/test", 85, reply)

	updated, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if updated != nil {
		t.Fatal("expected overlay to dismiss itself on 'n'")
	}
	if !overlay.Done() {
		t.Fatal("expected overlay to be marked done")
	}
	select {
	case res := <-reply:
		if res != agent.ConfirmNo {
			t.Fatalf("expected ConfirmNo, got %v", res)
		}
	default:
		t.Fatal("expected reply on channel")
	}
}

func TestApprovalOverlayApproveAlways(t *testing.T) {
	reply := make(chan agent.ConfirmResult, 1)
	overlay := NewApprovalOverlay("status", "check node status", 10, reply)

	updated, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if updated != nil {
		t.Fatal("expected overlay to dismiss itself on 'a'")
	}
	select {
	case res := <-reply:
		if res != agent.ConfirmAlways {
			t.Fatalf("expected ConfirmAlways, got %v", res)
		}
	default:
		t.Fatal("expected reply on channel")
	}
}

func TestApprovalOverlayBlockNever(t *testing.T) {
	reply := make(chan agent.ConfirmResult, 1)
	overlay := NewApprovalOverlay("shell", "reboot", 95, reply)

	updated, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	if updated != nil {
		t.Fatal("expected overlay to dismiss itself on 'v'")
	}
	select {
	case res := <-reply:
		if res != agent.ConfirmNever {
			t.Fatalf("expected ConfirmNever, got %v", res)
		}
	default:
		t.Fatal("expected reply on channel")
	}
}

func TestApprovalOverlayEscDismissesWithDeny(t *testing.T) {
	reply := make(chan agent.ConfirmResult, 1)
	overlay := NewApprovalOverlay("shell", "hostname", 30, reply)

	updated, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if updated != nil {
		t.Fatal("expected overlay to dismiss itself on esc")
	}
	select {
	case res := <-reply:
		if res != agent.ConfirmNo {
			t.Fatalf("expected ConfirmNo on esc, got %v", res)
		}
	default:
		t.Fatal("expected reply on channel")
	}
}

func TestApprovalOverlayExplainToggle(t *testing.T) {
	reply := make(chan agent.ConfirmResult, 1)
	overlay := NewApprovalOverlay("shell", "ls -la", 20, reply)

	renderedBefore := PaintAll(overlay.Render(80))
	if strings.Contains(renderedBefore, "blast radius") {
		t.Fatal("expected explanation hidden initially")
	}

	updated, _ := overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if updated == nil {
		t.Fatal("expected overlay to stay active on '?'")
	}

	renderedAfter := PaintAll(overlay.Render(80))
	if !strings.Contains(renderedAfter, "blast radius") {
		t.Fatal("expected explanation visible after '?'")
	}
}

func TestApprovalOverlayRenderRiskBadges(t *testing.T) {
	highRisk := NewApprovalOverlay("destroy", "wipe disk", 85, nil)
	linesHigh := highRisk.Render(80)
	textHigh := PaintAll(linesHigh)
	if !strings.Contains(textHigh, "[HIGH RISK]") {
		t.Errorf("expected [HIGH RISK] in:\n%s", textHigh)
	}

	caution := NewApprovalOverlay("kill", "stop process", 45, nil)
	linesCaution := caution.Render(80)
	textCaution := PaintAll(linesCaution)
	if !strings.Contains(textCaution, "[CAUTION]") {
		t.Errorf("expected [CAUTION] in:\n%s", textCaution)
	}

	low := NewApprovalOverlay("status", "query facts", 15, nil)
	linesLow := low.Render(80)
	textLow := PaintAll(linesLow)
	if !strings.Contains(textLow, "[LOW RISK]") {
		t.Errorf("expected [LOW RISK] in:\n%s", textLow)
	}
}

func TestApprovalOverlayEnterDoesNotApprove(t *testing.T) {
	reply := make(chan agent.ConfirmResult, 1)
	overlay := NewApprovalOverlay("shell", "dangerous command", 90, reply)

	// Enter must not mean yes. Enter must not resolve ConfirmYes.
	updated, cmd := overlay.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated == nil || cmd != nil {
		t.Fatalf("expected overlay to stay active on Enter, got updated=%v, cmd=%v", updated, cmd)
	}
	if overlay.Done() {
		t.Fatal("expected overlay NOT to be done after Enter")
	}
	select {
	case res := <-reply:
		t.Fatalf("Enter must not send a reply, got %v", res)
	default:
		// expected: nothing sent on Enter
	}

	// Yes is an explicit 'y' only.
	updated, cmd = overlay.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if updated != nil || cmd != nil {
		t.Fatalf("expected overlay to dismiss on 'y', got updated=%v, cmd=%v", updated, cmd)
	}
	if !overlay.Done() {
		t.Fatal("expected overlay to be done after 'y'")
	}
	select {
	case res := <-reply:
		if res != agent.ConfirmYes {
			t.Fatalf("expected ConfirmYes on 'y', got %v", res)
		}
	default:
		t.Fatal("expected reply on channel after 'y'")
	}
}

func TestApprovalOverlayRenderUTF8ValidityAndAlignment(t *testing.T) {
	overlay := NewApprovalOverlay("shell", "cat /tmp/data.txt", 45, nil)

	widths := []int{30, 40, 60, 80, 100, 120, 200}
	for _, w := range widths {
		lines := overlay.Render(w)
		if len(lines) < 2 {
			t.Fatalf("expected at least 2 lines in rendered output for width %d, got %d", w, len(lines))
		}

		// Verify every line is valid UTF-8
		for i, line := range lines {
			if !utf8.ValidString(line.Text) {
				t.Fatalf("width %d: line %d contains invalid UTF-8 bytes: %q", w, i, line.Text)
			}
		}

		topBorder := lines[0].Text
		bottomBorder := lines[len(lines)-1].Text

		topRunes := utf8.RuneCountInString(topBorder)
		bottomRunes := utf8.RuneCountInString(bottomBorder)

		expectedBoxWidth := effectiveWidth(w) - 2
		if expectedBoxWidth < 30 {
			expectedBoxWidth = 30
		}

		if topRunes != expectedBoxWidth {
			t.Errorf("width %d: top border rune count = %d, expected %d", w, topRunes, expectedBoxWidth)
		}
		if bottomRunes != expectedBoxWidth {
			t.Errorf("width %d: bottom border rune count = %d, expected %d", w, bottomRunes, expectedBoxWidth)
		}
		if topRunes != bottomRunes {
			t.Errorf("width %d: top (%d) and bottom (%d) border widths do not match", w, topRunes, bottomRunes)
		}
	}
}
