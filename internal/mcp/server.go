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
	"github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/state"
	"github.com/toasterbook88/axis/internal/transport"
)

const (
	clusterSnapshotURI = "cluster://snapshot"
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

type mcpRemoteExecutor interface {
	Run(context.Context, string) (string, error)
	Close() error
}

var loadMCPRuntime = runtimectx.Load
var loadMCPState = state.Load
var fetchCachedSnapshot = daemon.FetchSnapshot
var lookPath = exec.LookPath
var execCommand = exec.CommandContext
var newMCPRemoteExecutor = func(nc config.NodeConfig) mcpRemoteExecutor {
	return transport.NewSSHExecutor(nc.Hostname, nc.EffectiveSSHPort(), nc.SSHUser, nc.EffectiveTimeout())
}

func NewServer(useCache bool, cacheAddr string) *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer(
		"axis",
		buildinfo.Version,
		mcpserver.WithRecovery(),
		mcpserver.WithToolCapabilities(false),
		mcpserver.WithResourceCapabilities(false, false),
		mcpserver.WithInstructions("AXIS exposes read-only cluster state and diagnostics. Do not assume any write or execution authority."),
	)

	registerResources(s, useCache, cacheAddr)
	registerTools(s, useCache, cacheAddr)

	return s
}

func registerResources(s *mcpserver.MCPServer, useCache bool, cacheAddr string) {
	snapResource := mcpproto.NewResource(
		clusterSnapshotURI,
		"Cluster Snapshot",
		mcpproto.WithResourceDescription("Current AXIS cluster state as JSON"),
		mcpproto.WithMIMEType("application/json"),
	)
	s.AddResource(snapResource, func(ctx context.Context, req mcpproto.ReadResourceRequest) ([]mcpproto.ResourceContents, error) {
		return clusterSnapshotResource(ctx, req, useCache, cacheAddr)
	})
}

