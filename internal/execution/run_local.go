package execution

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

func probeLocalAvailableRAMMB(ctx context.Context) (int64, error) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.CommandContext(ctx, "vm_stat").Output()
		if err != nil {
			return 0, fmt.Errorf("vm_stat: %w", err)
		}
		return parseDarwinAvailableRAMMB(string(out))
	case "linux":
		data, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0, fmt.Errorf("read /proc/meminfo: %w", err)
		}
		return parseLinuxAvailableRAMMB(string(data))
	default:
		return 0, fmt.Errorf("unsupported local RAM probe OS %q", runtime.GOOS)
	}
}

func parseDarwinAvailableRAMMB(out string) (int64, error) {
	var (
		pageSize int64 = 4096
		pages    int64
	)

	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.Contains(trimmed, "page size of ") && strings.Contains(trimmed, " bytes"):
			start := strings.Index(trimmed, "page size of ")
			end := strings.Index(trimmed, " bytes")
			if start >= 0 && end > start {
				value := trimmed[start+len("page size of ") : end]
				parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
				if err == nil && parsed > 0 {
					pageSize = parsed
				}
			}
		case strings.HasPrefix(trimmed, "Pages free:"),
			strings.HasPrefix(trimmed, "Pages inactive:"),
			strings.HasPrefix(trimmed, "Pages speculative:"):
			count, err := parseVMStatPageCount(trimmed)
			if err != nil {
				return 0, err
			}
			pages += count
		}
	}

	if pages <= 0 {
		return 0, fmt.Errorf("vm_stat did not report reclaimable pages")
	}
	return pages * pageSize / (1024 * 1024), nil
}

func parseVMStatPageCount(line string) (int64, error) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid vm_stat line %q", line)
	}
	value := strings.TrimSpace(parts[1])
	value = strings.TrimSuffix(value, ".")
	value = strings.ReplaceAll(value, ".", "")
	value = strings.ReplaceAll(value, ",", "")
	count, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse vm_stat count %q: %w", line, err)
	}
	return count, nil
}

func parseLinuxAvailableRAMMB(out string) (int64, error) {
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("invalid MemAvailable line %q", line)
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse MemAvailable: %w", err)
		}
		return kb / 1024, nil
	}
	return 0, fmt.Errorf("MemAvailable not found")
}

func runLocal(
	ctx context.Context,
	st *state.ClusterState,
	skillStore *skills.Store,
	owner state.ExecutionOwner,
	req GuardedExecutionRequest,
	reqs models.TaskRequirements,
	resp GuardedExecutionResult,
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

	contextFile, err := os.CreateTemp("", "axis-knows-*.json")
	if err != nil {
		resp.Error = err.Error()
		return resp, err
	}
	defer os.Remove(contextFile.Name())

	if _, err := contextFile.Write(contextJSON); err != nil {
		_ = contextFile.Close()
		resp.Error = err.Error()
		return resp, err
	}
	if err := contextFile.Close(); err != nil {
		resp.Error = err.Error()
		return resp, err
	}

	envVars := []string{
		"AXIS_CONTEXT_FILE=" + contextFile.Name(),
		"BEST_NODE=" + resp.Node,
		"AXIS_EXECUTION_MODE=" + req.Mode,
		"AXIS_CONFIRM=" + req.Confirm,
		fmt.Sprintf("AXIS_RESERVATION_MB=%d", reservationMB),
	}
	if resp.ExecID != "" {
		envVars = append(envVars, "AXIS_EXECUTION_PARENT_ID="+resp.ExecID)
	}
	env := append(os.Environ(), envVars...)
	env = append(env, extraEnv...)

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

	if req.ExposePorts != "" && stderrWriter != nil {
		fmt.Fprintf(stderrWriter, "[AXIS] Warning: Expose ports %q ignored for local execution.\n", req.ExposePorts)
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

	startedAt := time.Now().UTC()
	vramStop := samplePeakVRAM(ctx, 500*time.Millisecond)
	out, peakRAMMB, runErr := runWithReservationHeartbeat(ledger, execID, func() (string, int64, error) {
		return runLocalWithOutput(ctx, command, env, stdoutWriter, stderrWriter)
	})
	peakVRAMMB := vramStop()
	elapsed := time.Since(startedAt)
	resp.Output = out
	resp.ExitCode = exitCode(runErr)
	if runErr != nil {
		resp.Error = runErr.Error()
		recordFailure(skillStore, req.Description, resp.ExitCode)
		runtimeChanged = true
		recordExecutionOutcome(st, reqs, resp, runErr, elapsed, peakRAMMB)
		resp.PeakRAMMB = peakRAMMB
		resp.PeakVRAMMB = peakVRAMMB
		resp.WallTimeMS = durationMilliseconds(elapsed)
		return resp, runErr
	}

	recordSuccess(skillStore, req.Description, command, resp.Node)
	runtimeChanged = true
	recordExecutionOutcome(st, reqs, resp, nil, elapsed, peakRAMMB)
	resp.OK = true
	resp.PeakRAMMB = peakRAMMB
	resp.PeakVRAMMB = peakVRAMMB
	resp.WallTimeMS = durationMilliseconds(elapsed)
	return resp, nil
}

func runLocalWithOutput(ctx context.Context, command string, env []string, stdout, stderr io.Writer) (string, int64, error) {
	if stdout == nil && stderr == nil {
		out, peak, err := RunLocalShell(ctx, command, env)
		return string(out), peak, err
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

	peak, err := StreamLocalShell(ctx, command, env, outWriter, errWriter)
	return combinedOutput(stdoutBuf.String(), stderrBuf.String()), peak, err
}

func combinedOutput(stdout, stderr string) string {
	stdout = strings.TrimSuffix(stdout, "\n")
	stderr = strings.TrimSuffix(stderr, "\n")

	switch {
	case stdout == "":
		return stderr
	case stderr == "":
		return stdout
	default:
		return stdout + "\n" + stderr
	}
}
