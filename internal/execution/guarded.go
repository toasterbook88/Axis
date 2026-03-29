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
)

type GuardedExecutionRequest struct {
	Description string                       `json:"description"`
	Mode        string                       `json:"mode,omitempty"`
	Confirm     string                       `json:"confirm,omitempty"`
	Stdout      io.Writer                    `json:"-"`
	Stderr      io.Writer                    `json:"-"`
	OnReady     func(GuardedExecutionResult) `json:"-"`
}

type GuardedExecutionResult struct {
	OK             bool                   `json:"ok"`
	Description    string                 `json:"description"`
	Mode           string                 `json:"mode,omitempty"`
	Intent         string                 `json:"intent,omitempty"`
	Command        string                 `json:"command,omitempty"`
	Node           string                 `json:"node,omitempty"`
	FitScore       int                    `json:"fit_score,omitempty"`
	IsLocal        bool                   `json:"is_local,omitempty"`
	Reasoning      []string               `json:"reasoning,omitempty"`
	Blocked        bool                   `json:"blocked,omitempty"`
	BlockReason    string                 `json:"block_reason,omitempty"`
	DumbScore      int                    `json:"dumb_score,omitempty"`
	Output         string                 `json:"output,omitempty"`
	Error          string                 `json:"error,omitempty"`
	ExitCode       int                    `json:"exit_code,omitempty"`
	SnapshotStatus models.SnapshotStatus  `json:"snapshot_status,omitempty"`
	Summary        *models.ClusterSummary `json:"summary,omitempty"`
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

var RunLocalShell = func(ctx context.Context, command string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Env = env
	return cmd.CombinedOutput()
}

var StreamLocalShell = func(ctx context.Context, command string, env []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

var PlanNixExecution = PlanNixWrapper

func NormalizeRequest(req *GuardedExecutionRequest) {
	if req == nil {
		return
	}
	req.Description = strings.TrimSpace(req.Description)
	req.Mode = strings.ToLower(strings.TrimSpace(req.Mode))
	req.Confirm = strings.TrimSpace(req.Confirm)
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

	var totalRAM int64
	for _, n := range snap.Nodes {
		if n.Name == node && n.Resources != nil {
			totalRAM = n.Resources.RAMTotalMB
			break
		}
	}
	if totalRAM <= 0 {
		return true
	}

	capMB := totalRAM - 1024
	if capMB < 0 {
		capMB = 0
	}

	ns, ok := st.Nodes[node]
	if !ok {
		return mb <= capMB
	}
	return ns.ReservedMB+mb <= capMB
}

func RunGuarded(ctx context.Context, rt *runtimectx.Context, req GuardedExecutionRequest) (GuardedExecutionResult, error) {
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
		return runLocal(ctx, rt.State, skillStore, req, resp, decision, reservationMB, commandToRun, append(turboPlan.Env, nixPlan.Env...), contextJSON)
	}

	targetConfig, ok := findNodeConfig(rt.Config, decision.Node)
	if !ok {
		resp.Error = fmt.Sprintf("node %q not found in config", decision.Node)
		return resp, fmt.Errorf("node %q not found in config", decision.Node)
	}

	return runRemote(ctx, rt.State, skillStore, req, resp, targetConfig, decision, reservationMB, commandToRun, append(turboPlan.Env, nixPlan.Env...), contextJSON)
}

func prepareRequirements(description, mode string, intent Intent) models.TaskRequirements {
	reqs := placement.InferRequirements(description)

	if intent.MatchedScript != nil {
		reqs.RequiredTools = append([]string(nil), intent.MatchedScript.RequiredTools...)
		if intent.MatchedScript.EstRAMMB > reqs.MinFreeRAMMB {
			reqs.MinFreeRAMMB = intent.MatchedScript.EstRAMMB
		}
	}

	if mode == ModeExec && len(reqs.RequiredTools) > 0 {
		filtered := reqs.RequiredTools[:0]
		for _, tool := range reqs.RequiredTools {
			if !strings.EqualFold(tool, "ollama") {
				filtered = append(filtered, tool)
			}
		}
		reqs.RequiredTools = append([]string(nil), filtered...)
	}

	return reqs
}

func runLocal(
	ctx context.Context,
	st *state.ClusterState,
	skillStore *skills.Store,
	req GuardedExecutionRequest,
	resp GuardedExecutionResult,
	decision models.PlacementDecision,
	reservationMB int64,
	command string,
	extraEnv []string,
	contextJSON []byte,
) (GuardedExecutionResult, error) {
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

	if st != nil {
		execID, err := st.AcquireTask(decision.Node, req.Description, reservationMB)
		if err != nil {
			resp.Error = err.Error()
			return resp, err
		}
		defer func() {
			if releaseErr := st.ReleaseTask(decision.Node, execID, reservationMB); releaseErr != nil && resp.Error == "" {
				resp.Error = releaseErr.Error()
			}
		}()
	}

	out, runErr := runLocalWithOutput(ctx, command, env, req.Stdout, req.Stderr)
	resp.Output = out
	resp.ExitCode = exitCode(runErr)
	if runErr != nil {
		resp.Error = runErr.Error()
		recordFailure(skillStore, req.Description, resp.ExitCode)
		return resp, runErr
	}

	recordSuccess(skillStore, req.Description, command, decision.Node)
	resp.OK = true
	return resp, nil
}

func runRemote(
	ctx context.Context,
	st *state.ClusterState,
	skillStore *skills.Store,
	req GuardedExecutionRequest,
	resp GuardedExecutionResult,
	targetConfig config.NodeConfig,
	decision models.PlacementDecision,
	reservationMB int64,
	command string,
	extraEnv []string,
	contextJSON []byte,
) (GuardedExecutionResult, error) {
	executor := NewRemoteExecutor(targetConfig)
	defer executor.Close()

	remoteContextPath := fmt.Sprintf("/tmp/axis-knows-%d.json", time.Now().UTC().UnixNano())
	writeJSONCmd := fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF\n", shellescape.Quote(remoteContextPath), string(contextJSON))
	if _, err := executor.Run(ctx, writeJSONCmd); err != nil {
		resp.Error = err.Error()
		resp.ExitCode = 1
		return resp, err
	}

	if st != nil {
		execID, err := st.AcquireTask(decision.Node, req.Description, reservationMB)
		if err != nil {
			resp.Error = err.Error()
			return resp, err
		}
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

	out, runErr := runRemoteWithOutput(ctx, executor, runCmd, req.Stdout, req.Stderr)
	resp.Output = out
	resp.ExitCode = exitCode(runErr)
	if runErr != nil {
		resp.Error = runErr.Error()
		recordFailure(skillStore, req.Description, resp.ExitCode)
		return resp, runErr
	}

	recordSuccess(skillStore, req.Description, command, decision.Node)
	resp.OK = true
	return resp, nil
}

func runLocalWithOutput(ctx context.Context, command string, env []string, stdout, stderr io.Writer) (string, error) {
	if stdout == nil && stderr == nil {
		out, err := RunLocalShell(ctx, command, env)
		return string(out), err
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

	err := StreamLocalShell(ctx, command, env, outWriter, errWriter)
	return combinedOutput(stdoutBuf.String(), stderrBuf.String()), err
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

func findNodeConfig(cfg *config.Config, name string) (config.NodeConfig, bool) {
	if cfg == nil {
		return config.NodeConfig{}, false
	}
	for _, n := range cfg.Nodes {
		if strings.EqualFold(n.Name, name) {
			return n, true
		}
	}
	return config.NodeConfig{}, false
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

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return 1
}
