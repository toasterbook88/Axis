package execution

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/persist"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/safety"
	"github.com/toasterbook88/axis/internal/scripts"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
	"github.com/toasterbook88/axis/internal/turboexec"
)

func generateExecID(node string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Extreme rare fallback
		return fmt.Sprintf("%d-%s", time.Now().UnixNano(), node)
	}
	return fmt.Sprintf("%s-%s", hex.EncodeToString(b), node)
}

func openTaskLog(execID string) *os.File {
	logPath := filepath.Join(persist.AxisPath("logs"), fmt.Sprintf("task-%s.log", execID))
	f, err := persist.OpenPrivateFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return nil
	}
	return f
}

type GuardedExecutionRequest struct {
	Description      string                                                `json:"description"`
	Mode             string                                                `json:"mode,omitempty"`
	Confirm          string                                                `json:"confirm,omitempty"`
	ExposePorts      string                                                `json:"expose_ports,omitempty"`
	MemoryRequestMB  int64                                                 `json:"memory_request_mb,omitempty"`
	MemoryMaxMB      int64                                                 `json:"memory_max_mb,omitempty"`
	RequestedNode    string                                                `json:"requested_node,omitempty"`
	OwnerSurface     string                                                `json:"-"`
	OwnerLabel       string                                                `json:"-"`
	OriginOverride   models.ExecutionOrigin                                `json:"-"`
	Stdout           io.Writer                                             `json:"-"`
	Stderr           io.Writer                                             `json:"-"`
	Stdin            io.Reader                                             `json:"-"`
	OnReady          func(GuardedExecutionResult)                          `json:"-"`
	OnStateChange    func(context.Context, string, GuardedExecutionResult) `json:"-"`
	Events           ExecutionEventSink                                    `json:"-"`
	BuildContextJSON ExecutionContextBuilder                               `json:"-"`
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
			if nc.IsLocal() {
				return models.NewExecutionOrigin(nc.Name, nc.PrimaryHostname(), nc.StableID)
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
	if req.Events != nil {
		req.Events.PlacementRequested(req.Description)
	}

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
	if req.Events != nil {
		req.Events.PlacementDecided(req.Description, decision.Node, decision.FitScore, decision.OK)
	}
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
	if req.Events != nil {
		req.Events.ExecutionReserved(req.Description, decision.Node)
	}
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

	if req.BuildContextJSON != nil {
		contextJSON, err := req.BuildContextJSON(rt.Snapshot, rt.State, decision, req.Description, intent.MatchedScript, intent.MatchedSkill)
		if err != nil {
			prepared.Result.Error = err.Error()
			prepared.Err = err
			return prepared, err
		}
		prepared.ContextJSON = contextJSON
	}

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
