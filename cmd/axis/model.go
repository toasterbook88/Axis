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
	Stop(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, port int) error
	Probe(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, port int) error
}

var defaultModelRunner modelProcessRunner = liveModelRunner{}

func modelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "Start or stop a llama-server on a named node",
	}
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
	if err := runner.Stop(ctx, nf, cfgNode, port); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "stopped %s:%d\n", nf.Name, port)
	return err
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

func (liveModelRunner) Stop(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, port int) error {
	script := shellStop(port)
	return runOnNode(ctx, node, cfgNode, script)
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
		"if test -z \"$_axis_pids\"; then exit 0; fi; " +
		shellLlamaServerOwnerGuard(port) +
		"for _axis_pid in $_axis_pids; do kill -KILL \"$_axis_pid\" || exit $?; done"
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
		"if ! command -v ps >/dev/null 2>&1; then echo 'axis model requires ps to verify process ownership' >&2; exit 127; fi; "+
			"for _axis_pid in $_axis_pids; do "+
			"case \"$_axis_pid\" in ''|*[!0-9]*) echo \"refusing invalid listener pid $_axis_pid\" >&2; exit 1;; esac; "+
			"_axis_cmd=$(ps -p \"$_axis_pid\" -o comm=) || exit $?; _axis_cmd=${_axis_cmd##*/}; "+
			"if test \"$_axis_cmd\" != llama-server; then "+
			"echo \"refusing port %d: pid $_axis_pid is $_axis_cmd, not llama-server\" >&2; exit 1; fi; "+
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
			"else echo 'axis model requires fuser or lsof to inspect port ownership' >&2; exit 127; fi; "+
			"if test \"$_axis_rc\" -gt 1; then exit \"$_axis_rc\"; fi; "+
			"true; ",
		port, port,
	)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func runOnNode(ctx context.Context, node models.NodeFacts, cfgNode *config.NodeConfig, script string) error {
	if models.IsLocalNode(node) {
		ex := transport.NewLocalExecutor()
		_, err := ex.Run(ctx, script)
		return err
	}
	if cfgNode == nil {
		return fmt.Errorf("node %s has no configuration entry", node.Name)
	}
	spec := cfgNode.SSHDialSpec()
	ex := transport.NewSSHExecutorFromDial(spec.Host, spec.Port, spec.User, spec.DialTimeoutSec, spec.Fallbacks)
	defer ex.Close()
	if err := ex.Connect(ctx); err != nil {
		return err
	}
	_, err := ex.Run(ctx, script)
	return err
}
