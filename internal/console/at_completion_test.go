package console

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func atTestModel(submit SubmitFunc, candidates []string) Model {
	return NewModel(Options{
		Submit:       submit,
		Now:          fixedNow,
		CancelGrace:  time.Millisecond,
		AtCandidates: func() []string { return candidates },
	})
}

func TestAtCompletionFiltersCandidates(t *testing.T) {
	m := typeText(atTestModel(noopSubmit, []string{"cranium", "foundry", "cachyos"}), "deploy to @cr")
	ac := m.atCompletion
	if !ac.active {
		t.Fatal("completion not active after typing @cr")
	}
	if ac.token != "cr" {
		t.Errorf("token = %q, want %q", ac.token, "cr")
	}
	if len(ac.candidates) != 1 || ac.candidates[0] != "cranium" {
		t.Errorf("candidates = %v, want [cranium]", ac.candidates)
	}
}

func TestAtCompletionCaseInsensitivePrefix(t *testing.T) {
	m := typeText(atTestModel(noopSubmit, []string{"Cranium", "foundry"}), "@CRAN")
	if !m.atCompletion.active {
		t.Fatal("completion not active for uppercase filter")
	}
	if len(m.atCompletion.candidates) != 1 || m.atCompletion.candidates[0] != "Cranium" {
		t.Errorf("candidates = %v, want [Cranium]", m.atCompletion.candidates)
	}
}

func TestAtCompletionNoMatchClears(t *testing.T) {
	m := typeText(atTestModel(noopSubmit, []string{"cranium"}), "@zzz")
	if m.atCompletion.active {
		t.Error("completion active with no matching candidates")
	}
}

func TestAtCompletionInactiveWithoutAt(t *testing.T) {
	m := typeText(atTestModel(noopSubmit, []string{"cranium"}), "plain words")
	if m.atCompletion.active {
		t.Error("completion active without @ token")
	}
}

func TestAtCompletionInactiveForBareAt(t *testing.T) {
	// '@' with no filter text yet: token is just "@", len < 2, inactive.
	m := typeText(atTestModel(noopSubmit, []string{"cranium"}), "@")
	if m.atCompletion.active {
		t.Error("completion active for bare @")
	}
}

func TestAtCompletionSpaceClearsState(t *testing.T) {
	m := typeText(atTestModel(noopSubmit, []string{"cranium"}), "@c done")
	if m.atCompletion.active {
		t.Error("completion survived a space (new token started)")
	}
}

func TestTabAcceptsCompletion(t *testing.T) {
	m := typeText(atTestModel(noopSubmit, []string{"cranium", "cachyos"}), "@cr")
	m, _ = press(m, tea.KeyTab)
	if m.Input() != "@cranium " {
		t.Errorf("input after Tab = %q, want %q", m.Input(), "@cranium ")
	}
	if m.atCompletion.active {
		t.Error("completion state survived acceptance")
	}
}

func TestTabAcceptsCommonPrefixGhost(t *testing.T) {
	// "cachyos" and "cranium" share no prefix beyond "c", but "cra"/"cranium"
	// does: typing @cr filters to one candidate whose common prefix is the
	// whole name; the ghost path accepts token+ghost.
	m := typeText(atTestModel(noopSubmit, []string{"cranium", "cachyos"}), "@c")
	// Two candidates match @c. Common prefix is "c", which equals the token,
	// so ghost is empty and Tab accepts the first candidate in sorted order.
	m, _ = press(m, tea.KeyTab)
	if m.Input() != "@cachyos " {
		t.Errorf("input after Tab = %q, want %q", m.Input(), "@cachyos ")
	}
}

