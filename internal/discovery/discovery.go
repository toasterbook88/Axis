// Package discovery enumerates configured nodes and collects facts.
package discovery

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/facts"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/transport"
)

const defaultBeaconWait = 8 * time.Second

var discoveryStartUDP = startUDP
var discoveryBeaconWait = waitForBeaconWindow
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
// Local node is detected by hostname match — uses LocalCollector.
// Remote nodes use SSH-based RemoteCollector.
// Never fails hard — unreachable nodes return StatusUnreachable.
// maxParallel is the maximum number of concurrent SSH probes.
// It prevents goroutine storms when the cluster has many nodes.
const maxParallel = 10

func Discover(ctx context.Context, cfg *config.Config) []models.NodeFacts {
	discovered := make(map[string]config.NodeConfig)
	var mu sync.Mutex

	// Prefill with static config
	for _, n := range cfg.Nodes {
		discovered[n.Name] = n
	}

	if cfg.Discovery != nil && cfg.Discovery.Enabled {
		discoveryStartUDP(ctx, cfg, discovered, &mu)
		discoveryBeaconWait(ctx, defaultBeaconWait)
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
			if models.IsLocalConfig(nc.Name, nc.Hostname) {
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
	return results
}

func waitForBeaconWindow(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
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
		return finalNodes[i].EffectiveSSHPort() < finalNodes[j].EffectiveSSHPort()
	})

	return finalNodes
}
