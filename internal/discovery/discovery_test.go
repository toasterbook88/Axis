package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/facts"
	"github.com/toasterbook88/axis/internal/models"
)

func TestOrderedNodesSortsDeterministically(t *testing.T) {
	discovered := map[string]config.NodeConfig{
		"zeta": {
			Name:     "zeta",
			Hostname: "10.0.0.9",
			SSHPort:  22,
		},
		"alpha": {
			Name:     "alpha",
			Hostname: "10.0.0.2",
			SSHPort:  2222,
		},
		"beta": {
			Name:     "beta",
			Hostname: "10.0.0.3",
			SSHPort:  22,
		},
	}

	nodes := orderedNodes(discovered)

	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(nodes))
	}

	if nodes[0].Name != "alpha" || nodes[1].Name != "beta" || nodes[2].Name != "zeta" {
		t.Fatalf("unexpected node order: %#v", nodes)
	}
}

func TestOrderedNodesBreaksTiesByHostnameThenPort(t *testing.T) {
	discovered := map[string]config.NodeConfig{
		"same-a": {
			Name:     "same",
			Hostname: "10.0.0.9",
			SSHPort:  2200,
		},
		"same-b": {
			Name:     "same",
			Hostname: "10.0.0.8",
			SSHPort:  2300,
		},
		"same-c": {
			Name:     "same",
			Hostname: "10.0.0.8",
			SSHPort:  2200,
		},
	}

	nodes := orderedNodes(discovered)

	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(nodes))
	}

	if nodes[0].Hostname != "10.0.0.8" || nodes[0].EffectiveSSHPort() != 2200 {
		t.Fatalf("expected first node to have lowest hostname/port tie-break, got %#v", nodes[0])
	}
	if nodes[1].Hostname != "10.0.0.8" || nodes[1].EffectiveSSHPort() != 2300 {
		t.Fatalf("expected second node to retain hostname tie-break with higher port, got %#v", nodes[1])
	}
	if nodes[2].Hostname != "10.0.0.9" {
		t.Fatalf("expected final node to have highest hostname, got %#v", nodes[2])
	}
}

func TestWaitForBeaconWindowReturnsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	waitForBeaconWindow(ctx, 5*time.Second)

	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("expected immediate return on canceled context, got %s", elapsed)
	}
}

func TestWaitForBeaconWindowHonorsDuration(t *testing.T) {
	start := time.Now()
	waitForBeaconWindow(context.Background(), 20*time.Millisecond)

	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Fatalf("expected wait to honor duration, got %s", elapsed)
	}
}

func TestDiscoverCollectsLocalAndRemoteNodesInStableOrder(t *testing.T) {
	restoreLocal := stubLocalDiscoveryCollector(t, func(name, role string) facts.Collector {
		return collectorFunc(func(context.Context) (*models.NodeFacts, error) {
			return &models.NodeFacts{Name: name, Role: role, Status: models.StatusComplete}, nil
		})
	})
	defer restoreLocal()
	restoreRemote := stubRemoteDiscoveryCollector(t, func(nc config.NodeConfig) facts.Collector {
		return collectorFunc(func(context.Context) (*models.NodeFacts, error) {
			return &models.NodeFacts{Name: nc.Name, Role: nc.Role, Hostname: nc.Hostname, Status: models.StatusComplete}, nil
		})
	})
	defer restoreRemote()
	restoreUDP := stubDiscoveryStartUDP(t, func(context.Context, *config.Config, map[string]config.NodeConfig, *sync.Mutex) {})
	defer restoreUDP()
	restoreWait := stubDiscoveryBeaconWait(t, func(context.Context, time.Duration) {})
	defer restoreWait()

	nodes := Discover(context.Background(), &config.Config{
		Nodes: []config.NodeConfig{
			{Name: "remote-b", Hostname: "10.0.0.2", Role: "gpu", SSHUser: "axis"},
			{Name: "local-a", Hostname: "localhost", Role: "laptop", SSHUser: "axis"},
		},
	})

	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
	if nodes[0].Name != "local-a" || nodes[1].Name != "remote-b" {
		t.Fatalf("expected stable ordered results, got %#v", nodes)
	}
}

