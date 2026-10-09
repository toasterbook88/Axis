package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/transport"
)

// switchAgentToModelChoice finds a working route to the chosen model, then
// applies it. Routing happens here, for the one model the operator picked,
// rather than probing every model when the list is drawn. The previous
// model's tunnel is closed only once the switch has succeeded; a failed
// switch closes the new tunnel instead and leaves the current model working.
func switchAgentToModelChoice(session *agentREPLSession, choice ModelChoice) error {
	var rt *runtimectx.Context
	if session.Runtime != nil {
		rt, _ = session.Runtime(context.Background()) // nil runtime: route falls back to the listed endpoint
	}
	routed, tunnel, err := resolveModelRoute(context.Background(), rt, choice)
	if err != nil {
		return err
	}
	if err := applyModelChoice(session, routed); err != nil {
		if tunnel != nil {
			tunnel()
		}
		return err
	}
	installModelTunnel(tunnel)
	return nil
}

var routeProbeFn = func(url string) bool { return probeEndpointFn(url) }

// openModelTunnelFn forwards a local port to remotePort on the node's own
// loopback over SSH, using the node's configured dial spec.
var openModelTunnelFn = func(ctx context.Context, node config.NodeConfig, remotePort int) (int, func(), error) {
	spec := node.SSHDialSpec()
	exec := transport.NewSSHExecutorFromDial(spec.Host, spec.Port, spec.User, spec.DialTimeoutSec, spec.Fallbacks)
	if err := exec.Connect(ctx); err != nil {
		return 0, nil, err
	}
	// The tunnel outlives this call; installModelTunnel owns its lifetime.
	port, stop, err := exec.ForwardLocal(context.Background(), 0, remotePort)
	if err != nil {
		_ = exec.Close()
		return 0, nil, err
	}
	return port, func() { stop(); _ = exec.Close() }, nil
}

var activeModelTunnel struct {
	mu    sync.Mutex
	close func()
}

// installModelTunnel makes closeFn the active model's tunnel and closes the
// previous one. A nil closeFn means the active model needs no tunnel, so any
// previous tunnel is simply closed. One model tunnel is open at a time.
func installModelTunnel(closeFn func()) {
	activeModelTunnel.mu.Lock()
	previous := activeModelTunnel.close
	activeModelTunnel.close = closeFn
	activeModelTunnel.mu.Unlock()
	if previous != nil {
		previous()
	}
}

// resolveModelRoute returns choice with an Endpoint that answers, plus the
// close func of a tunnel it opened (nil when none). It does not install or
// close any tunnel; the caller does that once the switch succeeds.
//
// For a model on another node it tries, in order: the listed address, the
// node name (resolvable via DNS or the tailnet), and finally an SSH tunnel
// over the route the fact plane uses for that node, which also reaches
// services bound only to the node's loopback. Local, cloud, and ai.yaml
// choices are returned unchanged.
func resolveModelRoute(ctx context.Context, rt *runtimectx.Context, choice ModelChoice) (ModelChoice, func(), error) {
	if choice.ProviderKind != "local" || choice.Node == "" || choice.Port <= 0 {
		return choice, nil, nil
	}
	probePath := "/v1/models"
	if choice.Protocol == agent.ProtocolOllama {
		probePath = "/api/tags"
	}

	var tried []string
	for _, base := range directRouteCandidates(rt, choice) {
		tried = append(tried, base)
		if routeProbeFn(base + probePath) {
			choice.Endpoint = base
			return choice, nil, nil
		}
	}

	node, ok := config.NodeConfig{}, false
	if rt != nil && rt.Config != nil {
		node, ok = rt.Config.FindNode(choice.Node)
	}
	if !ok {
		return choice, nil, fmt.Errorf("model %q on node %q is not reachable: tried %s; no SSH config for the node to tunnel through", choice.Model, choice.Node, strings.Join(tried, ", "))
	}
	port, closeFn, err := openModelTunnelFn(ctx, node, choice.Port)
	if err != nil {
		return choice, nil, fmt.Errorf("model %q on node %q is not reachable: tried %s; ssh tunnel: %w", choice.Model, choice.Node, strings.Join(tried, ", "), err)
	}
	// interlinked-ignore: ubs_hardcoded_localhost — ForwardLocal binds the tunnel listener to loopback only
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !routeProbeFn(base + probePath) {
		closeFn()
		return choice, nil, fmt.Errorf("model %q on node %q is not reachable: tried %s; ssh tunnel opened but %s did not answer", choice.Model, choice.Node, strings.Join(tried, ", "), base+probePath)
	}
	choice.Endpoint = base
	return choice, closeFn, nil
}

// directRouteCandidates lists base URLs to try without SSH: the listed
// endpoint, then the node name and its observed hostname.
func directRouteCandidates(rt *runtimectx.Context, choice ModelChoice) []string {
	seen := map[string]bool{}
	var out []string
	add := func(base string) {
		if base != "" && !seen[base] {
			seen[base] = true
			out = append(out, base)
		}
	}
	add(strings.TrimRight(choice.Endpoint, "/"))
	add(fmt.Sprintf("http://%s:%d", choice.Node, choice.Port))
	if rt != nil && rt.Snapshot != nil {
		for _, n := range rt.Snapshot.Nodes {
			if n.Name == choice.Node && n.Hostname != "" && !strings.ContainsAny(n.Hostname, " /") {
				add(fmt.Sprintf("http://%s:%d", n.Hostname, choice.Port))
			}
		}
	}
	return out
}
