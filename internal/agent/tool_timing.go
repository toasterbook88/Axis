package agent

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
)

type approvalWaitKey struct{}

// timedDispatch runs one tool call and returns its execution time. Time the
// call spends waiting on operator approval, including time queued behind
// other prompts, is not tool latency: a call declined after a long prompt
// never ran.
func (a *Agent) timedDispatch(ctx context.Context, tc chat.ToolCall) (string, time.Duration, error) {
	wait := new(atomic.Int64)
	start := time.Now()
	result, err := a.dispatchToolCall(context.WithValue(ctx, approvalWaitKey{}, wait), tc)
	elapsed := time.Since(start) - time.Duration(wait.Load())
	if elapsed < 0 {
		elapsed = 0
	}
	return result, elapsed, err
}

// gatedConfirm asks fn one question at a time and records the wait, queueing
// included, against the tool call in ctx.
func (a *Agent) gatedConfirm(ctx context.Context, fn ConfirmFunc, tool, desc string, score int) ConfirmResult {
	waitStart := time.Now()
	a.confirmGate.Lock()
	defer a.confirmGate.Unlock()
	result := fn(tool, desc, score)
	if wait, ok := ctx.Value(approvalWaitKey{}).(*atomic.Int64); ok {
		wait.Add(int64(time.Since(waitStart)))
	}
	return result
}
