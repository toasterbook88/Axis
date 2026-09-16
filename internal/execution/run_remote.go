package execution

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
	"github.com/toasterbook88/axis/internal/transport"

	"encoding/base64"
)

type RemoteExecutor interface {
	Run(context.Context, string) (string, error)
	Close() error
}

type StreamingRemoteExecutor interface {
	RemoteExecutor
	Stream(context.Context, string, io.Writer, io.Writer) error
}

type PortForwardingRemoteExecutor interface {
	ForwardLocal(ctx context.Context, localPort, remotePort int) (int, func(), error)
}

var NewRemoteExecutor = func(nc config.NodeConfig) RemoteExecutor {
	spec := nc.SSHDialSpec()
	return transport.NewSSHExecutorFromDial(spec.Host, spec.Port, spec.User, spec.DialTimeoutSec, spec.Fallbacks)
}

// RunLocalShell executes command in a login bash shell and returns its combined
// output, the peak RSS of the child process in MB (0 if unavailable), and any
// error. The second return value is populated from cmd.ProcessState.SysUsage()
// after the process exits, giving per-execution accuracy.
var RunLocalShell = func(ctx context.Context, command string, env []string) ([]byte, int64, error) {
	// Intentional ModeExec shell boundary: raw commands retain full shell
	// semantics under guarded execution after confirm=YES, safety.Check,
	// reservation heartbeats, and provenance tracking.
	// codeql[go/command-injection]
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return out, peakRAMFromProcessState(cmd.ProcessState), err
}

// StreamLocalShell executes command in a login bash shell, streaming output to
// the supplied writers. Returns the peak RSS in MB (0 if unavailable) and any
// error.
var StreamLocalShell = func(ctx context.Context, command string, env []string, stdout, stderr io.Writer) (int64, error) {
	// Intentional ModeExec shell boundary: raw commands retain full shell
	// semantics under guarded execution after confirm=YES, safety.Check,
	// reservation heartbeats, and provenance tracking.
	// codeql[go/command-injection]
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	return peakRAMFromProcessState(cmd.ProcessState), err
}

var PlanNixExecution = PlanNixWrapper
var ProbeLocalAvailableRAMMB = probeLocalAvailableRAMMB

func RunGuarded(ctx context.Context, rt *runtimectx.Context, req GuardedExecutionRequest) (GuardedExecutionResult, error) {
	prepared, err := PrepareGuardedExecution(ctx, rt, req)
	if err != nil || prepared.Result.Blocked {
		return prepared.Result, err
	}
	return RunPreparedExecution(ctx, prepared)
}

