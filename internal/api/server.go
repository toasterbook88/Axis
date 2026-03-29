package api

import (
	"context"
	"net/http"
	"os/exec"
	"sync"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/scripts"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/transport"
)

const DefaultAddr = daemon.DefaultAddr

type ToolDef = daemon.ToolDef
type ToolsResponse = daemon.ToolsResponse
type KnowledgeResponse = daemon.KnowledgeResponse
type RunRequest = execution.GuardedExecutionRequest
type RunResponse = execution.GuardedExecutionResult
type snapshotCache = daemon.SnapshotCache
type remoteExecutor = execution.RemoteExecutor

type runIntent struct {
	command       string
	label         string
	matchedScript *scripts.Script
	matchedSkill  *skills.LearnedSkill
}

var loadLiveRuntime = runtimectx.Load
var newRemoteExecutor = func(nc config.NodeConfig) execution.RemoteExecutor {
	return transport.NewSSHExecutor(nc.Hostname, nc.EffectiveSSHPort(), nc.SSHUser, nc.EffectiveTimeout())
}
var runLocalShell = func(ctx context.Context, command string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Env = env
	return cmd.CombinedOutput()
}

var apiExecutionDepsMu sync.Mutex

func Serve(addr string, cache snapshotCache) error {
	return daemon.Serve(addr, cache)
}

func registerRoutes(mux *http.ServeMux, cache snapshotCache) {
	daemon.RegisterRoutesWithDeps(mux, cache, daemon.RouteDeps{
		LoadRuntime: loadLiveRuntime,
		RunGuarded:  runTaskWithRuntime,
	})
}

func resolveIntent(description, mode string, skillStore *skills.Store) (runIntent, error) {
	intent, err := execution.ResolveIntent(description, mode, skillStore)
	if err != nil {
		return runIntent{}, err
	}
	return runIntent{
		command:       intent.Command,
		label:         intent.Label,
		matchedScript: intent.MatchedScript,
		matchedSkill:  intent.MatchedSkill,
	}, nil
}

func runTask(ctx context.Context, req RunRequest) (RunResponse, error) {
	if req.Confirm == "" {
		req.Confirm = execution.ConfirmWord
	}
	rt, err := loadLiveRuntime(ctx)
	if err != nil {
		resp := RunResponse{
			Description: req.Description,
			Mode:        req.Mode,
			Error:       err.Error(),
		}
		return resp, err
	}
	return runTaskWithRuntime(ctx, rt, req)
}

func runTaskWithRuntime(ctx context.Context, rt *runtimectx.Context, req execution.GuardedExecutionRequest) (RunResponse, error) {
	apiExecutionDepsMu.Lock()
	prevRemoteFactory := execution.NewRemoteExecutor
	prevLocalShell := execution.RunLocalShell
	execution.NewRemoteExecutor = func(nc config.NodeConfig) execution.RemoteExecutor {
		return newRemoteExecutor(nc)
	}
	execution.RunLocalShell = runLocalShell
	defer func() {
		execution.NewRemoteExecutor = prevRemoteFactory
		execution.RunLocalShell = prevLocalShell
		apiExecutionDepsMu.Unlock()
	}()

	return execution.RunGuarded(ctx, rt, req)
}
