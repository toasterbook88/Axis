package console

import "testing"

// The footer and title must name the cluster grant, not fall back to the
// wider observe label.
func TestChromeModeLabels(t *testing.T) {
	for in, want := range map[string]string{
		"cluster": "cluster",
		"default": "observe",
		"":        "observe",
		"edit":    "edit",
		"full":    "exec",
		"exec":    "exec",
	} {
		if got := chromeMode(in); got != want {
			t.Errorf("chromeMode(%q) = %q, want %q", in, got, want)
		}
		m := Model{mode: func() string { return in }}
		if got := m.modeLabel(); got != want {
			t.Errorf("modeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