func registerTools(s *mcpserver.MCPServer, useCache bool, cacheAddr string) {
	s.AddTool(
		mcpproto.NewTool(
			"cluster_snapshot",
			mcpproto.WithDescription("Return the current AXIS cluster snapshot"),
			mcpproto.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
			return clusterSnapshotTool(ctx, req, useCache, cacheAddr)
		},
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
		func(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
			return placementDecisionTool(ctx, req, useCache, cacheAddr)
		},
	)

	s.AddTool(
		mcpproto.NewTool(
			"axis_health",
			mcpproto.WithDescription("Return the same AXIS health payload exposed by the local HTTP control surface"),
			mcpproto.WithReadOnlyHintAnnotation(true),
		),
		func(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
			return axisHealthTool(ctx, req, useCache, cacheAddr)
		},
	)

	s.AddTool(
		mcpproto.NewTool(
			"axis_tools",
			mcpproto.WithDescription("Return the same AXIS tool catalog exposed by the local HTTP control surface"),
			mcpproto.WithReadOnlyHintAnnotation(true),
		),
		axisToolsTool,
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

}

func ServeStdio(cached bool, cacheAddr string) error {
	return mcpserver.ServeStdio(NewServer(cached, cacheAddr))
}

func clusterSnapshotResource(ctx context.Context, req mcpproto.ReadResourceRequest, useCache bool, cacheAddr string) ([]mcpproto.ResourceContents, error) {
	snap, err := currentSnapshot(ctx, useCache, cacheAddr)
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

func clusterSnapshotTool(ctx context.Context, req mcpproto.CallToolRequest, useCache bool, cacheAddr string) (*mcpproto.CallToolResult, error) {
	snap, err := currentSnapshot(ctx, useCache, cacheAddr)
	if err != nil {
		return mcpproto.NewToolResultError(err.Error()), nil
	}
	return mcpproto.NewToolResultJSON(snap)
}

func placementDecisionTool(ctx context.Context, req mcpproto.CallToolRequest, useCache bool, cacheAddr string) (*mcpproto.CallToolResult, error) {
	desc, err := req.RequireString("description")
	if err != nil {
		return mcpproto.NewToolResultError(err.Error()), nil
	}

	snap, st, err := currentPlacementInputs(ctx, useCache, cacheAddr)
	if err != nil {
		return mcpproto.NewToolResultError(err.Error()), nil
	}

	decision := placement.SelectBestNode(placement.InferRequirements(desc), snap.Nodes, st)
	decision.Reasoning = runtimectx.PrependWarningReasoning(decision.Reasoning, snap.Warnings)
	return mcpproto.NewToolResultJSON(decision)
}

func axisHealthTool(ctx context.Context, req mcpproto.CallToolRequest, useCache bool, cacheAddr string) (*mcpproto.CallToolResult, error) {
	if useCache {
		meta, err := daemon.FetchMeta(ctx, cacheAddr)
		if err != nil {
			return mcpproto.NewToolResultError(err.Error()), nil
		}
		return mcpproto.NewToolResultJSON(daemon.HealthPayload(&meta))
	}

	snap, err := currentSnapshot(ctx, false, cacheAddr)
	if err != nil {
		return mcpproto.NewToolResultError(err.Error()), nil
	}

	payload := daemon.HealthPayload(nil)
	if snap != nil {
		payload["snapshot_status"] = snap.Status
		payload["warnings"] = len(snap.Warnings)
	}
	return mcpproto.NewToolResultJSON(payload)
}

func axisToolsTool(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
	_ = ctx
	_ = req
	return mcpproto.NewToolResultJSON(daemon.ToolsResponse{Tools: daemon.ToolDefinitions()})
}

func ipAddrTool(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return toolResultJSON(runFirstAvailableCommand(ctx,
		[]string{"ip", "addr"},
		[]string{"ifconfig"},
	))
}

func tailscaleStatusTool(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return toolResultJSON(runCommand(ctx, "tailscale", "status", "--json"))
}

func tailscalePingTool(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
	peer, err := req.RequireString("peer")
	if err != nil {
		return mcpproto.NewToolResultError(err.Error()), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return toolResultJSON(runCommand(ctx, "tailscale", "ping", peer))
}

func wireguardStatusTool(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return toolResultJSON(runCommand(ctx, "wg", "show"))
}

func dockerPSTool(ctx context.Context, req mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return toolResultJSON(runCommand(ctx, "docker", "ps", "--format", "{{json .}}"))
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

	exec := newMCPRemoteExecutor(nc)
	defer exec.Close()

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(nc.EffectiveTimeout()+2)*time.Second)
	defer cancel()

	out, runErr := exec.Run(runCtx, "printf axis-ok")
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

func currentSnapshot(ctx context.Context, useCache bool, cacheAddr string) (*models.ClusterSnapshot, error) {
	if useCache {
		snap, _, err := fetchCachedSnapshot(ctx, cacheAddr)
		if err != nil {
			return nil, err
		}
		return snap, nil
	}

	toolCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	rt, err := loadMCPRuntime(toolCtx)
	if err != nil {
		return nil, err
	}
	return rt.Snapshot, nil
}

func currentPlacementInputs(ctx context.Context, useCache bool, cacheAddr string) (*models.ClusterSnapshot, *state.ClusterState, error) {
	if !useCache {
		toolCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		rt, err := loadMCPRuntime(toolCtx)
		if err != nil {
			return nil, nil, err
		}
		return rt.Snapshot, rt.State, nil
	}

	snap, err := currentSnapshot(ctx, true, cacheAddr)
	if err != nil {
		return nil, nil, err
	}

	st, stateErr := loadMCPState()
	if stateErr != nil && st == nil {
		return nil, nil, stateErr
	}
	if stateErr != nil {
		snap = daemon.CloneSnapshot(snap)
		appendMCPWarningIfMissing(snap, models.Warning{
			Kind:    "state",
			Message: stateErr.Error(),
		})
	}
	return snap, st, nil
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
		if _, err := lookPath(candidate[0]); err == nil {
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
	path, err := lookPath(name)
	if err != nil {
		return commandResult{
			Available: false,
			Command:   strings.Join(append([]string{name}, args...), " "),
			Error:     err.Error(),
		}
	}

	cmd := execCommand(ctx, path, args...)
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

func appendMCPWarningIfMissing(snap *models.ClusterSnapshot, warning models.Warning) {
	if snap == nil {
		return
	}
	for _, existing := range snap.Warnings {
		if existing.Kind == warning.Kind && existing.Message == warning.Message && existing.Node == warning.Node {
			return
		}
	}
	snap.Warnings = append(snap.Warnings, warning)
}

func toolResultJSON(v any) (*mcpproto.CallToolResult, error) {
	return mcpproto.NewToolResultJSON(v)
}
