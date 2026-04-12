package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/safety"
	"github.com/toasterbook88/axis/internal/scripts"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
	"github.com/toasterbook88/axis/internal/transport"
	"github.com/toasterbook88/axis/internal/turboexec"
)

const (
	ModeScript = "script"
	ModeExec   = "exec"

	ConfirmWord = "YES"

	StateChangeExecutionReserved = "execution-reserved"
	StateChangeExecutionFinished = "execution-finished"

	OwnerSurfaceGuardedExec   = "guarded-exec"
	OwnerSurfaceTaskRun       = "task-run"
	OwnerSurfaceHTTPRun       = "http-run"
	OwnerSurfaceAgentRunShell = "agent-run-shell"
)

var executionHeartbeatInterval = 15 * time.Second
var heartbeatTask = func(st *state.ClusterState, node, execID string) error {
	return st.HeartbeatTask(node, execID)
}
var localExecutionHostname = os.Hostname

type GuardedExecutionRequest struct {
	Description    string                                                `json:"description"`
	Mode           string                                                `json:"mode,omitempty"`
	Confirm        string                                                `json:"confirm,omitempty"`
	OwnerSurface   string                                                `json:"-"`
	OwnerLabel     string                                                `json:"-"`
	OriginOverride models.ExecutionOrigin                                `json:"-"`
	Stdout         io.Writer                                             `json:"-"`
	Stderr         io.Writer                                             `json:"-"`
	OnReady        func(GuardedExecutionResult)                          `json:"-"`
	OnStateChange  func(context.Context, string, GuardedExecutionResult) `json:"-"`
}

type GuardedExecutionResult struct {
	OK             bool                        `json:"ok"`
	Description    string                      `json:"description"`
	Mode           string                      `json:"mode,omitempty"`
	Intent         string                      `json:"intent,omitempty"`
	Command        string                      `json:"command,omitempty"`
	Node           string                      `json:"node,omitempty"`
	Tool           string                      `json:"tool,omitempty"`
	Workload       models.WorkloadProfileMatch `json:"workload,omitempty"`
	FitScore       int                         `json:"fit_score,omitempty"`
	IsLocal        bool                        `json:"is_local,omitempty"`
	Reasoning      []string                    `json:"reasoning,omitempty"`
	Blocked        bool                        `json:"blocked,omitempty"`
	BlockReason    string                      `json:"block_reason,omitempty"`
	DumbScore      int                         `json:"dumb_score,omitempty"`
	Output         string                      `json:"output,omitempty"`
	Error          string                      `json:"error,omitempty"`
	ExitCode       int                         `json:"exit_code,omitempty"`
	SnapshotStatus models.SnapshotStatus       `json:"snapshot_status,omitempty"`
	Summary        *models.ClusterSummary      `json:"summary,omitempty"`
}

type Intent struct {
	Command       string
	Label         string
	MatchedScript *scripts.Script
	MatchedSkill  *skills.LearnedSkill
}

type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

type RemoteExecutor interface {
	Run(context.Context, string) (string, error)
	Close() error
}

type StreamingRemoteExecutor interface {
	RemoteExecutor
	Stream(context.Context, string, io.Writer, io.Writer) error
}

