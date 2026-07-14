package execution

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/events"
	"github.com/toasterbook88/axis/internal/git"
	"github.com/toasterbook88/axis/internal/knowledge"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/persist"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/reservation"
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
	OwnerSurfaceAgentRunTask  = "agent-run-task"
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

func generateExecID(node string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Extreme rare fallback
		return fmt.Sprintf("%d-%s", time.Now().UnixNano(), node)
	}
	return fmt.Sprintf("%s-%s", hex.EncodeToString(b), node)
}

type GuardedExecutionRequest struct {
	Description     string                                                `json:"description"`
	Mode            string                                                `json:"mode,omitempty"`
	Confirm         string                                                `json:"confirm,omitempty"`
	ExposePorts     string                                                `json:"expose_ports,omitempty"`
	MemoryRequestMB int64                                                 `json:"memory_request_mb,omitempty"`
	MemoryMaxMB     int64                                                 `json:"memory_max_mb,omitempty"`
	RequestedNode   string                                                `json:"requested_node,omitempty"`
	OwnerSurface    string                                                `json:"-"`
	OwnerLabel      string                                                `json:"-"`
	OriginOverride  models.ExecutionOrigin                                `json:"-"`
	Stdout          io.Writer                                             `json:"-"`
	Stderr          io.Writer                                             `json:"-"`
	Stdin           io.Reader                                             `json:"-"`
	OnReady         func(GuardedExecutionResult)                          `json:"-"`
	OnStateChange   func(context.Context, string, GuardedExecutionResult) `json:"-"`
}

type GuardedExecutionResult struct {
	ExecID         string                      `json:"exec_id,omitempty"`
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
	PeakRAMMB      int64                       `json:"peak_ram_mb,omitempty"`
	PeakVRAMMB     int64                       `json:"peak_vram_mb,omitempty"`
	WallTimeMS     int64                       `json:"wall_time_ms,omitempty"`
}

