package main

import (
	"fmt"
	"strings"

	"github.com/toasterbook88/axis/internal/console"
)

// startupModelLine is the model's settled status line. Only a model that
// answered the readiness probe is called ready; anything else says what is
// actually known about it.
func startupModelLine(target ModelChoice, verdict modelReadiness) console.StatusLine {
	name := modelLabel(target)
	line := console.StatusLine{Key: "model", State: console.StatusInfo}
	switch verdict {
	case modelReady:
		line.State, line.Text = console.StatusDone, name+" ready"
	case modelCold:
		line.Text = name + " selected — may still be loading"
	case modelDown:
		line.State, line.Text = console.StatusFailed, name+" selected — not answering"
	default:
		line.Text = name + " selected (not checked)"
	}
	return line
}

// startupNotes persists the resolver's captured output. When the model is
// down and will not be bound, its warning is a failure, not an aside; if the
// resolver printed none, one is written so the reason is never silent.
func (j *consoleStartupJob) startupNotes(out string, down bool, target ModelChoice) {
	warned := false
	for _, n := range noteLines(out) {
		state := console.StatusInfo
		if down && strings.HasPrefix(n, "Warning:") {
			state, warned = console.StatusFailed, true
		}
		j.note(state, n)
	}
	if down && !warned {
		j.note(console.StatusFailed, fmt.Sprintf("Warning: model %q is not answering.", modelLabel(target)))
	}
}
