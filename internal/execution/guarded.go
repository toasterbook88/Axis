package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/git"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/safety"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

const (
	ModeScript = "script"
	ModeExec   = "exec"

	ConfirmWord = "YES"

	StateChangeExecutionReserved = "execution-reserved"
	StateChangeExecutionFinished = "execution-finished"

	OwnerSurfaceGuardedExec    = "guarded-exec"
	OwnerSurfaceTaskRun        = "task-run"
	OwnerSurfaceHTTPRun        = "http-run"
	OwnerSurfaceAgentRunShell  = "agent-run-shell"
	OwnerSurfaceAgentRunOnNode = "agent-run-on-node"
	OwnerSurfaceAgentRunTask   = "agent-run-task"
)

var executionHeartbeatInterval = 15 * time.Second
var heartbeatTask = func(ledger *reservation.Ledger, ledgerExecID string) error {
	if ledger != nil && ledgerExecID != "" {
		return ledger.Heartbeat(ledgerExecID)
	}
	return nil
}
var localExecutionHostname = os.Hostname

var sharedSafetyEvaluator = safety.NewEvaluator(safety.DefaultRuleSet())

// ExecutionEventSink receives advisory execution-lifecycle events. Optional;
// nil disables emission.
type ExecutionEventSink interface {
	PlacementRequested(taskID string)
	PlacementDecided(taskID, node string, fitScore int, ok bool)
	ExecutionReserved(taskID, node string)
	StateChanged(taskID, node, trigger string, ok bool)
}

// ExecutionContextBuilder assembles the script/skill execution-context JSON.
// Optional; nil yields no context.
type ExecutionContextBuilder func(
	snap *models.ClusterSnapshot,
	st *state.ClusterState,
	decision models.PlacementDecision,
	taskDesc string,
	script any,
	skill any,
) ([]byte, error)

func (e *ValidationError) Error() string {
	return e.Message
}

func recordFailure(skillStore *skills.Store, description string, exitCode int) {
	if skillStore == nil {
		return
	}
	reason := fmt.Sprintf("failed with code %d", exitCode)
	skillStore.RecordFailure(description, reason)
	_ = skills.Update(func(s *skills.Store) error {
		s.RecordFailure(description, reason)
		return nil
	})
}

func notifyStateChange(ctx context.Context, req GuardedExecutionRequest, trigger string, resp GuardedExecutionResult) {
	if req.OnStateChange != nil {
		req.OnStateChange(ctx, trigger, resp)
	}

	if req.Events != nil {
		req.Events.StateChanged(req.Description, resp.Node, trigger, resp.OK)
	}
}

func applyFailureOutcome(st *state.ClusterState, resp GuardedExecutionResult, runErr error) {
	if st == nil || st.Failures == nil {
		return
	}
	class := models.FailureExecCrash
	if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, context.Canceled) {
		class = models.FailureTimeout
	}
	evidence := []string{fmt.Sprintf("exit code %d", resp.ExitCode)}
	scope := models.FailureScope{
		Node:     resp.Node,
		Workload: resp.Workload.Class,
		Tool:     resp.Tool,
	}
	st.Failures.Record(class, scope, runErr.Error(), evidence)
	// Also record a broader {Node, Workload} entry so placement filter/ranking
	// queries (which do not include Tool) can still match this failure.
	if resp.Tool != "" {
		broadScope := models.FailureScope{
			Node:     resp.Node,
			Workload: resp.Workload.Class,
		}
		st.Failures.Record(class, broadScope, runErr.Error(), evidence)
	}
}

func applySuccessOutcome(st *state.ClusterState, resp GuardedExecutionResult) {
	if st == nil || st.Failures == nil {
		return
	}
	scope := models.FailureScope{
		Node:     resp.Node,
		Workload: resp.Workload.Class,
		Tool:     resp.Tool,
	}
	st.Failures.RecordSuccess(scope)
	// Also clear the broader {Node, Workload} entry written by emitFailure.
	if resp.Tool != "" {
		broadScope := models.FailureScope{
			Node:     resp.Node,
			Workload: resp.Workload.Class,
		}
		st.Failures.RecordSuccess(broadScope)
	}
}

func durationMilliseconds(d time.Duration) int64 {
	if d <= 0 {
		return 1
	}
	if ms := d.Milliseconds(); ms > 0 {
		return ms
	}
	return 1
}