type PreparedExecution struct {
	Request       GuardedExecutionRequest
	Intent        Intent
	Requirements  models.TaskRequirements
	Result        GuardedExecutionResult
	ReservationMB int64
	Command       string
	ExtraEnv      []string
	ContextJSON   []byte
	TargetNode    models.NodeFacts
	TargetConfig  config.NodeConfig
	Owner         state.ExecutionOwner
	SkillStore    *skills.Store
	State         *state.ClusterState
	Ledger        *reservation.Ledger
	Err           error
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

type PortForwardingRemoteExecutor interface {
	ForwardLocal(ctx context.Context, localPort, remotePort int) (int, func(), error)
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
	req.RequestedNode = strings.TrimSpace(req.RequestedNode)
	req.OriginOverride = req.OriginOverride.Normalized()
	if req.Stdin == nil {
		req.Stdin = os.Stdin
	}
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
	reqMem := reqs.GetMemoryRequestMB()
	if reqMem > 0 {
		return reqMem
	}
	return reqs.MinFreeRAMMB + 1024
}

func CanReserve(snap *models.ClusterSnapshot, node string, mb int64) bool {
	if mb <= 0 || snap == nil {
		return true
	}

	for _, n := range snap.Nodes {
		if n.Name == node {
			return n.RAMReservedMB+mb <= n.ReservableRAM()
		}
	}
	return true
}

func PrepareGuardedExecution(ctx context.Context, rt *runtimectx.Context, req GuardedExecutionRequest) (PreparedExecution, error) {
	// CRITICAL INVARIANT: operator-requested task execution in this package's
	// guarded execution path must go through RunGuarded rather than bypassing it.
	NormalizeRequest(&req)

	prepared := PreparedExecution{
		Request: req,
		Result: GuardedExecutionResult{
			Description: req.Description,
			Mode:        req.Mode,
		},
	}

	if err := ValidateRequest(req); err != nil {
		prepared.Result.Error = err.Error()
		prepared.Err = err
		return prepared, err
	}
	if rt == nil || rt.Config == nil || rt.Snapshot == nil {
		err := fmt.Errorf("runtime context unavailable")
		prepared.Result.Error = err.Error()
		prepared.Err = err
		return prepared, err
	}

	prepared.Result.SnapshotStatus = rt.Snapshot.Status
	prepared.Result.Summary = &rt.Snapshot.Summary

	skillStore := rt.Skills
	if skillStore == nil {
		skillStore = &skills.Store{}
	}
	prepared.SkillStore = skillStore
	prepared.State = rt.State
	prepared.Ledger = rt.Ledger
	prepared.Owner = executionOwner(rt, req)

	intent, err := ResolveIntent(req.Description, req.Mode, skillStore)
	if err != nil {
		prepared.Result.Error = err.Error()
		prepared.Err = err
		return prepared, err
	}
	prepared.Intent = intent

	prepared.Result.Intent = intent.Label
	prepared.Result.Command = intent.Command

	// Advisory placement event
	events.EmitToBuffer(events.NoopEmitter{}, events.EventTaskPlacementRequested, map[string]any{
		events.PayloadKeyTaskID: req.Description,
	})

	reqs := prepareRequirements(req.Description, req.Mode, intent)
	if req.MemoryRequestMB > 0 {
		reqs.MemoryRequestMB = req.MemoryRequestMB
	}
	if req.MemoryMaxMB > 0 {
		reqs.MemoryMaxMB = req.MemoryMaxMB
	}
	prepared.Requirements = reqs

	if rt == nil || rt.Snapshot == nil {
		err := fmt.Errorf("cluster snapshot is not available")
		prepared.Result.Error = err.Error()
		prepared.Err = err
		return prepared, err
	}
	nodesToEvaluate := rt.Snapshot.Nodes
	if req.RequestedNode != "" {
		var matchedNode *models.NodeFacts
		for _, n := range rt.Snapshot.Nodes {
			if strings.EqualFold(n.Name, req.RequestedNode) {
				nodeCopy := n
				matchedNode = &nodeCopy
				break
			}
		}
		if matchedNode == nil {
			err := fmt.Errorf("requested node %q not found in snapshot", req.RequestedNode)
			prepared.Result.Error = err.Error()
			prepared.Err = err
			return prepared, err
		}
		req.RequestedNode = matchedNode.Name
		prepared.Request.RequestedNode = matchedNode.Name
		nodesToEvaluate = []models.NodeFacts{*matchedNode}
	}

	decision := placement.SelectBestNode(reqs, nodesToEvaluate, rt.State)

	// Advisory placement decision event (post-decision)
	events.EmitToBuffer(events.NoopEmitter{}, events.EventTaskPlacementRequested, map[string]any{
		events.PayloadKeyTaskID: req.Description,
		events.PayloadKeyNode:   decision.Node,
		"fit_score":             decision.FitScore,
		"ok":                    decision.OK,
		"phase":                 "decision",
	})
	prepared.Result.Reasoning = runtimectx.PrependWarningReasoning(decision.Reasoning, rt.Snapshot.Warnings)
	prepared.Result.Node = decision.Node
	prepared.Result.Tool = decision.Tool
	prepared.Result.Workload = decision.Workload
	prepared.Result.FitScore = decision.FitScore
	prepared.Result.IsLocal = decision.IsLocal

	if !decision.OK {
		var err error
		if req.RequestedNode != "" {
			err = fmt.Errorf("requested node %q rejected by placement: %s", req.RequestedNode, strings.Join(decision.Reasoning, "; "))
		} else {
			err = fmt.Errorf("no suitable node found")
		}
		prepared.Result.Error = err.Error()
		prepared.Err = err
		return prepared, err
	}

	targetNode, ok := findNodeFacts(rt.Snapshot, decision.Node)
	if !ok {
		err := fmt.Errorf("node %q not found in snapshot", decision.Node)
		prepared.Result.Error = err.Error()
		prepared.Err = err
		return prepared, err
	}
	prepared.TargetNode = targetNode

	// Check for node config if remote
	var targetConfig config.NodeConfig
	isLocal := models.IsLocalNode(targetNode)
	if !isLocal {
		var ok bool
		targetConfig, ok = rt.Config.FindNode(decision.Node)
		if !ok {
			err := fmt.Errorf("node %q not found in config", decision.Node)
			prepared.Result.Error = err.Error()
			prepared.Err = err
			return prepared, err
		}
		prepared.TargetConfig = targetConfig
	}

	reservationMB := ReservationMBForRequirements(reqs)
	prepared.ReservationMB = reservationMB
	if !CanReserve(rt.Snapshot, decision.Node, reservationMB) {
		var err error
		if req.RequestedNode != "" {
			err = fmt.Errorf("requested node %q rejected by placement: reservation cap exceeded (%d MB request)", req.RequestedNode, reservationMB)
		} else {
			err = fmt.Errorf("reservation cap exceeded")
		}
		prepared.Result.Error = fmt.Sprintf("node %s cannot reserve %d MB (current reservations exceed cap)", decision.Node, reservationMB)
		prepared.Err = err
		return prepared, err
	}
	// Advisory event for external observers (MCP agents, hooks, etc.)
	events.EmitToBuffer(events.NoopEmitter{}, events.EventTaskExecutionReserved, map[string]any{
		events.PayloadKeyNode:   decision.Node,
		events.PayloadKeyTaskID: req.Description, // best effort identifier
	})
	prepared.Result.Reasoning = append(prepared.Result.Reasoning, fmt.Sprintf("reservation headroom protected: %dMB", reservationMB))

	if err := enforceLocalExecutionSafety(ctx, targetNode, reqs, &prepared.Result); err != nil {
		prepared.Result.Error = err.Error()
		prepared.Err = err
		return prepared, err
	}

	turboPlan := turboexec.Prepare(targetNode, reqs, intent.Command)
	prepared.Result.Reasoning = append(prepared.Result.Reasoning, turboPlan.Notes...)
	commandToRun := turboPlan.Command

	safetyDecision := sharedSafetyEvaluator.Evaluate(commandToRun, req.OwnerSurface)
	if safetyDecision.Verdict == safety.VerdictDeny {
		prepared.Result.Blocked = true
		prepared.Result.BlockReason = strings.Join(safetyDecision.Reasons, "; ")
		prepared.Result.DumbScore = 100
		prepared.Result.Error = prepared.Result.BlockReason
		return prepared, nil
	}

	contextJSON, err := knowledge.ExecutionContextJSON(rt.Snapshot, rt.State, decision, req.Description, intent.MatchedScript, intent.MatchedSkill)
	if err != nil {
		prepared.Result.Error = err.Error()
		prepared.Err = err
		return prepared, err
	}
	prepared.ContextJSON = contextJSON

	nixPlan := PlanNixExecution(targetNode, reqs, commandToRun)
	prepared.Result.Command = commandToRun
	if nixPlan.Enabled {
		commandToRun = nixPlan.Command
		prepared.Result.Command = commandToRun
		prepared.Result.Reasoning = append(prepared.Result.Reasoning, nixPlan.Notes...)
	}
	prepared.Command = commandToRun
	prepared.ExtraEnv = append(turboPlan.Env, nixPlan.Env...)
	prepared.Result.ExecID = generateExecID(prepared.Result.Node)

	return prepared, nil
}

func RunPreparedExecution(ctx context.Context, prepared PreparedExecution) (GuardedExecutionResult, error) {
	if prepared.Err != nil || prepared.Result.Blocked {
		return prepared.Result, prepared.Err
	}

	if prepared.Request.OwnerSurface == OwnerSurfaceTaskRun {
		ok, cleanup, err := handleDirtyWorkingTree(ctx, prepared.Request, prepared.Request.Stderr)
		if err != nil || !ok {
			prepared.Result.Error = err.Error()
			prepared.Result.ExitCode = 1
			return prepared.Result, err
		}
		defer cleanup()
	}

	if prepared.Request.OnReady != nil {
		prepared.Request.OnReady(prepared.Result)
	}

	if models.IsLocalNode(prepared.TargetNode) {
		return runLocal(
			ctx,
			prepared.State,
			prepared.SkillStore,
			prepared.Owner,
			prepared.Request,
			prepared.Requirements,
			prepared.Result,
			prepared.ReservationMB,
			prepared.Command,
			prepared.ExtraEnv,
			prepared.ContextJSON,
			prepared.Ledger,
		)
	}

	return runRemote(
		ctx,
		prepared.State,
		prepared.SkillStore,
		prepared.Owner,
		prepared.Request,
		prepared.Requirements,
		prepared.Result,
		prepared.TargetConfig,
		prepared.TargetNode.ResolvedDialTarget,
		prepared.ReservationMB,
		prepared.Command,
		prepared.ExtraEnv,
		prepared.ContextJSON,
		prepared.Ledger,
	)
}

func RunGuarded(ctx context.Context, rt *runtimectx.Context, req GuardedExecutionRequest) (GuardedExecutionResult, error) {
	prepared, err := PrepareGuardedExecution(ctx, rt, req)
	if err != nil || prepared.Result.Blocked {
		return prepared.Result, err
	}
	return RunPreparedExecution(ctx, prepared)
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

	var logFile *os.File
	logDir := persist.AxisPath("logs")
	if err := os.MkdirAll(logDir, 0755); err == nil {
		logPath := filepath.Join(logDir, fmt.Sprintf("task-%s.log", execID))
		if lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
			logFile = lf
			defer logFile.Close()
		}
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
	out, peakRAMMB, runErr := runWithReservationHeartbeat(ledger, execID, func() (string, int64, error) {
		return runLocalWithOutput(ctx, command, env, stdoutWriter, stderrWriter)
	})
	elapsed := time.Since(startedAt)
	resp.Output = out
	resp.ExitCode = exitCode(runErr)
	if runErr != nil {
		resp.Error = runErr.Error()
		recordFailure(skillStore, req.Description, resp.ExitCode)
		runtimeChanged = true
		recordExecutionOutcome(st, reqs, resp, runErr, elapsed, peakRAMMB)
		resp.PeakRAMMB = peakRAMMB
		resp.WallTimeMS = durationMilliseconds(elapsed)
		return resp, runErr
	}

	recordSuccess(skillStore, req.Description, command, resp.Node)
	runtimeChanged = true
	recordExecutionOutcome(st, reqs, resp, nil, elapsed, peakRAMMB)
	resp.OK = true
	resp.PeakRAMMB = peakRAMMB
	resp.WallTimeMS = durationMilliseconds(elapsed)
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
	writeJSONCmd := fmt.Sprintf("cat > %s << 'AXIS_EOF'\n%s\nAXIS_EOF\n", shellescape.Quote(remoteContextPath), string(contextJSON))
	if _, err := executor.Run(ctx, writeJSONCmd); err != nil {
		resp.Error = err.Error()
		resp.ExitCode = 1
		return resp, err
	}

	execID := resp.ExecID
	if execID == "" {
		execID = generateExecID(resp.Node)
		resp.ExecID = execID
	}

	var logFile *os.File
	logDir := persist.AxisPath("logs")
	if err := os.MkdirAll(logDir, 0755); err == nil {
		logPath := filepath.Join(logDir, fmt.Sprintf("task-%s.log", execID))
		if lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644); err == nil {
			logFile = lf
			defer logFile.Close()
		}
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

	// Also emit the canonical events package event for external consumers (MCP, future hooks).
	eventName := ""
	switch trigger {
	case StateChangeExecutionReserved:
		eventName = events.EventTaskExecutionReserved
	case StateChangeExecutionFinished:
		eventName = events.EventTaskExecutionFinished
	}

	if eventName != "" {
		events.EmitToBuffer(events.NoopEmitter{}, eventName, map[string]any{
			events.PayloadKeyNode:    resp.Node,
			events.PayloadKeyTaskID:  req.Description,
			events.PayloadKeyTrigger: trigger,
			events.PayloadKeyResult:  resp.OK,
		})
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
