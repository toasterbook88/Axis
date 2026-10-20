package console

import tea "github.com/charmbracelet/bubbletea"

// Spinner frames. The Unicode set is a single thin line that rotates; the
// ASCII set is the fallback for terminals that cannot render it.
var (
	spinnerFramesUnicode = [4]string{"─", "╲", "│", "╱"}
	spinnerFramesASCII   = [4]string{"-", "\\", "|", "/"}
)

// spinnerFrame returns frame i modulo 4. It is the only place frames are read,
// so every busy indicator on screen stays in step.
func spinnerFrame(i int, unicode bool) string {
	i %= 4
	if i < 0 {
		i += 4
	}
	if unicode {
		return spinnerFramesUnicode[i]
	}
	return spinnerFramesASCII[i]
}

// animating reports whether anything on screen needs the spinner to advance.
// The tick chain stops as soon as this is false so an idle console is silent.
func (m Model) animating() bool {
	return m.state != turnIdle
}

// ensureTick starts the spinner tick chain unless one is already in flight.
// Two chains would double the animation speed and never die, so every site
// that wants animation goes through here.
func (m *Model) ensureTick() tea.Cmd {
	if m.ticking {
		return nil
	}
	m.ticking = true
	return spinnerTick()
}
