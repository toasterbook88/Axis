package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/modellife"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/transport"
)

var loadModelSnapshot = func(ctx context.Context) (*models.ClusterSnapshot, error) {
	rt, err := runtimectx.Load(ctx)
	if err != nil {
		return nil, err
	}
	if rt == nil || rt.Snapshot == nil {
		return nil, fmt.Errorf("no cluster snapshot")
	}
	return rt.Snapshot, nil
}

var loadModelConfig = func() (*config.Config, error) {
	return config.Load(config.DefaultConfigPath())
}

type modelProcessRunner interface {
	Start(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, plan modellife.StartPlan) error
	Stop(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, port int) (modelStopDisposition, error)
	Probe(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, port int) error
}

var defaultModelRunner modelProcessRunner = liveModelRunner{}

func modelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "Inspect resident models or manage llama-server on a named node",
	}
	cmd.AddCommand(modelListCmd())
	cmd.AddCommand(modelInspectCmd())
	cmd.AddCommand(modelStartCmd())
	cmd.AddCommand(modelStopCmd())
	return cmd
}

func modelStartCmd() *cobra.Command {
	var node, weights string
	var port int
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start llama-server on a named node (explicit port and weights)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
			defer cancel()
			return runModelStart(ctx, cmd, node, weights, port, defaultModelRunner)
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "Cluster node name (required)")
	cmd.Flags().StringVar(&weights, "weights", "", "GGUF path on a named local volume (required)")
	cmd.Flags().IntVar(&port, "port", 0, "Listen port (required; no default)")
	_ = cmd.MarkFlagRequired("node")
	_ = cmd.MarkFlagRequired("weights")
	_ = cmd.MarkFlagRequired("port")
	return cmd
}

func modelStopCmd() *cobra.Command {
	var node string
	var port int
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the llama-server listening on --port on a named node",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()
			return runModelStop(ctx, cmd, node, port, defaultModelRunner)
		},
	}
	cmd.Flags().StringVar(&node, "node", "", "Cluster node name (required)")
	cmd.Flags().IntVar(&port, "port", 0, "Listen port (required)")
	_ = cmd.MarkFlagRequired("node")
	_ = cmd.MarkFlagRequired("port")
	return cmd
}

func runModelStart(ctx context.Context, cmd *cobra.Command, nodeName, weights string, port int, runner modelProcessRunner) error {
	nf, cfgNode, err := resolveModelNode(ctx, nodeName)
	if err != nil {
		return err
	}
	plan, err := modellife.PlanStart(nf, weights, port)
	if err != nil {
		return err
	}
	if err := runner.Start(ctx, nf, cfgNode, plan); err != nil {
		return err
	}
	if err := runner.Probe(ctx, nf, cfgNode, plan.Port); err != nil {
		return fmt.Errorf("started but probe failed: %w", err)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "started %s on %s:%d volume %s\n", plan.Argv[0], plan.Node, plan.Port, plan.Volume)
	return err
}

func runModelStop(ctx context.Context, cmd *cobra.Command, nodeName string, port int, runner modelProcessRunner) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	nf, cfgNode, err := resolveModelNode(ctx, nodeName)
	if err != nil {
		return err
	}
	disposition, err := runner.Stop(ctx, nf, cfgNode, port)
	if err != nil {
		return err
	}
	// Report what was actually observed. Only "stopped" is a successful
	// lifecycle transition; the rest mean nothing was stopped, so they must
	// not print success, and must fail for shell automation.
	if _, writeErr := fmt.Fprintf(cmd.OutOrStdout(), "%s %s:%d\n", disposition, nf.Name, port); writeErr != nil {
		return writeErr
	}
	if disposition == modelStopStopped {
		return nil
	}
	return ExitCodeError{
		Code:    ExitErrCommandFail,
		Message: fmt.Sprintf("model stop on %s:%d: %s", nf.Name, port, modelStopExplanation(disposition)),
	}
}

