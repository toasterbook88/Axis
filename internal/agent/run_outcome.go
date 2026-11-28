package agent

// RunStop says why the most recent Run (or RunWithSinks) returned.
type RunStop string

const (
	// RunStopNone means no run has finished on this agent yet.
	RunStopNone RunStop = ""
	// RunStopAnswered means the model ended a round with no tool calls,
	// which the loop treats as its final answer.
	RunStopAnswered RunStop = "answered"
	// RunStopTurnCeiling means the loop used every turn (--max-turns)
	// without a final answer. Run returns nil in this case.
	RunStopTurnCeiling RunStop = "turn_ceiling"
	// RunStopFailed means Run returned an error.
	RunStopFailed RunStop = "failed"
)

// RunOutcome describes how the most recent run ended.
type RunOutcome struct {
	Stop     RunStop
	Turns    int // model rounds that returned a response in that run
	MaxTurns int // the turn ceiling that applied to that run
	// ScopeRefused lists, once each, the tools the model called that the
	// current autonomy mode hides but --autonomy full would expose. The
	// call failed and the model saw the error; nothing ran.
	ScopeRefused []string
}

// LastRunOutcome returns how the most recent run ended. It is the zero value
// until a run finishes.
func (a *Agent) LastRunOutcome() RunOutcome {
	a.outcomeMu.Lock()
	defer a.outcomeMu.Unlock()
	outcome := a.lastOutcome
	outcome.ScopeRefused = append([]string(nil), outcome.ScopeRefused...)
	return outcome
}

func (a *Agent) setRunOutcome(o RunOutcome) {
	a.outcomeMu.Lock()
	o.ScopeRefused = a.scopeRefused
	a.scopeRefused = nil
	a.lastOutcome = o
	a.outcomeMu.Unlock()
}

// resetScopeRefused clears the per-run list at the start of a run.
func (a *Agent) resetScopeRefused() {
	a.outcomeMu.Lock()
	a.scopeRefused = nil
	a.outcomeMu.Unlock()
}

// noteScopeRefused records a call the mode hid, when a wider mode would
// have exposed the tool. Tools no mode exposes are not recorded.
func (a *Agent) noteScopeRefused(name string) {
	if !a.tools.visibleUnderFull(name) {
		return
	}
	a.outcomeMu.Lock()
	defer a.outcomeMu.Unlock()
	for _, n := range a.scopeRefused {
		if n == name {
			return
		}
	}
	a.scopeRefused = append(a.scopeRefused, name)
}
