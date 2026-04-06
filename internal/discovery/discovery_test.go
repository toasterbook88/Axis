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

func TestBeaconWaitDurationDefaultsAndCaps(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want time.Duration
	}{
		{
			name: "nil config uses default interval",
			cfg:  nil,
			want: 3250 * time.Millisecond,
		},
		{
			name: "missing discovery uses default interval",
			cfg:  &config.Config{},
			want: 3250 * time.Millisecond,
		},
		{
			name: "configured interval adjusts wait",
			cfg: &config.Config{
				Discovery: &config.DiscoveryConfig{BeaconInterval: 2},
			},
			want: 2250 * time.Millisecond,
		},
		{
			name: "long interval is capped",
			cfg: &config.Config{
				Discovery: &config.DiscoveryConfig{BeaconInterval: 60},
			},
			want: 8 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := beaconWaitDuration(tt.cfg); got != tt.want {
				t.Fatalf("beaconWaitDuration() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestDiscoverCollectsLocalAndRemoteNodesInStableOrder(t *testing.T) {
	restoreMatch := stubDiscoveryIsLocalConfig(t, func(nc config.NodeConfig) bool {
		return nc.Hostname == "localhost"
	})
	defer restoreMatch()
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
	restoreMatch := stubDiscoveryIsLocalConfig(t, func(nc config.NodeConfig) bool {
		return nc.Hostname == "localhost"
	})
	defer restoreMatch()
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

func TestDiscoverUsesStableIdentityAwareLocalMatcher(t *testing.T) {
	restoreMatch := stubDiscoveryIsLocalConfig(t, func(nc config.NodeConfig) bool {
		return nc.StableID == "abc-123"
	})
	defer restoreMatch()
	restoreLocal := stubLocalDiscoveryCollector(t, func(name, role string) facts.Collector {
		return collectorFunc(func(context.Context) (*models.NodeFacts, error) {
			return &models.NodeFacts{Name: name, Hostname: "local-collected", Status: models.StatusComplete}, nil
		})
	})
	defer restoreLocal()
	restoreRemote := stubRemoteDiscoveryCollector(t, func(nc config.NodeConfig) facts.Collector {
		return collectorFunc(func(context.Context) (*models.NodeFacts, error) {
			return &models.NodeFacts{Name: nc.Name, Hostname: "remote-collected", Status: models.StatusComplete}, nil
		})
	})
	defer restoreRemote()
	restoreUDP := stubDiscoveryStartUDP(t, func(context.Context, *config.Config, map[string]config.NodeConfig, *sync.Mutex) {})
	defer restoreUDP()
	restoreWait := stubDiscoveryBeaconWait(t, func(context.Context, time.Duration) {})
	defer restoreWait()

	nodes := Discover(context.Background(), &config.Config{
		Nodes: []config.NodeConfig{
			{Name: "local-a", Hostname: "198.51.100.7", StableID: "abc-123", Role: "laptop", SSHUser: "axis"},
		},
	})

	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
	if nodes[0].Hostname != "local-collected" {
		t.Fatalf("expected stable identity match to use local collector, got %#v", nodes[0])
	}
}

func TestDiscoverUsesAdaptiveBeaconWindowFromConfig(t *testing.T) {
	restoreUDP := stubDiscoveryStartUDP(t, func(context.Context, *config.Config, map[string]config.NodeConfig, *sync.Mutex) {})
	defer restoreUDP()

	var waited time.Duration
	restoreWait := stubDiscoveryBeaconWait(t, func(_ context.Context, d time.Duration) {
		waited = d
	})
	defer restoreWait()

	Discover(context.Background(), &config.Config{
		Discovery: &config.DiscoveryConfig{
			Enabled:        true,
			BeaconInterval: 2,
		},
	})

	if waited != 2250*time.Millisecond {
		t.Fatalf("Discover() waited %s, want 2250ms", waited)
	}
}

func TestDiscoverSeededSkipsUDPWindowAndIncludesSeededNodes(t *testing.T) {
	restoreMatch := stubDiscoveryIsLocalConfig(t, func(config.NodeConfig) bool { return false })
	defer restoreMatch()
	restoreRemote := stubRemoteDiscoveryCollector(t, func(nc config.NodeConfig) facts.Collector {
		return collectorFunc(func(context.Context) (*models.NodeFacts, error) {
			return &models.NodeFacts{Name: nc.Name, Hostname: nc.Hostname, Status: models.StatusComplete}, nil
		})
	})
	defer restoreRemote()

	var udpCalls, waitCalls int
	restoreUDP := stubDiscoveryStartUDP(t, func(context.Context, *config.Config, map[string]config.NodeConfig, *sync.Mutex) {
		udpCalls++
	})
	defer restoreUDP()
	restoreWait := stubDiscoveryBeaconWait(t, func(context.Context, time.Duration) {
		waitCalls++
	})
	defer restoreWait()

	nodes := DiscoverSeeded(context.Background(), &config.Config{
		Nodes: []config.NodeConfig{
			{Name: "static-node", Hostname: "10.0.0.1", SSHUser: "axis"},
		},
		Discovery: &config.DiscoveryConfig{Enabled: true, BeaconInterval: 2},
	}, []config.NodeConfig{
		{Name: "beacon-node", Hostname: "10.0.0.9", SSHUser: "axis"},
	})

	if udpCalls != 0 || waitCalls != 0 {
		t.Fatalf("expected seeded discovery to skip UDP accumulation, got udp=%d wait=%d", udpCalls, waitCalls)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 discovered nodes, got %d", len(nodes))
	}
	if nodes[0].Name != "beacon-node" || nodes[1].Name != "static-node" {
		t.Fatalf("expected seeded node merge in stable order, got %#v", nodes)
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

	b := Beacon{
		Type:      "axis",
		Name:      "beacon-node",
		StableID:  "ABC-123",
		IP:        "10.0.0.9",
		SSHPort:   2200,
		Role:      "worker",
		Timestamp: time.Now().UTC(),
	}
	b.Sig = signBeacon(b, "shared")
	sendBeacon(t, port, b)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		node, ok := discovered["beacon-node"]
		mu.Unlock()
		if ok {
			if node.Hostname != "10.0.0.9" || node.SSHPort != 2200 || node.SSHUser != "axis" || node.StableID != "abc-123" {
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

	wrongSig := Beacon{
		Type:      "axis",
		Name:      "wrong-secret",
		IP:        "10.0.0.9",
		Timestamp: time.Now().UTC(),
	}
	wrongSig.Sig = signBeacon(wrongSig, "nope") // wrong secret → bad HMAC
	sendBeacon(t, port, wrongSig)

	stale := Beacon{
		Type:      "axis",
		Name:      "stale-node",
		IP:        "10.0.0.10",
		Timestamp: time.Now().UTC().Add(-31 * time.Second),
	}
	stale.Sig = signBeacon(stale, "shared")
	sendBeacon(t, port, stale)

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

func TestStartUDPDoesNotDuplicateStableIDBoundNode(t *testing.T) {
	port := freeUDPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	discovered := map[string]config.NodeConfig{
		"static-node": {
			Name:     "static-node",
			Hostname: "192.168.1.10",
			StableID: "abc-123",
			SSHUser:  "me",
			SSHPort:  2222,
		},
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
		Name:      "renamed-node",
		StableID:  "ABC-123",
		IP:        "10.0.0.99",
		SSHPort:   22,
		Timestamp: time.Now().UTC(),
	})

	time.Sleep(150 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(discovered) != 1 {
		t.Fatalf("expected stable-id match to avoid duplicates, got %#v", discovered)
	}
	if _, ok := discovered["renamed-node"]; ok {
		t.Fatalf("expected renamed beacon to merge into existing node, got %#v", discovered)
	}
	node := discovered["static-node"]
	if node.Hostname != "192.168.1.10" || node.SSHUser != "me" || node.SSHPort != 2222 || node.StableID != "abc-123" {
		t.Fatalf("expected static node to remain authoritative, got %#v", node)
	}
}

func TestBeaconRegistryTracksChangesAndPrunesExpired(t *testing.T) {
	registry := NewBeaconRegistry()

	changed := registry.UpdateFromBeacon(Beacon{
		Name:      "beacon-node",
		StableID:  "ABC-123",
		IP:        "10.0.0.9",
		SSHPort:   2200,
		Role:      "worker",
		Timestamp: time.Now().UTC(),
	})
	if !changed {
		t.Fatal("expected first beacon to change registry")
	}

	changed = registry.UpdateFromBeacon(Beacon{
		Name:      "beacon-node",
		StableID:  "abc-123",
		IP:        "10.0.0.9",
		SSHPort:   2200,
		Role:      "worker",
		Timestamp: time.Now().UTC(),
	})
	if changed {
		t.Fatal("expected identical beacon payload to be ignored")
	}

	changed = registry.UpdateFromBeacon(Beacon{
		Name:      "renamed-node",
		StableID:  "abc-123",
		IP:        "10.0.0.11",
		SSHPort:   2200,
		Role:      "worker",
		Timestamp: time.Now().UTC(),
	})
	if !changed {
		t.Fatal("expected changed beacon payload to update registry")
	}

	nodes := registry.Snapshot()
	if len(nodes) != 1 {
		t.Fatalf("expected 1 registry node, got %#v", nodes)
	}
	if nodes[0].Name != "renamed-node" || nodes[0].Hostname != "10.0.0.11" || nodes[0].StableID != "abc-123" {
		t.Fatalf("unexpected registry snapshot: %#v", nodes[0])
	}

	if !registry.PruneExpired(time.Now().UTC().Add(beaconTTL + time.Second)) {
		t.Fatal("expected expired beacon entry to prune")
	}
	if got := registry.Snapshot(); len(got) != 0 {
		t.Fatalf("expected empty registry after prune, got %#v", got)
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

func stubDiscoveryIsLocalConfig(t *testing.T, fn func(config.NodeConfig) bool) func() {
	t.Helper()
	prev := discoveryIsLocalConfig
	discoveryIsLocalConfig = fn
	return func() {
		discoveryIsLocalConfig = prev
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
