// Package discovery enumerates configured nodes and collects facts.
package discovery

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/facts"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/transport"
)

const (
	defaultBeaconInterval = 3 * time.Second
	beaconWaitSlack       = 250 * time.Millisecond
	minBeaconWait         = 1 * time.Second
	maxBeaconWait         = 8 * time.Second
)

var discoveryStartUDP = startUDP
var discoveryBeaconWait = waitForBeaconWindow
var discoveryIsLocalConfig = func(nc config.NodeConfig) bool {
	return models.IsLocalConfig(nc.Name, nc.Hostname, nc.StableID)
}
var newLocalDiscoveryCollector = func(name, role string) facts.Collector {
	return facts.NewLocalCollector(name, role)
}
var newRemoteDiscoveryCollector = func(nc config.NodeConfig) facts.Collector {
	exec := transport.NewSSHExecutor(
		nc.Hostname,
		nc.EffectiveSSHPort(),
		nc.SSHUser,
		nc.EffectiveTimeout(),
	)
	return facts.NewRemoteCollector(nc.Name, nc.Role, nc.Hostname, exec)
}

// Discover probes all configured nodes concurrently and returns their facts.
// Local node is detected by observed stable identity or hostname/address match
// and uses LocalCollector.
// Remote nodes use SSH-based RemoteCollector.
// Never fails hard — unreachable nodes return StatusUnreachable.
// maxParallel is the maximum number of concurrent SSH probes.
// It prevents goroutine storms when the cluster has many nodes.
const maxParallel = 10

func Discover(ctx context.Context, cfg *config.Config) []models.NodeFacts {
	nodes, _ := discover(ctx, cfg, nil, true)
	return nodes
}

// DiscoverWithWarnings probes all configured nodes and returns any discovery
// warnings that should be surfaced to operators.
func DiscoverWithWarnings(ctx context.Context, cfg *config.Config) ([]models.NodeFacts, []models.Warning) {
	return discover(ctx, cfg, nil, true)
}

// DiscoverSeeded probes configured nodes plus long-lived beacon-discovered
// nodes supplied by the caller. Unlike Discover, it does not reopen the UDP
// listener or wait for a fresh beacon accumulation window.
func DiscoverSeeded(ctx context.Context, cfg *config.Config, seeded []config.NodeConfig) []models.NodeFacts {
	nodes, _ := discover(ctx, cfg, seeded, false)
	return nodes
}

func discover(ctx context.Context, cfg *config.Config, seeded []config.NodeConfig, includeUDP bool) ([]models.NodeFacts, []models.Warning) {
	discovered := make(map[string]config.NodeConfig)
	var mu sync.Mutex
	var warnings []models.Warning

	// Prefill with static config
	for _, n := range cfg.Nodes {
		discovered[n.Name] = n
	}
	for _, n := range seeded {
		if key, exists := discoveredNodeKey(discovered, n.Name, n.StableID); exists {
			existing := discovered[key]
			if existing.StableID == "" {
				existing.StableID = n.StableID
				discovered[key] = existing
			}
			continue
		}
		discovered[n.Name] = n
	}

	if includeUDP && cfg.Discovery != nil && cfg.Discovery.Enabled {
		discoveryStartUDP(ctx, cfg, discovered, &mu)
		wait := beaconWaitDuration(cfg)
		if !discoveryBeaconWait(ctx, wait) {
			warnings = append(warnings, models.Warning{
				Kind:    "discovery",
				Message: fmt.Sprintf("discovery beacon window ended early before %s; results may miss peer nodes", wait.Round(time.Millisecond)),
			})
		}
	}

	mu.Lock()
	finalNodes := orderedNodes(discovered)
	mu.Unlock()

	results := make([]models.NodeFacts, len(finalNodes))

	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i, node := range finalNodes {
		wg.Add(1)
		sem <- struct{}{} // acquire semaphore slot
		go func(idx int, nc config.NodeConfig) {
			defer wg.Done()
			defer func() { <-sem }() // release slot

			nodeCtx, cancel := context.WithTimeout(ctx, time.Duration(nc.EffectiveTimeout())*time.Second)
			defer cancel()

			var collector facts.Collector
			if discoveryIsLocalConfig(nc) {
				collector = newLocalDiscoveryCollector(nc.Name, nc.Role)
			} else {
				collector = newRemoteDiscoveryCollector(nc)
			}

			nf, err := collector.Collect(nodeCtx)
			if err != nil {
				// Collector itself failed (should not happen, but guard)
				nf = &models.NodeFacts{
					Name:        nc.Name,
					Role:        nc.Role,
					Status:      models.StatusError,
					Error:       err.Error(),
					CollectedAt: time.Now().UTC(),
				}
			}
			results[idx] = *nf
		}(i, node)
	}

	wg.Wait()
	return results, warnings
}

func beaconWaitDuration(cfg *config.Config) time.Duration {
	interval := defaultBeaconInterval
	if cfg != nil && cfg.Discovery != nil && cfg.Discovery.BeaconInterval > 0 {
		interval = time.Duration(cfg.Discovery.BeaconInterval) * time.Second
	}

	wait := interval + beaconWaitSlack
	if wait < minBeaconWait {
		return minBeaconWait
	}
	if wait > maxBeaconWait {
		return maxBeaconWait
	}
	return wait
}

func waitForBeaconWindow(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func orderedNodes(discovered map[string]config.NodeConfig) []config.NodeConfig {
	finalNodes := make([]config.NodeConfig, 0, len(discovered))
	for _, nc := range discovered {
		finalNodes = append(finalNodes, nc)
	}

	sort.Slice(finalNodes, func(i, j int) bool {
		if finalNodes[i].Name != finalNodes[j].Name {
			return finalNodes[i].Name < finalNodes[j].Name
		}
		if finalNodes[i].Hostname != finalNodes[j].Hostname {
			return finalNodes[i].Hostname < finalNodes[j].Hostname
		}
		if finalNodes[i].EffectiveSSHPort() != finalNodes[j].EffectiveSSHPort() {
			return finalNodes[i].EffectiveSSHPort() < finalNodes[j].EffectiveSSHPort()
		}
		return finalNodes[i].StableID < finalNodes[j].StableID
	})

	return finalNodes
}
