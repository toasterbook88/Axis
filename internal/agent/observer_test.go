package agent

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingObserver captures emitted events. It is safe for concurrent use
// because the agent reports tool results from parallel dispatch goroutines.
type recordingObserver struct {
	mu     sync.Mutex
	events []string
}

func (o *recordingObserver) record(format string, args ...interface{}) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, fmt.Sprintf(format, args...))
}

func (o *recordingObserver) all() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.events...)
}

func (o *recordingObserver) TurnStarted(turn, max int) { o.record("turn %d/%d", turn, max) }
func (o *recordingObserver) CompactionSkipped(err error) {
	o.record("compaction skipped: %v", err)
}
func (o *recordingObserver) ToolCalled(id, name, args string) {
	o.record("called %s/%s %s", id, name, args)
}
func (o *recordingObserver) ToolSkipped(id, name, reason string) {
	o.record("skipped %s/%s: %s", id, name, reason)
}
func (o *recordingObserver) ToolSucceeded(id, name, summary string, resultLen int, elapsed time.Duration) {
	o.record("ok %s/%s %q %d %s", id, name, summary, resultLen, elapsed.Truncate(time.Millisecond))
}
func (o *recordingObserver) ToolFailed(id, name string, err error, elapsed time.Duration) {
	o.record("failed %s/%s: %v %s", id, name, err, elapsed.Truncate(time.Millisecond))
}
func (o *recordingObserver) ShellExecuting(id, node, cwd, command string) {
	o.record("shell id=%q node=%q cwd=%q cmd=%q", id, node, cwd, command)
}
func (o *recordingObserver) MaxTurnsReached(max int) { o.record("max turns %d", max) }

// emitAll drives every emit helper once with fixed arguments.
func emitAll(a *Agent) {
	a.emitTurnStarted(1, 25)
	a.emitCompactionSkipped(errors.New("budget"))
	a.emitToolCalled("call-1", "axis_status", `{"cached":true}`)
	a.emitToolSkipped("call-2", "bash", "dry-run")
	a.emitToolSucceeded("call-1", "axis_status", "5 nodes", 42, 120*time.Millisecond)
	a.emitToolFailed("call-3", "remote_grep", errors.New("dial timeout"), 40*time.Millisecond)
	a.emitShellExecuting("call-4", "", "", "ls")
	a.emitMaxTurnsReached(25)
}

func TestRedactionAlsoCoversTheFallbackOutput(t *testing.T) {
	var buf bytes.Buffer
	a := &Agent{output: &buf, verbose: true}

	const secret = "sk-abcdefghijklmnopqrstuvwxyz0123456789"
	a.emitShellExecuting("c1", "node-a", "", "export API_KEY="+secret)

	if strings.Contains(buf.String(), secret) {
		t.Errorf("secret reached the plain CLI output:\n%s", buf.String())
	}
}

func TestRedactionLeavesOrdinaryCommandsReadable(t *testing.T) {
	// Blanking every quoted argument would make the transcript useless as an
	// audit trail, which is the reason the command is shown at all.
	var buf bytes.Buffer
	a := &Agent{output: &buf}
	a.emitShellExecuting("c1", "", "", `grep -r "needle" ./src`)

	out := buf.String()
	if !strings.Contains(out, "needle") || !strings.Contains(out, "./src") {
		t.Errorf("redaction destroyed a benign command:\n%s", out)
	}
}

func TestToolIDsReachTheObserver(t *testing.T) {
	// Tools dispatch in parallel; the id is how a completion is matched to
	// its call.
	obs := &recordingObserver{}
	a := &Agent{output: &bytes.Buffer{}, observer: obs}

	a.emitToolCalled("call-a", "axis_status", "")
	a.emitToolSucceeded("call-a", "axis_status", "ok", 2, time.Millisecond)

	for _, e := range obs.all() {
		if !strings.Contains(e, "call-a") {
			t.Errorf("event lost its correlation id: %q", e)
		}
	}
}

func TestCompactionAndSkipReasonsAreRedacted(t *testing.T) {
	// Redaction coverage must match what the documentation claims. These two
	// paths forwarded raw text after the first pass.
	obs := &recordingObserver{}
	a := &Agent{output: &bytes.Buffer{}, observer: obs}

	const secret = "sk-abcdefghijklmnopqrstuvwxyz0123456789"
	a.emitCompactionSkipped(errors.New("budget exceeded for Bearer " + secret))
	a.emitToolSkipped("c1", "bash", "dry-run: Bearer "+secret)

	got := strings.Join(obs.all(), "\n")
	if strings.Contains(got, secret) {
		t.Errorf("secret survived redaction:\n%s", got)
	}
}