func TestDiscoverWrapsCollectorFailuresAsErrorNodes(t *testing.T) {
	restoreLocal := stubLocalDiscoveryCollector(t, func(name, role string) facts.Collector {
		return collectorFunc(func(context.Context) (*models.NodeFacts, error) {
			return nil, errors.New("local boom")
		})
	})
	defer restoreLocal()
	restoreRemote := stubRemoteDiscoveryCollector(t, func(nc config.NodeConfig) facts.Collector {
		return collectorFunc(func(context.Context) (*models.NodeFacts, error) {
			return nil, errors.New("remote boom")
		})
	})
	defer restoreRemote()
	restoreUDP := stubDiscoveryStartUDP(t, func(context.Context, *config.Config, map[string]config.NodeConfig, *sync.Mutex) {})
	defer restoreUDP()
	restoreWait := stubDiscoveryBeaconWait(t, func(context.Context, time.Duration) {})
	defer restoreWait()

	nodes := Discover(context.Background(), &config.Config{
		Nodes: []config.NodeConfig{
			{Name: "local-a", Hostname: "localhost", Role: "laptop", SSHUser: "axis"},
			{Name: "remote-b", Hostname: "10.0.0.2", Role: "gpu", SSHUser: "axis"},
		},
	})

	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
	for _, node := range nodes {
		if node.Status != models.StatusError {
			t.Fatalf("expected wrapped error status, got %#v", nodes)
		}
		if node.Error == "" {
			t.Fatalf("expected error message on wrapped node, got %#v", node)
		}
	}
}

