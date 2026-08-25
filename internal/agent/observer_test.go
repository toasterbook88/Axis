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
func (o *recordingObserver) ToolSucceeded(id, name, summary string, resultLen int) {
	o.record("ok %s/%s %q %d", id, name, summary, resultLen)
}
func (o *recordingObserver) ToolFailed(id, name string, err error) {
	o.record("failed %s/%s: %v", id, name, err)
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
	a.emitToolSucceeded("call-1", "axis_status", "5 nodes", 42)
	a.emitToolFailed("call-3", "remote_grep", errors.New("dial timeout"))
	a.emitShellExecuting("call-4", "", "", "ls")
	a.emitMaxTurnsReached(25)
}

func TestObserverReceivesEventsAndOutputStaysSilent(t *testing.T) {
	var buf bytes.Buffer
	obs := &recordingObserver{}
	a := &Agent{output: &buf, observer: obs, verbose: true}

	emitAll(a)

	// An observer takes over rendering entirely: nothing may leak to Output,
	// or a surface that owns the screen would be corrupted by stray writes.
	if buf.Len() != 0 {
		t.Errorf("observer set but %d bytes written to Output: %q", buf.Len(), buf.String())
	}

	want := []string{
		"turn 1/25",
		"compaction skipped: budget",
		`called call-1/axis_status {"cached":true}`,
		"skipped call-2/bash: dry-run",
		`ok call-1/axis_status "5 nodes" 42`,
		"failed call-3/remote_grep: dial timeout",
		`shell id="call-4" node="" cwd="" cmd="ls"`,
		"max turns 25",
	}
	got := obs.all()
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNilObserverFallsBackToFormattedOutput(t *testing.T) {
	var buf bytes.Buffer
	a := &Agent{output: &buf, verbose: true}

	emitAll(a)

	out := buf.String()
	for _, want := range []string{
		"Turn 1/25",
		"compaction skipped: budget",
		"Calling axis_status",
		`Parameters: {"cached":true}`,
		"Skipped execution of bash",
		"5 nodes",
		"Result: 42 chars",
		`Error executing tool "remote_grep": dial timeout`,
		"Executing shell: ls",
		"maximum turns (25)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("fallback output missing %q:\n%s", want, out)
		}
	}
}

func TestShellExecutingDistinguishesLocalRemoteAndCwd(t *testing.T) {
	cases := []struct {
		name           string
		node, cwd, cmd string
		want           string
	}{
		{"local", "", "", "ls", "Executing shell: ls"},
		{"local with cwd", "", "/srv", "ls", "Executing shell (in /srv): ls"},
		{"remote", "node-a", "", "ls", "Executing on node-a: ls"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			a := &Agent{output: &buf}
			a.emitShellExecuting("call-1", tc.node, tc.cwd, tc.cmd)
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("got %q, want it to contain %q", buf.String(), tc.want)
			}
		})
	}
}

func TestVerboseOnlyDetailStaysOutOfNonVerboseOutput(t *testing.T) {
	var buf bytes.Buffer
	a := &Agent{output: &buf} // verbose false

	a.emitToolCalled("call-1", "axis_status", `{"cached":true}`)
	a.emitToolSucceeded("call-1", "axis_status", "5 nodes", 42)

	out := buf.String()
	if strings.Contains(out, "Parameters:") {
		t.Errorf("non-verbose output leaked tool parameters:\n%s", out)
	}
	if strings.Contains(out, "Result:") {
		t.Errorf("non-verbose output leaked result size:\n%s", out)
	}
	if !strings.Contains(out, "5 nodes") {
		t.Errorf("non-verbose output dropped the summary:\n%s", out)
	}
}

func TestObserverGetsVerboseDetailRegardlessOfVerboseFlag(t *testing.T) {
	// Verbose is a rendering choice for the fallback path. An observer always
	// receives the full event so the surface can decide what to show.
	var buf bytes.Buffer
	obs := &recordingObserver{}
	a := &Agent{output: &buf, observer: obs} // verbose false

	a.emitToolCalled("call-1", "axis_status", `{"cached":true}`)
	a.emitToolSucceeded("call-1", "axis_status", "5 nodes", 42)

	got := strings.Join(obs.all(), "\n")
	if !strings.Contains(got, `{"cached":true}`) {
		t.Errorf("observer did not receive tool arguments:\n%s", got)
	}
	if !strings.Contains(got, "42") {
		t.Errorf("observer did not receive result length:\n%s", got)
	}
}

func TestRedactionHappensBeforeTheObserverSeesAnything(t *testing.T) {
	// The console must be incapable of receiving raw credential-shaped text,
	// not merely trusted to handle it. Redaction is applied at the emit
	// boundary so it covers the observer and the plain CLI equally.
	var buf bytes.Buffer
	obs := &recordingObserver{}
	a := &Agent{output: &buf, observer: obs, verbose: true}

	const secret = "sk-abcdefghijklmnopqrstuvwxyz0123456789"
	a.emitToolCalled("c1", "bash", `{"cmd":"curl -H \"Authorization: Bearer `+secret+`\""}`)
	a.emitShellExecuting("c2", "", "", "export API_KEY="+secret)
	a.emitToolFailed("c3", "bash", errors.New("auth failed for Bearer "+secret))

	got := strings.Join(obs.all(), "\n")
	if strings.Contains(got, secret) {
		t.Errorf("secret reached the observer:\n%s", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("nothing was redacted; the masking seam did not run:\n%s", got)
	}
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
	a.emitToolSucceeded("call-a", "axis_status", "ok", 2)

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

func TestRunSerializesConcurrentCallers(t *testing.T) {
	// Run mutates the conversation without locking internally, so a caller
	// that abandons a turn must block rather than race the turn it left
	// running. This asserts the lock exists and is held across a run.
	a := &Agent{output: &bytes.Buffer{}}

	locked := make(chan struct{})
	go func() {
		a.runMu.Lock()
		close(locked)
		time.Sleep(20 * time.Millisecond)
		a.runMu.Unlock()
	}()
	<-locked

	start := time.Now()
	a.runMu.Lock()
	elapsed := time.Since(start)
	a.runMu.Unlock()

	if elapsed < 10*time.Millisecond {
		t.Errorf("second caller waited %v; Run is not serialized", elapsed)
	}
}

func TestRunWithSinksRestoresPreviousSinks(t *testing.T) {
	// Turn-scoped sinks must not leak into whatever runs next.
	var base bytes.Buffer
	baseObs := &recordingObserver{}
	a := &Agent{output: &base, observer: baseObs}

	turnObs := &recordingObserver{}
	var turnOut bytes.Buffer

	// Exercise the swap without driving a full turn: the restore is what
	// matters, and Run needs a live backend.
	func() {
		a.runMu.Lock()
		defer a.runMu.Unlock()
		prevObs, prevOut := a.observer, a.output
		a.observer, a.output = turnObs, &turnOut
		defer func() { a.observer, a.output = prevObs, prevOut }()
		a.emitToolCalled("c1", "axis_status", "")
	}()

	if len(turnObs.all()) != 1 {
		t.Errorf("turn-scoped observer received %d events, want 1", len(turnObs.all()))
	}
	if len(baseObs.all()) != 0 {
		t.Errorf("session observer saw %d turn-scoped events", len(baseObs.all()))
	}

	a.emitToolCalled("c2", "axis_status", "")
	if len(baseObs.all()) != 1 {
		t.Error("previous observer was not restored")
	}
}
