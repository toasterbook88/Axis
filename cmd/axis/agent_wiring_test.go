package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

// TestBuildAgentSessionConfigWiresGuardedShellAndSystem is the live-contract
// guard: if agentCmd ever drops RunShell/RunOnNode/SystemExtra again, this fails.
func TestBuildAgentSessionConfigWiresGuardedShellAndSystem(t *testing.T) {
	cfg := buildAgentSessionConfig(agentSessionParams{
		Endpoint:    "http://localhost:11434",
		Model:       "qwen2.5",
		SystemExtra: "custom system text",
		MaxTurns:    7,
		MaxTokens:   2048,
	})
	if cfg.SystemExtra != "custom system text" {
		t.Fatalf("SystemExtra = %q, want custom system text", cfg.SystemExtra)
	}
	if cfg.RunShell == nil {
		t.Fatal("RunShell is nil — live agent would fall back to direct sh -c")
	}
	if cfg.RunOnNode == nil {
		t.Fatal("RunOnNode is nil — live agent would reject or bypass node commands")
	}
	if cfg.RunTask == nil {
		t.Fatal("RunTask is nil")
	}
	if cfg.Model != "qwen2.5" || cfg.Endpoint != "http://localhost:11434" {
		t.Fatalf("model/endpoint not passed through: model=%q endpoint=%q", cfg.Model, cfg.Endpoint)
	}
	if cfg.MaxTurns != 7 || cfg.MaxTokens != 2048 {
		t.Fatalf("turns/tokens not passed: turns=%d tokens=%d", cfg.MaxTurns, cfg.MaxTokens)
	}
}

func TestGuardedAgentCommandRunnerPinsRequestedNode(t *testing.T) {
	prevLoad := loadAgentShellRuntime
	prevRun := runGuardedAgentShell
	prevDaemonMeta := fetchAgentDaemonMeta
	prevSignal := signalAgentDaemonRefresh
	t.Cleanup(func() {
		loadAgentShellRuntime = prevLoad
		runGuardedAgentShell = prevRun
		fetchAgentDaemonMeta = prevDaemonMeta
		signalAgentDaemonRefresh = prevSignal
	})

	rt := &runtimectx.Context{
		Config: &config.Config{
			Nodes: []config.NodeConfig{
				{Name: "studio", Hostname: "localhost", SSHUser: "me"},
				{Name: "nixos", Hostname: "192.168.1.10", SSHUser: "axis"},
			},
		},
		Snapshot: &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{
				{Name: "studio", Hostname: "localhost"},
				{Name: "nixos", Hostname: "192.168.1.10"},
			},
		},
	}
	loadAgentShellRuntime = func(context.Context) (*runtimectx.Context, error) {
		return rt, nil
	}
	fetchAgentDaemonMeta = func(context.Context, string) (daemon.Metadata, error) {
		return daemon.Metadata{}, errors.New("daemon unavailable")
	}
	signalAgentDaemonRefresh = func(context.Context, string) error { return nil }

	var gotReq execution.GuardedExecutionRequest
	runGuardedAgentShell = func(_ context.Context, gotRT *runtimectx.Context, req execution.GuardedExecutionRequest) (execution.GuardedExecutionResult, error) {
		if gotRT != rt {
			t.Fatalf("unexpected runtime")
		}
		gotReq = req
		return execution.GuardedExecutionResult{
			OK:      true,
			Node:    "nixos",
			Command: req.Description,
			Output:  "ok",
		}, nil
	}

	out, err := guardedAgentCommandRunner("qwen2.5", "nixos")(context.Background(), "echo remote")
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	if !strings.Contains(out, `"node":"nixos"`) {
		t.Fatalf("output = %q", out)
	}
	if gotReq.RequestedNode != "nixos" {
		t.Fatalf("RequestedNode = %q, want nixos", gotReq.RequestedNode)
	}
	if gotReq.OwnerSurface != execution.OwnerSurfaceAgentRunOnNode {
		t.Fatalf("OwnerSurface = %q, want %q", gotReq.OwnerSurface, execution.OwnerSurfaceAgentRunOnNode)
	}
	if gotReq.Description != "echo remote" {
		t.Fatalf("Description = %q", gotReq.Description)
	}
	if gotReq.OwnerLabel != "qwen2.5" {
		t.Fatalf("OwnerLabel = %q", gotReq.OwnerLabel)
	}
	if gotReq.Confirm != execution.ConfirmWord {
		t.Fatalf("Confirm = %q", gotReq.Confirm)
	}
}

func TestBuildAgentSessionConfigRunOnNodeUsesGuardedSurface(t *testing.T) {
	prevLoad := loadAgentShellRuntime
	prevRun := runGuardedAgentShell
	prevDaemonMeta := fetchAgentDaemonMeta
	t.Cleanup(func() {
		loadAgentShellRuntime = prevLoad
		runGuardedAgentShell = prevRun
		fetchAgentDaemonMeta = prevDaemonMeta
	})
	loadAgentShellRuntime = func(context.Context) (*runtimectx.Context, error) {
		return &runtimectx.Context{
			Snapshot: &models.ClusterSnapshot{
				Nodes: []models.NodeFacts{{Name: "nixos", Hostname: "n"}},
			},
		}, nil
	}
	fetchAgentDaemonMeta = func(context.Context, string) (daemon.Metadata, error) {
		return daemon.Metadata{}, errors.New("no daemon")
	}
	runGuardedAgentShell = func(_ context.Context, _ *runtimectx.Context, req execution.GuardedExecutionRequest) (execution.GuardedExecutionResult, error) {
		if req.RequestedNode != "nixos" {
			t.Fatalf("RequestedNode = %q", req.RequestedNode)
		}
		if req.OwnerSurface != execution.OwnerSurfaceAgentRunOnNode {
			t.Fatalf("surface = %q", req.OwnerSurface)
		}
		return execution.GuardedExecutionResult{OK: true, Node: "nixos", Output: "done"}, nil
	}

	cfg := buildAgentSessionConfig(agentSessionParams{Model: "m"})
	out, err := cfg.RunOnNode(context.Background(), "nixos", "uname -a")
	if err != nil {
		t.Fatalf("RunOnNode: %v", err)
	}
	if !strings.Contains(out, "nixos") {
		t.Fatalf("out = %q", out)
	}
}
