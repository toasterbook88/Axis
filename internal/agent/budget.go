package agent

// Context budget: what one request may hold so it fits the model's window.
// See docs/superpowers/specs/2026-11-05-agent-context-budget-design.md.

const (
	// replyReserveMax is the most room kept free for the model's reply.
	// The longest logged answer was about 1,700 tokens; tool-call arguments
	// need room too, and the cloud backend asks for 4,096.
	replyReserveMax = 4096
	// estimateFactorMax bounds the learned estimate factor k. A larger
	// ratio comes from content the chars/4 estimate cannot model (a badly
	// tokenizing result, a proxy-added prompt) and is not learned.
	estimateFactorMax = 2.0
)

// budgetState is the context budget tied to the active backend. The agent
// guards it with clientMu; SetBackend replaces it.
type budgetState struct {
	targetWindow  int     // the target's served window; 0 = unknown
	learnedWindow int     // window stated by an overflow error; 0 = none
	factor        float64 // estimate factor k; below 1 means 1
	gen           uint64  // bumped by SetBackend
}

// replyReserve is the room kept free for the reply in a budget of b tokens.
func replyReserve(b int) int {
	return min(replyReserveMax, b/8)
}

// budget returns the request budget B (the smallest known limit) and the
// usable part U = B minus the reply reserve.
func (a *Agent) budget() (b, u int) {
	a.clientMu.RLock()
	defer a.clientMu.RUnlock()
	b = a.maxTokens
	if a.bud.targetWindow > 0 {
		b = min(b, a.bud.targetWindow)
	}
	if a.bud.learnedWindow > 0 {
		b = min(b, a.bud.learnedWindow)
	}
	return b, b - replyReserve(b)
}

// estimateFactor returns k, the learned correction to the chars/4 estimate.
func (a *Agent) estimateFactor() float64 {
	a.clientMu.RLock()
	defer a.clientMu.RUnlock()
	return max(1, a.bud.factor)
}

// backendGeneration identifies the current backend. A round records it so
// an overflow from a backend switched away mid-round teaches nothing.
func (a *Agent) backendGeneration() uint64 {
	a.clientMu.RLock()
	defer a.clientMu.RUnlock()
	return a.bud.gen
}

// learnWindow lowers the learned window to n, if gen is still the current
// backend. It never raises it. It reports whether n was applied.
func (a *Agent) learnWindow(gen uint64, n int) bool {
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	if gen != a.bud.gen || n <= 0 {
		return false
	}
	if a.bud.learnedWindow == 0 || n < a.bud.learnedWindow {
		a.bud.learnedWindow = n
	}
	return true
}

// learnFactor raises k to ratio (stated tokens over the uncorrected
// estimate), if gen is still the current backend and ratio is within the
// cap. It never lowers k. It reports whether the ratio was accepted.
func (a *Agent) learnFactor(gen uint64, ratio float64) bool {
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	if gen != a.bud.gen || ratio > estimateFactorMax {
		return false
	}
	a.bud.factor = max(a.bud.factor, ratio)
	return true
}
