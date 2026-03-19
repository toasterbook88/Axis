package axismcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	mcpproto "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/snapshot"
	"github.com/toasterbook88/axis/internal/transport"
)

const (
	clusterSnapshotURI = "cluster://snapshot"
	defaultToolTimeout = 20 * time.Second
)

type commandResult struct {
	Available bool   `json:"available"`
	Command   string `json:"command,omitempty"`
	Output    string `json:"output,omitempty"`
	Error     string `json:"error,omitempty"`
}

type sshConnectivityResult struct {
	Node   string `json:"node"`
	Host   string `json:"host"`
	OK     bool   `json:"ok"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

func NewServer() *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer(
		"axis",
		"0.1.0",
		mcpserver.WithRecovery(),
		mcpserver.WithToolCapabilities(false),
		mcpserver.WithResourceCapabilities(false, false),
		mcpserver.WithInstructions("AXIS exposes read-only cluster state and diagnostics. Do not assume any write or execution authority."),
	)

	snapResource := mcpproto.NewResource(
		clusterSnapshotURI,
		"Cluster Snapshot",
		mcpproto.WithResourceDescription("Current AXIS cluster state as JSON"),
		mcpproto.WithMIMEType("application/json"),
	)
	s.AddResource(snapResource, clusterSnapshotResource)

	s.AddTool(
		mcpproto.NewTool(
			"cluster_snapshot",
			mcpproto.WithDescription("Return the current AXIS cluster snapshot"),
			mcpproto.WithReadOnlyHintAnnotation(true),
		),
		clusterSnapshotTool,
	)

	s.AddTool(
		mcpproto.NewTool(
			"placement_decision",
			mcpproto.WithDescription("Select the best node for a task (advisory only)"),
			mcpproto.WithReadOnlyHintAnnotation(true),
			mcpproto.WithString(
				"description",
				mcpproto.Required(),
				mcpproto.Description("Task description to evaluate"),
			),
		),
		placementDecisionTool,
	)

	s.AddTool(
		mcpproto.NewTool(
			"ip_addr",
			mcpproto.WithDescription("Return local interface/address information"),
			mcpproto.WithReadOnlyHintAnnotation(true),
		),
		ipAddrTool,
	)

	s.AddTool(
		mcpproto.NewTool(
			"tailscale_status",
			mcpproto.WithDescription("Return local Tailscale status"),
			mcpproto.WithReadOnlyHintAnnotation(true),
		),
		tailscaleStatusTool,
	)

	s.AddTool(
		mcpproto.NewTool(
			"tailscale_ping",
			mcpproto.WithDescription("Ping a Tailscale peer from the current node"),
			mcpproto.WithReadOnlyHintAnnotation(true),
			mcpproto.WithString(
				"peer",
				mcpproto.Required(),
				mcpproto.Description("Tailscale peer name or IP"),
			),
		),
		tailscalePingTool,
	)

	s.AddTool(
		mcpproto.NewTool(
			"wireguard_status",
			mcpproto.WithDescription("Return local WireGuard status via wg show"),
			mcpproto.WithReadOnlyHintAnnotation(true),
		),
		wireguardStatusTool,
	)

	s.AddTool(
		mcpproto.NewTool(
			"docker_ps",
			mcpproto.WithDescription("Return local Docker container listing"),
			mcpproto.WithReadOnlyHintAnnotation(true),
		),
		dockerPSTool,
	)

	s.AddTool(
		mcpproto.NewTool(
			"ssh_connectivity_test",
			mcpproto.WithDescription("Test SSH connectivity to a configured AXIS node"),
			mcpproto.WithReadOnlyHintAnnotation(true),
			mcpproto.WithString(
				"node",
				mcpproto.Required(),
				mcpproto.Description("Configured node name"),
			),
		),
		sshConnectivityTestTool,
	)

	return s
}

func ServeStdio() error {
	return mcpserver.ServeStdio(NewServer())
}

func clusterSnapshotResource(ctx context.Context, req mcpproto.ReadResourceRequest) ([]mcpproto.ResourceContents, error) {
	snap, err := currentSnapshot(ctx)
	if err != nil {
		return nil, err
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return nil, err
	}

	return []mcpproto.ResourceContents{
		mcpproto.TextResourceContents{
			URI:      clusterSnapshotURI,
			MIMEType: "application/json",
			Text:     string(data),
		},
	}, nil
}

func clusterSnapshotTool(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
	snap, err := currentSnapshot(ctx)
	if err != nil {
		return mcpproto.NewToolResultError(err.Error()), nil
	}
	return mcpproto.NewToolResultJSON(snap)
}

func placementDecisionTool(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
	desc, err := req.RequireString("description")
	if err != nil {
		return mcpproto.NewToolResultError(err.Error()), nil
	}

	snap, err := currentSnapshot(ctx)
	if err != nil {
		return mcpproto.NewToolResultError(err.Error()), nil
	}

	decision := placement.SelectBestNode(placement.InferRequirements(desc), snap.Nodes)
	return mcpproto.NewToolResultJSON(decision)
}

func ipAddrTool(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
	return toolResultJSON(runFirstAvailableCommand(withTimeout(ctx, 5*time.Second),
		[]string{"ip", "addr"},
		[]string{"ifconfig"},
	))
}

func tailscaleStatusTool(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
	return toolResultJSON(runCommand(withTimeout(ctx, 10*time.Second), "tailscale", "status", "--json"))
}

func tailscalePingTool(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
	peer, err := req.RequireString("peer")
	if err != nil {
		return mcpproto.NewToolResultError(err.Error()), nil
	}
	return toolResultJSON(runCommand(withTimeout(ctx, 10*time.Second), "tailscale", "ping", peer))
}

func wireguardStatusTool(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
	return toolResultJSON(runCommand(withTimeout(ctx, 10*time.Second), "wg", "show"))
}

func dockerPSTool(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
	return toolResultJSON(runCommand(withTimeout(ctx, 10*time.Second), "docker", "ps", "--format", "{{json .}}"))
}

func sshConnectivityTestTool(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
	nodeName, err := req.RequireString("node")
	if err != nil {
		return mcpproto.NewToolResultError(err.Error()), nil
	}

	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil {
		return mcpproto.NewToolResultError(err.Error()), nil
	}

	nc, ok := findNodeConfig(cfg, nodeName)
	if !ok {
		return mcpproto.NewToolResultError(fmt.Sprintf("node %q not found in %s", nodeName, config.DefaultConfigPath())), nil
	}

	exec := transport.NewSSHExecutor(nc.Hostname, nc.EffectiveSSHPort(), nc.SSHUser, nc.EffectiveTimeout())
	defer exec.Close()

	out, runErr := exec.Run(withTimeout(ctx, time.Duration(nc.EffectiveTimeout()+2)*time.Second), "printf axis-ok")
	result := sshConnectivityResult{
		Node:   nc.Name,
		Host:   nc.Hostname,
		OK:     runErr == nil && strings.TrimSpace(out) == "axis-ok",
		Output: strings.TrimSpace(out),
	}
	if runErr != nil {
		result.Error = runErr.Error()
	}

	return mcpproto.NewToolResultJSON(result)
}

func currentSnapshot(ctx context.Context) (*models.ClusterSnapshot, error) {
	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil {
		return nil, err
	}

	toolCtx := withTimeout(ctx, 60*time.Second)
	nodes := discovery.Discover(toolCtx, cfg)
	snap := snapshot.Build(nodes)
	return snap, nil
}

func withTimeout(ctx context.Context, d time.Duration) context.Context {
	if _, ok := ctx.Deadline(); ok {
		return ctx
	}
	child, _ := context.WithTimeout(ctx, d)
	return child
}

func findNodeConfig(cfg *config.Config, name string) (config.NodeConfig, bool) {
	for _, n := range cfg.Nodes {
		if strings.EqualFold(n.Name, name) {
			return n, true
		}
	}
	return config.NodeConfig{}, false
}

func runFirstAvailableCommand(ctx context.Context, candidates ...[]string) commandResult {
	for _, candidate := range candidates {
		if len(candidate) == 0 {
			continue
		}
		if _, err := exec.LookPath(candidate[0]); err == nil {
			return runCommand(ctx, candidate[0], candidate[1:]...)
		}
	}
	return commandResult{
		Available: false,
		Command:   "ip addr | ifconfig",
		Error:     "no supported network interface command found",
	}
}

func runCommand(ctx context.Context, name string, args ...string) commandResult {
	path, err := exec.LookPath(name)
	if err != nil {
		return commandResult{
			Available: false,
			Command:   strings.Join(append([]string{name}, args...), " "),
			Error:     err.Error(),
		}
	}

	cmd := exec.CommandContext(ctx, path, args...)
	out, err := cmd.CombinedOutput()
	result := commandResult{
		Available: true,
		Command:   strings.Join(append([]string{name}, args...), " "),
		Output:    strings.TrimSpace(string(out)),
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func toolResultJSON(v any) (*mcpproto.CallToolResult, error) {
	return mcpproto.NewToolResultJSON(v)
}