func modelStopExplanation(d modelStopDisposition) string {
	switch d {
	case modelStopNotRunning:
		return "no listener was running on that port"
	case modelStopWrongOwner:
		return "the listener is not an axis-managed llama-server; refusing to kill it"
	case modelStopInspectionUnavailable:
		return "port ownership could not be inspected (fuser, lsof, or ps unavailable)"
	default:
		return string(d)
	}
}

func resolveModelNode(ctx context.Context, name string) (models.NodeFacts, *config.NodeConfig, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return models.NodeFacts{}, nil, fmt.Errorf("node is required")
	}
	snap, err := loadModelSnapshot(ctx)
	if err != nil {
		return models.NodeFacts{}, nil, err
	}
	var nf *models.NodeFacts
	for i := range snap.Nodes {
		if snap.Nodes[i].Name == name {
			nf = &snap.Nodes[i]
			break
		}
	}
	if nf == nil {
		return models.NodeFacts{}, nil, fmt.Errorf("node %s not in snapshot", name)
	}
	cfg, err := loadModelConfig()
	if err != nil {
		return *nf, nil, err
	}
	for i := range cfg.Nodes {
		if cfg.Nodes[i].Name == name {
			return *nf, &cfg.Nodes[i], nil
		}
	}
	if models.IsLocalNode(*nf) {
		return *nf, nil, nil
	}
	return *nf, nil, fmt.Errorf("node %s has no configuration entry", name)
}

type liveModelRunner struct{}

func (liveModelRunner) Start(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, plan modellife.StartPlan) error {
	if len(plan.Argv) == 0 {
		return fmt.Errorf("empty argv")
	}
	script := shellStart(plan.Argv, plan.Port)
	return runOnNode(ctx, node, cfgNode, script)
}

func (liveModelRunner) Stop(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, port int) (modelStopDisposition, error) {
	script := shellStop(port)
	out, err := runOnNodeCapturing(ctx, node, cfgNode, script)
	return classifyModelStop(out, err)
}

func (liveModelRunner) Probe(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, port int) error {
	script := shellProbe(port)
	var last error
	for i := 0; i < 10; i++ {
		if err := runOnNode(ctx, node, cfgNode, script); err == nil {
			return nil
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if last == nil {
		last = fmt.Errorf("probe failed")
	}
	return last
}

func shellStart(argv []string, port int) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
	}
	return shellListenerLookup(port) + fmt.Sprintf(
		"if test -n \"$_axis_pids\"; then "+
			"echo \"refusing to start llama-server: port %d already has listener pid(s) $_axis_pids\" >&2; exit 1; fi; "+
			"nohup %s >/dev/null 2>&1 &",
		port, strings.Join(quoted, " "),
	)
}

func shellStop(port int) string {
	return shellListenerLookup(port) +
		"if test -z \"$_axis_pids\"; then echo '" + modelStopMarker + "not_running'; exit 0; fi; " +
		shellLlamaServerOwnerGuard(port) +
		"for _axis_pid in $_axis_pids; do kill -KILL \"$_axis_pid\" || exit $?; done; " +
		"echo '" + modelStopMarker + "stopped'"
}

func shellProbe(port int) string {
	return shellListenerLookup(port) + fmt.Sprintf(
		"if test -z \"$_axis_pids\"; then echo \"no listener on port %d\" >&2; exit 1; fi; ",
		port,
	) + shellLlamaServerOwnerGuard(port) + fmt.Sprintf(
		"curl -fsS --max-time 5 http://127.0.0.1:%d/v1/models >/dev/null",
		port,
	)
}

