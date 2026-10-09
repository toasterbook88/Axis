package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func routeTestRuntime() *runtimectx.Context {
	return &runtimectx.Context{
		Snapshot: &models.ClusterSnapshot{Nodes: []models.NodeFacts{
			{Name: "worker", Hostname: "worker-host", Status: models.StatusComplete},
		}},
		Config: &config.Config{Nodes: []config.NodeConfig{{Name: "worker", Hostname: "198.51.100.7", SSHUser: "axis"}}},
	}
}

func remoteOllamaChoice() ModelChoice {
	return ModelChoice{
		ID: "worker:ollama:coder:7b", Model: "coder:7b", Protocol: agent.ProtocolOllama,
		ProviderName: "ollama", ProviderKind: "local", Node: "worker", Port: 11434,
		Endpoint: "http://198.51.100.7:11434", SecurityClass: agent.BackendRemote,
	}
}

type tunnelRecorder struct {
	opened []string
	closed int
	port   int
	err    error
}

func stubModelRoute(t *testing.T, reachable func(url string) bool, tr *tunnelRecorder) {
	t.Helper()
	prevProbe, prevTunnel := routeProbeFn, openModelTunnelFn
	t.Cleanup(func() {
		routeProbeFn, openModelTunnelFn = prevProbe, prevTunnel
		installModelTunnel(nil)
	})
	routeProbeFn = reachable
	openModelTunnelFn = func(_ context.Context, node config.NodeConfig, remotePort int) (int, func(), error) {
		if tr.err != nil {
			return 0, nil, tr.err
		}
		tr.opened = append(tr.opened, node.Name)
		return tr.port, func() { tr.closed++ }, nil
	}
}

func TestResolveModelRouteKeepsReachableDirectEndpoint(t *testing.T) {
	tr := &tunnelRecorder{port: 40001}
	stubModelRoute(t, func(url string) bool { return strings.HasPrefix(url, "http://198.51.100.7:11434") }, tr)
	got, tunnel, err := resolveModelRoute(context.Background(), routeTestRuntime(), remoteOllamaChoice())
	if err != nil || got.Endpoint != "http://198.51.100.7:11434" || len(tr.opened) != 0 || tunnel != nil {
		t.Fatalf("got %q err %v tunnels %v, want direct endpoint and no tunnel", got.Endpoint, err, tr.opened)
	}
}

func TestResolveModelRouteFallsBackToNodeName(t *testing.T) {
	tr := &tunnelRecorder{port: 40001}
	stubModelRoute(t, func(url string) bool { return strings.HasPrefix(url, "http://worker:11434") }, tr)
	got, _, err := resolveModelRoute(context.Background(), routeTestRuntime(), remoteOllamaChoice())
	if err != nil || got.Endpoint != "http://worker:11434" || len(tr.opened) != 0 {
		t.Fatalf("got %q err %v tunnels %v, want node-name endpoint", got.Endpoint, err, tr.opened)
	}
}

// A service bound to the remote loopback is reachable only over SSH, the
// route the fact plane already proved for this node.
func TestResolveModelRouteTunnelsOverSSHWhenNoDirectRoute(t *testing.T) {
	tr := &tunnelRecorder{port: 40001}
	stubModelRoute(t, func(url string) bool { return strings.HasPrefix(url, "http://127.0.0.1:40001") }, tr)
	got, tunnel, err := resolveModelRoute(context.Background(), routeTestRuntime(), remoteOllamaChoice())
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoint != "http://127.0.0.1:40001" || len(tr.opened) != 1 || tr.opened[0] != "worker" || tunnel == nil {
		t.Fatalf("got %q tunnels %v, want SSH tunnel endpoint and its close func", got.Endpoint, tr.opened)
	}
	if got.SecurityClass != agent.BackendRemote || got.Node != "worker" {
		t.Fatalf("tunnelled choice = %+v, must stay a remote choice on worker", got)
	}
	// Resolving does not install or close anything; the caller decides
	// after the switch succeeds.
	if tr.closed != 0 {
		t.Fatalf("closed = %d, want resolve to leave tunnels alone", tr.closed)
	}
}

// Installing a route closes whatever tunnel the previous model used, even
// when the new route needs no tunnel (local, cloud, direct).
func TestInstallModelTunnelClosesPreviousOnEveryInstall(t *testing.T) {
	t.Cleanup(func() { installModelTunnel(nil) })
	closedA, closedB := 0, 0
	installModelTunnel(func() { closedA++ })
	installModelTunnel(func() { closedB++ })
	if closedA != 1 || closedB != 0 {
		t.Fatalf("after second tunnel: closedA=%d closedB=%d, want 1, 0", closedA, closedB)
	}
	installModelTunnel(nil) // switched to a model that needs no tunnel
	if closedB != 1 {
		t.Fatalf("closedB = %d, want the tunnel closed when switching to a direct model", closedB)
	}
}

func TestResolveModelRouteReportsWhatWasTried(t *testing.T) {
	tr := &tunnelRecorder{err: errors.New("ssh: unable to authenticate")}
	stubModelRoute(t, func(string) bool { return false }, tr)
	_, _, err := resolveModelRoute(context.Background(), routeTestRuntime(), remoteOllamaChoice())
	if err == nil {
		t.Fatal("want error when no route works")
	}
	for _, want := range []string{"coder:7b", "worker", "http://198.51.100.7:11434", "http://worker:11434", "ssh: unable to authenticate"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}

func TestResolveModelRouteLeavesLocalAndCloudChoicesAlone(t *testing.T) {
	tr := &tunnelRecorder{port: 40001}
	probed := 0
	stubModelRoute(t, func(string) bool { probed++; return false }, tr)
	for _, c := range []ModelChoice{
		{Model: "here", ProviderKind: "local", Node: "", Endpoint: "http://localhost:11434", Port: 11434},
		{Model: "c", ProviderKind: "cloud", Protocol: agent.ProtocolCloud, Endpoint: "https://api.example.com"},
		{Model: "role", ProviderKind: "local", ProviderName: "ai-backend:hub", Node: "worker", Endpoint: "http://127.0.0.1:4000/v1"},
	} {
		got, _, err := resolveModelRoute(context.Background(), routeTestRuntime(), c)
		if err != nil || got.Endpoint != c.Endpoint {
			t.Fatalf("choice %q changed: %q err %v", c.Model, got.Endpoint, err)
		}
	}
	if probed != 0 || len(tr.opened) != 0 {
		t.Fatalf("probed %d, tunnels %v; want no routing for local/cloud/role choices", probed, tr.opened)
	}
}