func runRemote(
	ctx context.Context,
	st *state.ClusterState,
	skillStore *skills.Store,
	owner state.ExecutionOwner,
	req GuardedExecutionRequest,
	reqs models.TaskRequirements,
	resp GuardedExecutionResult,
	targetConfig config.NodeConfig,
	resolvedDialTarget string,
	reservationMB int64,
	command string,
	extraEnv []string,
	contextJSON []byte,
	ledger *reservation.Ledger,
) (GuardedExecutionResult, error) {
	runtimeChanged := false
	defer func() {
		if runtimeChanged {
			notifyStateChange(ctx, req, StateChangeExecutionFinished, resp)
		}
	}()

	executor := NewRemoteExecutor(targetConfig)
	// Route over the discovered fast path (e.g. GbE/Thunderbolt) when available,
	// keeping targetConfig for SSH identity/host-key verification.
	if resolvedDialTarget != "" {
		if sshExec, ok := executor.(*transport.SSHExecutor); ok {
			sshExec.ResolvedDialTarget = resolvedDialTarget
		}
	}
	defer executor.Close()

	remoteContextPath := fmt.Sprintf("/tmp/axis-knows-%d.json", time.Now().UTC().UnixNano())
	// Deliver the context over SSH stdin when the payload (or its base64 form)
	// would push the command string past the remote kernel's MAX_ARG_STRLEN —
	// the limit applies to the entire command string ssh hands the remote shell
	// as one argv element, so chunking inside one command does not dodge it.
	if stdinExec, ok := executor.(transport.StdinRemoteExecutor); len(contextJSON) > transport.MaxContextInlineBytes && ok {
		writeJSONCmd := fmt.Sprintf("mkdir -p $(dirname %s) && base64 -d > %s",
			shellescape.Quote(remoteContextPath), shellescape.Quote(remoteContextPath))
		encoded := []byte(base64.StdEncoding.EncodeToString(contextJSON))
		if _, err := stdinExec.RunWithStdin(ctx, writeJSONCmd, encoded); err != nil {
			resp.Error = err.Error()
			resp.ExitCode = 1
			return resp, err
		}
	} else {
		writeJSONCmd := transport.BuildRemoteWriteCommand(remoteContextPath, contextJSON)
		if _, err := executor.Run(ctx, writeJSONCmd); err != nil {
			resp.Error = err.Error()
			resp.ExitCode = 1
			return resp, err
		}
	}

	execID := resp.ExecID
	if execID == "" {
		execID = generateExecID(resp.Node)
		resp.ExecID = execID
	}

	logFile := openTaskLog(execID)
	if logFile != nil {
		defer logFile.Close()
	}

	stdoutWriter := req.Stdout
	stderrWriter := req.Stderr
	if logFile != nil {
		if stdoutWriter == nil {
			stdoutWriter = logFile
		} else {
			stdoutWriter = io.MultiWriter(stdoutWriter, logFile)
		}
		if stderrWriter == nil {
			stderrWriter = logFile
		} else {
			stderrWriter = io.MultiWriter(stderrWriter, logFile)
		}
	}

	if req.ExposePorts != "" {
		remote, local, err := ParseExposePorts(req.ExposePorts)
		if err != nil {
			resp.Error = err.Error()
			resp.ExitCode = 1
			return resp, err
		}
		forwarder, ok := executor.(PortForwardingRemoteExecutor)
		if !ok {
			err := fmt.Errorf("executor does not support port forwarding")
			resp.Error = err.Error()
			resp.ExitCode = 1
			return resp, err
		}
		boundPort, stopForward, err := forwarder.ForwardLocal(ctx, local, remote)
		if err != nil {
			resp.Error = err.Error()
			resp.ExitCode = 1
			return resp, err
		}
		defer stopForward()
		if stderrWriter != nil {
			fmt.Fprintf(stderrWriter, "[AXIS] Ephemeral SSH port forwarding active: 127.0.0.1:%d -> remote:%d\n", boundPort, remote)
		}
	}

	if ledger != nil {
		entry := reservation.Entry{
			ID:           execID,
			Node:         resp.Node,
			OwnerExecID:  execID,
			OwnerSurface: owner.Surface,
			OwnerPID:     os.Getpid(),
			OwnerOrigin:  owner.Origin,
			RAMMB:        reservationMB,
			Description:  req.Description,
			ExpiresAt:    time.Now().UTC().Add(5 * time.Minute),
		}
		if _, acquireErr := ledger.Reserve(entry); acquireErr != nil {
			resp.Error = acquireErr.Error()
			return resp, acquireErr
		}
		runtimeChanged = true
		notifyStateChange(ctx, req, StateChangeExecutionReserved, resp)
		defer func() {
			if releaseErr := ledger.Release(execID); releaseErr != nil && resp.Error == "" {
				resp.Error = releaseErr.Error()
			}
		}()
	}

	envVars := []string{
		"AXIS_EXECUTION_MODE=" + req.Mode,
		"AXIS_CONFIRM=" + req.Confirm,
		fmt.Sprintf("AXIS_RESERVATION_MB=%d", reservationMB),
	}
	if resp.ExecID != "" {
		envVars = append(envVars, "AXIS_EXECUTION_PARENT_ID="+resp.ExecID)
	}
	env := append(envVars, extraEnv...)

	runCmd := fmt.Sprintf(
		"%s _axis_ctx=%s; trap 'rm -f \"$_axis_ctx\"' EXIT; bash -lc %s",
		RemoteExecPrefix(resp.Node, remoteContextPath, env),
		shellescape.Quote(remoteContextPath),
		shellescape.Quote(command),
	)

	startedAt := time.Now().UTC()
	out, _, runErr := runWithReservationHeartbeat(ledger, execID, func() (string, int64, error) {
		out, err := runRemoteWithOutput(ctx, executor, runCmd, stdoutWriter, stderrWriter)
		return out, 0, err
	})
	elapsed := time.Since(startedAt)
	resp.Output = out
	resp.ExitCode = exitCode(runErr)
	if runErr != nil {
		resp.Error = runErr.Error()
		recordFailure(skillStore, req.Description, resp.ExitCode)
		runtimeChanged = true
		recordExecutionOutcome(st, reqs, resp, runErr, elapsed, 0)
		resp.WallTimeMS = durationMilliseconds(elapsed)
		return resp, runErr
	}

	recordSuccess(skillStore, req.Description, command, resp.Node)
	runtimeChanged = true
	recordExecutionOutcome(st, reqs, resp, nil, elapsed, 0)
	resp.OK = true
	resp.WallTimeMS = durationMilliseconds(elapsed)
	return resp, nil
}