func shellLlamaServerOwnerGuard(port int) string {
	return fmt.Sprintf(
		"if ! command -v ps >/dev/null 2>&1; then echo 'axis model requires ps to verify process ownership' >&2; echo '"+modelStopMarker+"inspection_unavailable' >&2; exit 127; fi; "+
			"for _axis_pid in $_axis_pids; do "+
			"case \"$_axis_pid\" in ''|*[!0-9]*) echo \"refusing invalid listener pid $_axis_pid\" >&2; exit 1;; esac; "+
			"_axis_cmd=$(ps -p \"$_axis_pid\" -o comm=) || exit $?; _axis_cmd=${_axis_cmd##*/}; "+
			"if test \"$_axis_cmd\" != llama-server; then "+
			"echo \"refusing port %d: pid $_axis_pid is $_axis_cmd, not llama-server\" >&2; echo '"+modelStopMarker+"wrong_owner' >&2; exit 1; fi; "+
			"done; ",
		port,
	)
}

func shellListenerLookup(port int) string {
	return fmt.Sprintf(
		"if command -v fuser >/dev/null 2>&1; then "+
			"_axis_pids=$(fuser %d/tcp 2>/dev/null); _axis_rc=$?; "+
			"elif command -v lsof >/dev/null 2>&1; then "+
			"_axis_pids=$(lsof -nP -tiTCP:%d -sTCP:LISTEN); _axis_rc=$?; "+
			"else echo 'axis model requires fuser or lsof to inspect port ownership' >&2; echo '"+modelStopMarker+"inspection_unavailable' >&2; exit 127; fi; "+
			"if test \"$_axis_rc\" -gt 1; then exit \"$_axis_rc\"; fi; "+
			"true; ",
		port, port,
	)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func runOnNode(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, script string) error {
	_, err := runOnNodeCapturing(ctx, node, cfgNode, script)
	return err
}

// runOnNodeCapturing is runOnNode that also returns the command output, so
// callers can read a result marker the script emitted. Both transports return
// combined output, including on failure.
func runOnNodeCapturing(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, script string) (string, error) {
	if models.IsLocalNode(node) {
		ex := transport.NewLocalExecutor()
		return ex.Run(ctx, script)
	}
	if cfgNode == nil {
		return "", fmt.Errorf("node %s has no configuration entry", node.Name)
	}
	spec := cfgNode.SSHDialSpec()
	ex := transport.NewSSHExecutorFromDial(spec.Host, spec.Port, spec.User, spec.DialTimeoutSec, spec.Fallbacks)
	defer ex.Close()
	if err := ex.Connect(ctx); err != nil {
		return "", err
	}
	return ex.Run(ctx, script)
}

// modelStopDisposition is the observed outcome of a stop request. Only
// modelStopStopped is a successful lifecycle transition; the others describe
// states where nothing was stopped, and must not be reported as success.
type modelStopDisposition string

const (
	modelStopStopped               modelStopDisposition = "stopped"
	modelStopNotRunning            modelStopDisposition = "not_running"
	modelStopWrongOwner            modelStopDisposition = "wrong_owner"
	modelStopInspectionUnavailable modelStopDisposition = "inspection_unavailable"
)

// modelStopMarker is emitted by the stop script so the outcome survives both
// the local and SSH transports, neither of which exposes a portable exit
// status to the caller.
const modelStopMarker = "axis-stop-result:"

// classifyModelStop maps script output and error into a typed disposition.
// A missing marker with no error is treated as stopped only when the script
// said so; an unrecognized success is an error, never an assumed success.
func classifyModelStop(out string, err error) (modelStopDisposition, error) {
	switch {
	case strings.Contains(out, modelStopMarker+string(modelStopInspectionUnavailable)):
		return modelStopInspectionUnavailable, nil
	case strings.Contains(out, modelStopMarker+string(modelStopWrongOwner)):
		return modelStopWrongOwner, nil
	case strings.Contains(out, modelStopMarker+string(modelStopNotRunning)):
		return modelStopNotRunning, nil
	case strings.Contains(out, modelStopMarker+string(modelStopStopped)):
		return modelStopStopped, nil
	}
	if err != nil {
		return "", err
	}
	return "", fmt.Errorf("model stop produced no recognizable result marker")
}