var NewRemoteExecutor = func(nc config.NodeConfig) RemoteExecutor {
	return transport.NewSSHExecutor(nc.Hostname, nc.EffectiveSSHPort(), nc.SSHUser, nc.EffectiveTimeout())
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


func NormalizeRequest(req *GuardedExecutionRequest) {
	if req == nil {
		return
	}
	req.Description = strings.TrimSpace(req.Description)
	req.Mode = strings.ToLower(strings.TrimSpace(req.Mode))
	req.Confirm = strings.TrimSpace(req.Confirm)
	req.OwnerSurface = strings.TrimSpace(req.OwnerSurface)
	req.OwnerLabel = strings.TrimSpace(req.OwnerLabel)
	req.OriginOverride = req.OriginOverride.Normalized()
}

func ValidateRequest(req GuardedExecutionRequest) error {
	switch {
	case req.Description == "":
		return &ValidationError{Message: "description is required"}
	case req.Mode == "":
		return &ValidationError{Message: "mode is required (use script or exec)"}
	case req.Mode != ModeScript && req.Mode != ModeExec:
		return &ValidationError{Message: "mode must be script or exec"}
	case req.Confirm != ConfirmWord:
		return &ValidationError{Message: "confirm must be YES to authorize execution"}
	default:
		return nil
	}
}

func executionOwner(rt *runtimectx.Context, req GuardedExecutionRequest) state.ExecutionOwner {
	origin := req.OriginOverride.Normalized()
	if origin.IsZero() {
		origin = resolveExecutionOrigin(rt)
	}
	owner := state.ExecutionOwner{
		Surface: strings.TrimSpace(req.OwnerSurface),
		Label:   strings.TrimSpace(req.OwnerLabel),
		Origin:  origin,
	}
	if owner.Surface == "" {
		owner.Surface = OwnerSurfaceGuardedExec
	}
	return owner
}

// LocalExecutionOrigin derives the trusted local AXIS execution origin from
// the current runtime context.
func LocalExecutionOrigin(rt *runtimectx.Context) models.ExecutionOrigin {
	return resolveExecutionOrigin(rt)
}

func resolveExecutionOrigin(rt *runtimectx.Context) models.ExecutionOrigin {
	if rt != nil && rt.Snapshot != nil {
		if localNode, ok := models.FindLocalNode(rt.Snapshot.Nodes); ok {
			return models.ExecutionOriginFromNode(localNode)
		}
	}
	if rt != nil && rt.Config != nil {
		for _, nc := range rt.Config.Nodes {
			if models.IsLocalConfig(nc.Name, nc.Hostname, nc.StableID) {
				return models.NewExecutionOrigin(nc.Name, nc.Hostname, nc.StableID)
			}
		}
	}
	hostname, _ := localExecutionHostname()
	return models.NewExecutionOrigin("", hostname, models.CurrentLocalStableID())
}

func ResolveIntent(description, mode string, skillStore *skills.Store) (Intent, error) {
	var intent Intent
	if skillStore != nil {
		if skill, ok := skillStore.BestMatch(description); ok {
			skillCopy := skill
			intent.MatchedSkill = &skillCopy
		}
	}
	if script, ok := scripts.GetBestScript(description); ok {
		scriptCopy := script
		intent.MatchedScript = &scriptCopy
	}

	switch mode {
	case ModeScript:
		if intent.MatchedScript != nil {
			intent.Command = intent.MatchedScript.Command
			intent.Label = fmt.Sprintf("fallback script %q", intent.MatchedScript.Name)
			return intent, nil
		}
		if intent.MatchedSkill != nil {
			intent.Command = intent.MatchedSkill.Command
			intent.Label = fmt.Sprintf("learned skill %q", intent.MatchedSkill.ID)
			return intent, nil
		}
		return Intent{}, fmt.Errorf("no known script or learned skill matches %q", description)
	case ModeExec:
		intent.Command = description
		intent.Label = "raw command"
		return intent, nil
	default:
		return Intent{}, fmt.Errorf("unsupported mode %q", mode)
	}
}

func ReservationMBForRequirements(reqs models.TaskRequirements) int64 {
	return reqs.MinFreeRAMMB + 1024
}

func CanReserve(snap *models.ClusterSnapshot, st *state.ClusterState, node string, mb int64) bool {
	if mb <= 0 || snap == nil || st == nil {
		return true
	}

	var totalRAM, freeRAM int64
	for _, n := range snap.Nodes {
		if n.Name == node && n.Resources != nil {
			totalRAM = n.Resources.RAMTotalMB
			freeRAM = n.Resources.RAMFreeMB
			break
		}
	}
	if totalRAM <= 0 && freeRAM <= 0 {
		return true
	}

	capMB := models.ReservableRAMMB(totalRAM, freeRAM)

	ns, ok := st.Nodes[node]
	if !ok {
		return mb <= capMB
	}
	return ns.ReservedMB+mb <= capMB
}

func RunGuarded(ctx context.Context, rt *runtimectx.Context, req GuardedExecutionRequest) (GuardedExecutionResult, error) {
	// CRITICAL INVARIANT: operator-requested task execution in this package's
	// guarded execution path must go through RunGuarded rather than bypassing it.
	NormalizeRequest(&req)

	resp := GuardedExecutionResult{
		Description: req.Description,
		Mode:        req.Mode,
	}

	if err := ValidateRequest(req); err != nil {
		resp.Error = err.Error()
		return resp, err
	}
	if rt == nil || rt.Config == nil || rt.Snapshot == nil {
		resp.Error = "runtime context unavailable"
		return resp, fmt.Errorf("runtime context unavailable")
	}

	resp.SnapshotStatus = rt.Snapshot.Status
	resp.Summary = &rt.Snapshot.Summary

	skillStore := rt.Skills
	if skillStore == nil {
		skillStore = &skills.Store{}
	}

	intent, err := ResolveIntent(req.Description, req.Mode, skillStore)
	if err != nil {
		resp.Error = err.Error()
		return resp, err
	}

	resp.Intent = intent.Label
	resp.Command = intent.Command

	reqs := prepareRequirements(req.Description, req.Mode, intent)
	decision := placement.SelectBestNode(reqs, rt.Snapshot.Nodes, rt.State)
	resp.Reasoning = runtimectx.PrependWarningReasoning(decision.Reasoning, rt.Snapshot.Warnings)
	resp.Node = decision.Node
	resp.Tool = decision.Tool
	resp.Workload = decision.Workload
	resp.FitScore = decision.FitScore
	resp.IsLocal = decision.IsLocal

	if !decision.OK {
		resp.Error = "no suitable node found"
		return resp, fmt.Errorf("no suitable node found")
	}

	reservationMB := ReservationMBForRequirements(reqs)
	if !CanReserve(rt.Snapshot, rt.State, decision.Node, reservationMB) {
		resp.Error = fmt.Sprintf("node %s cannot reserve %d MB (current reservations exceed cap)", decision.Node, reservationMB)
		return resp, fmt.Errorf("reservation cap exceeded")
	}
	resp.Reasoning = append(resp.Reasoning, fmt.Sprintf("reservation headroom protected: %dMB", reservationMB))

	targetNode, ok := findNodeFacts(rt.Snapshot, decision.Node)
	if !ok {
		resp.Error = fmt.Sprintf("node %q not found in snapshot", decision.Node)
		return resp, fmt.Errorf("node %q not found in snapshot", decision.Node)
	}

	if err := enforceLocalExecutionSafety(ctx, targetNode, reqs, &resp); err != nil {
		resp.Error = err.Error()
		return resp, err
	}

	turboPlan := turboexec.Prepare(targetNode, reqs, intent.Command)
	resp.Reasoning = append(resp.Reasoning, turboPlan.Notes...)
	commandToRun := turboPlan.Command

	k := knowledge.Build(rt.Snapshot, rt.State, decision.Node)
	if block := safety.Check(k, commandToRun, skillStore.IsKnownBad); block.Blocked {
		resp.Blocked = true
		resp.BlockReason = block.Reason
		resp.DumbScore = block.Score
		resp.Error = block.Reason
		return resp, nil
	}

	contextJSON, err := knowledge.ExecutionContextJSON(rt.Snapshot, rt.State, decision, req.Description, intent.MatchedScript, intent.MatchedSkill)
	if err != nil {
		resp.Error = err.Error()
		return resp, err
	}

	nixPlan := PlanNixExecution(targetNode, reqs, commandToRun)
	resp.Command = commandToRun
	if nixPlan.Enabled {
		commandToRun = nixPlan.Command
		resp.Command = commandToRun
		resp.Reasoning = append(resp.Reasoning, nixPlan.Notes...)
	}

	if req.OnReady != nil {
		req.OnReady(resp)
	}

	if models.IsLocalNode(targetNode) {
		return runLocal(ctx, rt.State, skillStore, executionOwner(rt, req), req, reqs, resp, decision, reservationMB, commandToRun, append(turboPlan.Env, nixPlan.Env...), contextJSON)
	}

	targetConfig, ok := rt.Config.FindNode(decision.Node)
	if !ok {
		resp.Error = fmt.Sprintf("node %q not found in config", decision.Node)
		return resp, fmt.Errorf("node %q not found in config", decision.Node)
	}

	return runRemote(ctx, rt.State, skillStore, executionOwner(rt, req), req, reqs, resp, targetConfig, decision, reservationMB, commandToRun, append(turboPlan.Env, nixPlan.Env...), contextJSON)
}

func prepareRequirements(description, mode string, intent Intent) models.TaskRequirements {
	reqs := placement.InferRequirements(description)

	if mode == ModeScript && intent.MatchedScript != nil {
		reqs.RequiredTools = append([]string(nil), intent.MatchedScript.RequiredTools...)
		if intent.MatchedScript.EstRAMMB > reqs.MinFreeRAMMB {
			reqs.MinFreeRAMMB = intent.MatchedScript.EstRAMMB
		}
	}

	return reqs
}

func enforceLocalExecutionSafety(ctx context.Context, node models.NodeFacts, reqs models.TaskRequirements, resp *GuardedExecutionResult) error {
	if !models.IsLocalNode(node) || !isInferenceExecution(reqs) {
		return nil
	}

	minNeeded := placement.MinFreeRAMForNode(reqs, node)
	if node.Resources != nil && node.Resources.RAMTotalMB > 0 && node.Resources.RAMTotalMB <= 8192 && minNeeded >= 4096 {
		reason := fmt.Sprintf("local model execution disabled on constrained %dMB host; run this on a remote node or use a smaller explicitly bounded model", node.Resources.RAMTotalMB)
		resp.Reasoning = append(resp.Reasoning, reason)
		return fmt.Errorf("%s", reason)
	}

	liveFree, err := ProbeLocalAvailableRAMMB(ctx)
	if err != nil {
		reason := fmt.Sprintf("live local memory preflight unavailable; refusing local inference execution: %v", err)
		resp.Reasoning = append(resp.Reasoning, reason)
		return fmt.Errorf("%s", reason)
	}

	resp.Reasoning = append(resp.Reasoning, fmt.Sprintf("live local memory preflight: %dMB available", liveFree))
	if minNeeded > 0 && liveFree < minNeeded {
		reason := fmt.Sprintf("live local memory preflight failed: need %dMB free, have %dMB", minNeeded, liveFree)
		resp.Reasoning = append(resp.Reasoning, reason)
		return fmt.Errorf("%s", reason)
	}

	return nil
}

func isInferenceExecution(reqs models.TaskRequirements) bool {
	if reqs.ContextWindowTokens > 0 || reqs.PrefersTurboQuant {
		return true
	}
	if reqs.MinFreeRAMMB >= 4096 {
		return true
	}
	for _, tool := range reqs.RequiredTools {
		switch strings.ToLower(tool) {
		case "ollama", "llama-server", "apple-foundation-models":
			return true
		}
	}
	for _, backend := range reqs.PreferredBackends {
		switch strings.ToLower(backend) {
		case "llama.cpp", "mlx", "apple-foundation-models":
			return true
		}
	}
	return false
}

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
	decision models.PlacementDecision,
	reservationMB int64,
	command string,
	extraEnv []string,
	contextJSON []byte,
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

	env := append(os.Environ(),
		"AXIS_CONTEXT_FILE="+contextFile.Name(),
		"BEST_NODE="+decision.Node,
		"AXIS_EXECUTION_MODE="+req.Mode,
		"AXIS_CONFIRM="+req.Confirm,
		fmt.Sprintf("AXIS_RESERVATION_MB=%d", reservationMB),
	)
	env = append(env, extraEnv...)

	var execID string
	if st != nil {
		acquireID, acquireErr := st.AcquireTaskWithOwner(decision.Node, req.Description, reservationMB, owner)
		if acquireErr != nil {
			resp.Error = acquireErr.Error()
			return resp, acquireErr
		}
		execID = acquireID
		runtimeChanged = true
		notifyStateChange(ctx, req, StateChangeExecutionReserved, resp)
		defer func() {
			if releaseErr := st.ReleaseTask(decision.Node, execID, reservationMB); releaseErr != nil && resp.Error == "" {
				resp.Error = releaseErr.Error()
			}
		}()
	}

	startedAt := time.Now().UTC()
	out, peakRAMMB, runErr := runWithReservationHeartbeat(st, decision.Node, execID, func() (string, int64, error) {
		return runLocalWithOutput(ctx, command, env, req.Stdout, req.Stderr)
	})
	elapsed := time.Since(startedAt)
	resp.Output = out
	resp.ExitCode = exitCode(runErr)
	if runErr != nil {
		resp.Error = runErr.Error()
		recordFailure(skillStore, req.Description, resp.ExitCode)
		runtimeChanged = true
		recordExecutionOutcome(st, reqs, resp, runErr, elapsed, peakRAMMB)
		return resp, runErr
	}

	recordSuccess(skillStore, req.Description, command, decision.Node)
	runtimeChanged = true
	recordExecutionOutcome(st, reqs, resp, nil, elapsed, peakRAMMB)
	resp.OK = true
	return resp, nil
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
	decision models.PlacementDecision,
	reservationMB int64,
	command string,
	extraEnv []string,
	contextJSON []byte,
) (GuardedExecutionResult, error) {
	runtimeChanged := false
	defer func() {
		if runtimeChanged {
			notifyStateChange(ctx, req, StateChangeExecutionFinished, resp)
		}
	}()

	executor := NewRemoteExecutor(targetConfig)
	defer executor.Close()

	remoteContextPath := fmt.Sprintf("/tmp/axis-knows-%d.json", time.Now().UTC().UnixNano())
	writeJSONCmd := fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF\n", shellescape.Quote(remoteContextPath), string(contextJSON))
	if _, err := executor.Run(ctx, writeJSONCmd); err != nil {
		resp.Error = err.Error()
		resp.ExitCode = 1
		return resp, err
	}

	var execID string
	if st != nil {
		acquireID, acquireErr := st.AcquireTaskWithOwner(decision.Node, req.Description, reservationMB, owner)
		if acquireErr != nil {
			resp.Error = acquireErr.Error()
			return resp, acquireErr
		}
		execID = acquireID
		runtimeChanged = true
		notifyStateChange(ctx, req, StateChangeExecutionReserved, resp)
		defer func() {
			if releaseErr := st.ReleaseTask(decision.Node, execID, reservationMB); releaseErr != nil && resp.Error == "" {
				resp.Error = releaseErr.Error()
			}
		}()
	}

	env := append([]string{
		"AXIS_EXECUTION_MODE=" + req.Mode,
		"AXIS_CONFIRM=" + req.Confirm,
		fmt.Sprintf("AXIS_RESERVATION_MB=%d", reservationMB),
	}, extraEnv...)

	runCmd := fmt.Sprintf(
		"%s trap 'rm -f %s' EXIT; bash -lc %s",
		RemoteExecPrefix(decision.Node, remoteContextPath, env),
		shellescape.Quote(remoteContextPath),
		shellescape.Quote(command),
	)

	startedAt := time.Now().UTC()
	out, _, runErr := runWithReservationHeartbeat(st, decision.Node, execID, func() (string, int64, error) {
		out, err := runRemoteWithOutput(ctx, executor, runCmd, req.Stdout, req.Stderr)
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
		return resp, runErr
	}

	recordSuccess(skillStore, req.Description, command, decision.Node)
	runtimeChanged = true
	recordExecutionOutcome(st, reqs, resp, nil, elapsed, 0)
	resp.OK = true
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

func runWithReservationHeartbeat(
	st *state.ClusterState,
	node, execID string,
	run func() (string, int64, error),
) (string, int64, error) {
	if st == nil || execID == "" {
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
			_ = heartbeatTask(st, node, execID)
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
	skillStore.RecordSuccess(description, command, node)
	_ = skillStore.Save()
}

func recordFailure(skillStore *skills.Store, description string, exitCode int) {
	if skillStore == nil {
		return
	}
	skillStore.RecordFailure(description, fmt.Sprintf("failed with code %d", exitCode))
	_ = skillStore.Save()
}

func notifyStateChange(ctx context.Context, req GuardedExecutionRequest, trigger string, resp GuardedExecutionResult) {
	if req.OnStateChange != nil {
		req.OnStateChange(ctx, trigger, resp)
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
	st.RecordObservation(models.ExecutionObservation{
		Scope:       scope,
		ObservedAt:  time.Now().UTC(),
		SampleCount: 1,
		LastSuccess: runErr == nil,
		WallTimeMS:  durationMilliseconds(elapsed),
		PeakRAMMB:   peakRAMMB,
		ModelName:   scope.ModelName,
	})
	if runErr != nil {
		applyFailureOutcome(st, resp, runErr)
	} else {
		applySuccessOutcome(st, resp)
	}
	_ = st.Save()
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
