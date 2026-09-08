package console

import (
	"strings"
	"testing"

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
