package agent

import (
	"context"
	"sync"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
)

type approvalWaitKey struct{}

// approvalWait measures how long one tool call spent waiting on operator
// approval: its own prompts, time queued behind other prompts, and the
// prompts of nested dispatches it ran synchronously (spawn_subagent runs a
// child agent on the parent's context). Overlapping waits count once: the
// counter adds time only while at least one wait is open.
type approvalWait struct {
	parent *approvalWait

	mu     sync.Mutex
	open   int
	since  time.Time
	waited time.Duration
}

// begin opens a wait on w and every enclosing call's counter.
func (w *approvalWait) begin(now time.Time) {
	for c := w; c != nil; c = c.parent {
		c.mu.Lock()
		if c.open == 0 {
			c.since = now
		}
		c.open++
		c.mu.Unlock()
	}
}

// end closes a wait opened by begin.
func (w *approvalWait) end(now time.Time) {
	for c := w; c != nil; c = c.parent {
		c.mu.Lock()
		c.open--
		if c.open == 0 {
			c.waited += now.Sub(c.since)
		}
		c.mu.Unlock()
	}
}

func (w *approvalWait) total() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.waited
}

// timedDispatch runs one tool call and returns its execution time, with
// approval waits removed: a call declined after a long prompt never ran.
func (a *Agent) timedDispatch(ctx context.Context, tc chat.ToolCall) (string, time.Duration, error) {
	parent, _ := ctx.Value(approvalWaitKey{}).(*approvalWait)
	wait := &approvalWait{parent: parent}
	start := time.Now()
	result, err := a.dispatchToolCall(context.WithValue(ctx, approvalWaitKey{}, wait), tc)
	elapsed := time.Since(start) - wait.total()
	if elapsed < 0 {
		elapsed = 0
	}
	return result, elapsed, err
}

// gatedConfirm asks fn one question at a time and records the wait, queueing
// included, against the tool call in ctx and every call enclosing it.
func (a *Agent) gatedConfirm(ctx context.Context, fn ConfirmFunc, tool, desc string, score int) ConfirmResult {
	if wait, ok := ctx.Value(approvalWaitKey{}).(*approvalWait); ok {
		wait.begin(time.Now())
		defer func() { wait.end(time.Now()) }()
	}
	a.confirmGate.Lock()
	defer a.confirmGate.Unlock()
	return fn(tool, desc, score)
}