func runRemoteWithOutput(ctx context.Context, executor RemoteExecutor, command string, stdout, stderr io.Writer) (string, error) {
	if stdout == nil && stderr == nil {
		return executor.Run(ctx, command)
	}

	streamer, ok := executor.(StreamingRemoteExecutor)
	if !ok {
		out, err := executor.Run(ctx, command)
		if stdout != nil && out != "" {
			_, _ = io.WriteString(stdout, out)
		}
		return out, err
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	outWriter := io.Writer(&stdoutBuf)
	if stdout != nil {
		outWriter = io.MultiWriter(stdout, &stdoutBuf)
	}

	errWriter := io.Writer(&stderrBuf)
	if stderr != nil {
		errWriter = io.MultiWriter(stderr, &stderrBuf)
	}

	err := streamer.Stream(ctx, command, outWriter, errWriter)
	return combinedOutput(stdoutBuf.String(), stderrBuf.String()), err
}

func runWithReservationHeartbeat(
	ledger *reservation.Ledger,
	ledgerExecID string,
	run func() (string, int64, error),
) (string, int64, error) {
	if ledger == nil || ledgerExecID == "" {
		return run()
	}
	type result struct {
		out  string
		peak int64
		err  error
	}

	done := make(chan result, 1)
	go func() {
		out, peak, err := run()
		done <- result{out: out, peak: peak, err: err}
	}()

	ticker := time.NewTicker(executionHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case res := <-done:
			return res.out, res.peak, res.err
		case <-ticker.C:
			_ = heartbeatTask(ledger, ledgerExecID)
		}
	}
}

func RemoteExecPrefix(node, contextPath string, extraEnv []string) string {
	parts := []string{
		"export",
		"BEST_NODE=" + shellescape.Quote(node),
		"AXIS_CONTEXT_FILE=" + shellescape.Quote(contextPath),
	}
	for _, kv := range extraEnv {
		if strings.TrimSpace(kv) == "" {
			continue
		}
		if idx := strings.Index(kv, "="); idx > 0 {
			parts = append(parts, kv[:idx]+"="+shellescape.Quote(kv[idx+1:]))
		}
	}
	return strings.Join(parts, " ") + ";"
}

func findNodeFacts(snap *models.ClusterSnapshot, name string) (models.NodeFacts, bool) {
	if snap == nil {
		return models.NodeFacts{}, false
	}
	for _, n := range snap.Nodes {
		if strings.EqualFold(n.Name, name) {
			return n, true
		}
	}
	return models.NodeFacts{}, false
}

func recordSuccess(skillStore *skills.Store, description, command, node string) {
	if skillStore == nil {
		return
	}
	// Mutate the in-memory store so callers holding it see the update, and
	// apply the same mutation to the locked on-disk copy. skills.Update
	// reloads from disk inside the lock, so a stale or empty in-memory store
	// can no longer truncate the operator's file.
	skillStore.RecordSuccess(description, command, node)
	_ = skills.Update(func(s *skills.Store) error {
		s.RecordSuccess(description, command, node)
		return nil
	})
}
