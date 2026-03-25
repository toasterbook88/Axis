package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
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