func TestStartUDPAddsBeaconDiscoveredNode(t *testing.T) {
	port := freeUDPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	discovered := map[string]config.NodeConfig{}
	var mu sync.Mutex

	startUDP(ctx, &config.Config{
		Discovery: &config.DiscoveryConfig{
			Enabled:        true,
			UDPPort:        port,
			BeaconInterval: 60,
			Secret:         "shared",
		},
	}, discovered, &mu)

	sendBeacon(t, port, Beacon{
		Type:      "axis",
		Name:      "beacon-node",
		IP:        "10.0.0.9",
		SSHPort:   2200,
		Role:      "worker",
		Timestamp: time.Now().UTC(),
		Secret:    "shared",
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		node, ok := discovered["beacon-node"]
		mu.Unlock()
		if ok {
			if node.Hostname != "10.0.0.9" || node.SSHPort != 2200 || node.SSHUser != "axis" {
				t.Fatalf("unexpected discovered node: %#v", node)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}

	t.Fatal("expected beacon node to be discovered")
}

func TestStartUDPIgnoresSecretMismatchAndStaleBeacons(t *testing.T) {
	port := freeUDPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	discovered := map[string]config.NodeConfig{}
	var mu sync.Mutex

	startUDP(ctx, &config.Config{
		Discovery: &config.DiscoveryConfig{
			Enabled:        true,
			UDPPort:        port,
			BeaconInterval: 60,
			Secret:         "shared",
		},
	}, discovered, &mu)

	sendBeacon(t, port, Beacon{
		Type:      "axis",
		Name:      "wrong-secret",
		IP:        "10.0.0.9",
		Timestamp: time.Now().UTC(),
		Secret:    "nope",
	})
	sendBeacon(t, port, Beacon{
		Type:      "axis",
		Name:      "stale-node",
		IP:        "10.0.0.10",
		Timestamp: time.Now().UTC().Add(-31 * time.Second),
		Secret:    "shared",
	})

	time.Sleep(150 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(discovered) != 0 {
		t.Fatalf("expected invalid beacons to be ignored, got %#v", discovered)
	}
}

func TestStartUDPDoesNotClobberStaticNode(t *testing.T) {
	port := freeUDPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	discovered := map[string]config.NodeConfig{
		"static-node": {Name: "static-node", Hostname: "192.168.1.10", SSHUser: "me", SSHPort: 2222},
	}
	var mu sync.Mutex

	startUDP(ctx, &config.Config{
		Discovery: &config.DiscoveryConfig{
			Enabled:        true,
			UDPPort:        port,
			BeaconInterval: 60,
		},
	}, discovered, &mu)

	sendBeacon(t, port, Beacon{
		Type:      "axis",
		Name:      "static-node",
		IP:        "10.0.0.99",
		SSHPort:   22,
		Timestamp: time.Now().UTC(),
	})

	time.Sleep(150 * time.Millisecond)

	mu.Lock()
	node := discovered["static-node"]
	mu.Unlock()
	if node.Hostname != "192.168.1.10" || node.SSHUser != "me" || node.SSHPort != 2222 {
		t.Fatalf("expected static node to remain untouched, got %#v", node)
	}
}

func TestLocalIPFallsBackWhenInterfaceLookupFails(t *testing.T) {
	restore := stubInterfaceAddrs(t, func() ([]net.Addr, error) {
		return nil, errors.New("boom")
	})
	defer restore()

	if got := localIP(); got != "127.0.0.1" {
		t.Fatalf("localIP() = %q, want 127.0.0.1", got)
	}
}

func TestLocalIPReturnsFirstNonLoopbackIPv4(t *testing.T) {
	restore := stubInterfaceAddrs(t, func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
			&net.IPNet{IP: net.ParseIP("10.1.2.3"), Mask: net.CIDRMask(24, 32)},
		}, nil
	})
	defer restore()

	if got := localIP(); got != "10.1.2.3" {
		t.Fatalf("localIP() = %q, want 10.1.2.3", got)
	}
}

type collectorFunc func(context.Context) (*models.NodeFacts, error)

func (f collectorFunc) Collect(ctx context.Context) (*models.NodeFacts, error) {
	return f(ctx)
}

func stubDiscoveryStartUDP(t *testing.T, fn func(context.Context, *config.Config, map[string]config.NodeConfig, *sync.Mutex)) func() {
	t.Helper()
	prev := discoveryStartUDP
	discoveryStartUDP = fn
	return func() {
		discoveryStartUDP = prev
	}
}

func stubDiscoveryBeaconWait(t *testing.T, fn func(context.Context, time.Duration)) func() {
	t.Helper()
	prev := discoveryBeaconWait
	discoveryBeaconWait = fn
	return func() {
		discoveryBeaconWait = prev
	}
}

func stubLocalDiscoveryCollector(t *testing.T, fn func(string, string) facts.Collector) func() {
	t.Helper()
	prev := newLocalDiscoveryCollector
	newLocalDiscoveryCollector = fn
	return func() {
		newLocalDiscoveryCollector = prev
	}
}

func stubRemoteDiscoveryCollector(t *testing.T, fn func(config.NodeConfig) facts.Collector) func() {
	t.Helper()
	prev := newRemoteDiscoveryCollector
	newRemoteDiscoveryCollector = fn
	return func() {
		newRemoteDiscoveryCollector = prev
	}
}

func stubInterfaceAddrs(t *testing.T, fn func() ([]net.Addr, error)) func() {
	t.Helper()
	prev := interfaceAddrs
	interfaceAddrs = fn
	return func() {
		interfaceAddrs = prev
	}
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer pc.Close()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

func sendBeacon(t *testing.T, port int, beacon Beacon) {
	t.Helper()
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer conn.Close()

	data, err := json.Marshal(beacon)
	if err != nil {
		t.Fatalf("Marshal beacon: %v", err)
	}
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("Write beacon: %v", err)
	}
}