func TestEscDismissesCompletionBeforeArmingEscEsc(t *testing.T) {
	m := typeText(atTestModel(noopSubmit, []string{"cranium"}), "@c")
	if !m.atCompletion.active {
		t.Fatal("completion not active before Esc")
	}
	m, _ = press(m, tea.KeyEsc)
	if m.atCompletion.active {
		t.Error("Esc did not dismiss completion")
	}
	if m.Input() != "@c" {
		t.Errorf("Esc modified input: %q", m.Input())
	}
	// The dismissal must not have armed esc-esc: this second Esc should arm,
	// not clear.
	m, _ = press(m, tea.KeyEsc)
	if m.Input() != "@c" {
		t.Errorf("esc-esc armed by completion dismissal: input %q", m.Input())
	}
	// Third Esc completes the (now armed) gesture and clears.
	m, _ = press(m, tea.KeyEsc)
	if m.Input() != "" {
		t.Errorf("esc esc did not clear after completion dismissed: %q", m.Input())
	}
}

func TestNavigationClearsCompletion(t *testing.T) {
	m := typeText(atTestModel(noopSubmit, []string{"cranium"}), "@c")
	m, _ = press(m, tea.KeyUp)
	if m.atCompletion.active {
		t.Error("Up did not clear completion")
	}

	m = typeText(atTestModel(noopSubmit, []string{"cranium"}), "@c")
	m, _ = press(m, tea.KeyLeft)
	if m.atCompletion.active {
		t.Error("Left did not clear completion")
	}
}

func TestBackspaceClearsCompletion(t *testing.T) {
	m := typeText(atTestModel(noopSubmit, []string{"cranium"}), "@c")
	m, _ = press(m, tea.KeyBackspace)
	if m.atCompletion.active {
		t.Error("Backspace did not clear completion state")
	}
	// Retyping reactivates.
	m = typeText(m, "c")
	if !m.atCompletion.active {
		t.Error("completion did not reactivate after retype")
	}
}

func TestAtCompletionDisabledWithoutSource(t *testing.T) {
	m := typeText(newTestModel(noopSubmit), "@cr")
	if m.atCompletion.active {
		t.Error("completion active with nil AtCandidates")
	}
}

func TestViewRendersGhostSuffix(t *testing.T) {
	m := typeText(atTestModel(noopSubmit, []string{"cranium"}), "@c")
	view := m.View()
	// Ghost text is part of the painted line.
	if !strings.Contains(view, "cranium") {
		t.Errorf("ghost suffix missing from view:\n%s", view)
	}
	// Plain input stays the typed text only.
	if !strings.Contains(view, "> @c") {
		t.Errorf("typed text missing from view:\n%s", view)
	}
}

func TestViewNoGhostWhenInactive(t *testing.T) {
	m := typeText(atTestModel(noopSubmit, []string{"cranium"}), "hello")
	if strings.Contains(m.View(), "cranium") {
		t.Errorf("ghost leaked into view without active completion:\n%s", m.View())
	}
}

func TestAcceptCompletionEditorRuneMath(t *testing.T) {
	e := NewEditor()
	e.SetText("deploy to @cr")
	// cursor already at end; token starts at rune 10.
	e.AcceptCompletion(10, "cranium")
	if e.Text() != "deploy to @cranium " {
		t.Errorf("text = %q", e.Text())
	}
	if e.Cursor() != len([]rune("deploy to @cranium ")) {
		t.Errorf("cursor = %d, want end", e.Cursor())
	}
}

func TestTokenBeforeCursorBoundaries(t *testing.T) {
	e := NewEditor()
	e.SetText("alpha beta @c")
	token, start := e.TokenBeforeCursor()
	if token != "@c" || start != 11 {
		t.Errorf("token = %q start = %d, want @c/11", token, start)
	}
	e.MoveLeft()
	e.MoveLeft()
	e.MoveLeft() // cursor mid-'beta'
	token, start = e.TokenBeforeCursor()
	if token != "beta" || start != 6 {
		t.Errorf("token = %q start = %d, want beta/6", token, start)
	}
}

func TestCommonPrefix(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"cranium", "cachyos"}, "c"},
		{[]string{"cranium"}, "cranium"},
		{[]string{"cranium", "foundry"}, ""},
		{nil, ""},
		{[]string{"CRAN", "cran"}, "CRAN"}, // case-insensitive compare, first item's case wins
	}
	for _, tc := range cases {
		if got := commonPrefix(tc.in); got != tc.want {
			t.Errorf("commonPrefix(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