func recordExecutionOutcome(st *state.ClusterState, reqs models.TaskRequirements, resp GuardedExecutionResult, runErr error, elapsed time.Duration, peakRAMMB int64) {
	if st == nil {
		return
	}
	scope := placement.ObservationScopeForRequirements(resp.Node, reqs, resp.Tool)
	observation := models.ExecutionObservation{
		Scope:       scope,
		ObservedAt:  time.Now().UTC(),
		SampleCount: 1,
		LastSuccess: runErr == nil,
		WallTimeMS:  durationMilliseconds(elapsed),
		PeakRAMMB:   peakRAMMB,
		ModelName:   scope.ModelName,
	}

	rec := state.TaskExecutionRecord{
		ExecID:      resp.ExecID,
		Description: resp.Description,
		Command:     resp.Command,
		Node:        resp.Node,
		IsLocal:     resp.IsLocal,
		ExitCode:    resp.ExitCode,
		PeakRAMMB:   peakRAMMB,
		PeakVRAMMB:  resp.PeakVRAMMB,
		WallTimeMS:  durationMilliseconds(elapsed),
		Timestamp:   time.Now().UTC(),
	}
	if runErr != nil {
		rec.Error = runErr.Error()
	}
	apply := func(target *state.ClusterState) {
		target.RecordObservation(observation)
		if runErr != nil {
			applyFailureOutcome(target, resp, runErr)
		} else {
			applySuccessOutcome(target, resp)
		}
		target.RecordTaskExecution(rec)
	}

	apply(st)
	if err := state.Update(func(latest *state.ClusterState) error {
		apply(latest)
		return nil
	}); err != nil {
		slog.Warn("execution outcome persistence failed", "exec_id", resp.ExecID, "error", err)
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return 1
}

func ParseExposePorts(s string) (remote, local int, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, nil
	}
	parts := strings.Split(s, ":")
	if len(parts) == 1 {
		p, err := strconv.Atoi(parts[0])
		if err != nil || p < 1 || p > 65535 {
			return 0, 0, fmt.Errorf("invalid port: %q", s)
		}
		return p, 0, nil
	} else if len(parts) == 2 {
		r, err1 := strconv.Atoi(parts[0])
		l, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil || l < 0 || l > 65535 || r < 1 || r > 65535 {
			return 0, 0, fmt.Errorf("invalid ports: %q", s)
		}
		return r, l, nil
	}
	return 0, 0, fmt.Errorf("invalid port format: %q (expected remote:local or remote)", s)
}

var GetGitRepoState = git.GetRepoState

func handleDirtyWorkingTree(ctx context.Context, req GuardedExecutionRequest, stderr io.Writer) (bool, func(), error) {
	if stderr == nil {
		stderr = os.Stderr
	}

	gitState, err := GetGitRepoState(".")
	if err != nil || !gitState.IsRepo || !gitState.IsDirty {
		return true, func() {}, nil
	}

	fmt.Fprintf(stderr, "⚠️  WARNING: Working tree is dirty (%d files modified).\n", gitState.DirtyCount)

	if os.Getenv("AXIS_FORCE_DIRTY") == "true" {
		fmt.Fprintln(stderr, "[AXIS] AXIS_FORCE_DIRTY=true detected: proceeding with dirty tree.")
		return true, func() {}, nil
	}

	isTerm := IsTerminalFunc(req.Stdin)

	if !isTerm {
		fmt.Fprintln(stderr, "[AXIS] Non-interactive environment: proceeding with dirty tree.")
		return true, func() {}, nil
	}

	fmt.Fprintln(stderr, "[s] Stash changes and proceed")
	fmt.Fprintln(stderr, "[p] Proceed with dirty tree anyway")
	fmt.Fprintln(stderr, "[a] Abort execution (default)")
	fmt.Fprint(stderr, "Select action: ")

	var line []byte
	var temp [1]byte
	for {
		n, err := req.Stdin.Read(temp[:])
		if n > 0 {
			if temp[0] == '\n' {
				break
			}
			line = append(line, temp[0])
		}
		if err != nil {
			break
		}
	}
	choice := strings.ToLower(strings.TrimSpace(string(line)))
	if len(choice) > 0 {
		choice = choice[:1]
	}

	switch choice {
	case "s":
		fmt.Fprintln(stderr, "[AXIS] Stashing local changes...")
		stashCmd := exec.CommandContext(ctx, "git", "stash", "-u")
		var stashStderr bytes.Buffer
		stashCmd.Stderr = &stashStderr
		if err := stashCmd.Run(); err != nil {
			return false, func() {}, fmt.Errorf("git stash failed: %w (details: %s)", err, stashStderr.String())
		}

		cleanup := func() {
			fmt.Fprintln(stderr, "[AXIS] Restoring stashed changes...")
			// Use a background context so the stash is always restored, even
			// when the execution context was cancelled or timed out — otherwise
			// the operator's stashed uncommitted changes would be lost.
			popCmd := exec.CommandContext(context.Background(), "git", "stash", "pop")
			if err := popCmd.Run(); err != nil {
				fmt.Fprintf(stderr, "[AXIS] Warning: failed to restore stashed changes: %v\n", err)
			}
		}
		return true, cleanup, nil

	case "p":
		fmt.Fprintln(stderr, "[AXIS] Proceeding with dirty tree anyway.")
		return true, func() {}, nil

	default:
		fmt.Fprintln(stderr, "[AXIS] Execution aborted by operator.")
		return false, func() {}, fmt.Errorf("execution aborted: working tree is dirty")
	}
}

var IsTerminalFunc = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok || f == nil {
		return false
	}
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}
