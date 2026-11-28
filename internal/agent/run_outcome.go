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
}

// LastRunOutcome returns how the most recent run ended. It is the zero value
// until a run finishes.
func (a *Agent) LastRunOutcome() RunOutcome {
	a.outcomeMu.Lock()
	defer a.outcomeMu.Unlock()
	return a.lastOutcome
}

func (a *Agent) setRunOutcome(o RunOutcome) {
	a.outcomeMu.Lock()
	a.lastOutcome = o
	a.outcomeMu.Unlock()
}
