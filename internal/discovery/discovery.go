// Package discovery enumerates configured nodes and collects facts.
package discovery

import (
	"context"
	"sync"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/facts"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/transport"
)

// Discover probes all configured nodes concurrently and returns their facts.
// Local node is detected by hostname match — uses LocalCollector.
// Remote nodes use SSH-based RemoteCollector.
// Never fails hard — unreachable nodes return StatusUnreachable.
func Discover(ctx context.Context, cfg *config.Config) []models.NodeFacts {
	discovered := make(map[string]config.NodeConfig)
	var mu sync.Mutex

	// Prefill with static config
	for _, n := range cfg.Nodes {
		discovered[n.Name] = n
	}

	if cfg.Discovery != nil && cfg.Discovery.Enabled {
		startUDP(ctx, cfg, discovered, &mu)
		// Wait 8 seconds to accumulate beacons before proceeding to SSH collection.
		time.Sleep(8 * time.Second)
	}

	mu.Lock()
	var finalNodes []config.NodeConfig
	for _, nc := range discovered {
		finalNodes = append(finalNodes, nc)
	}
	mu.Unlock()

	results := make([]models.NodeFacts, len(finalNodes))

	var wg sync.WaitGroup
	for i, node := range finalNodes {
		wg.Add(1)
		go func(idx int, nc config.NodeConfig) {
			defer wg.Done()
			
			nodeCtx, cancel := context.WithTimeout(ctx, time.Duration(nc.EffectiveTimeout())*time.Second)
			defer cancel()

			var collector facts.Collector
			if models.IsLocalConfig(nc.Name, nc.Hostname) {
				collector = facts.NewLocalCollector(nc.Name, nc.Role)
			} else {
				exec := transport.NewSSHExecutor(
					nc.Hostname,
					nc.EffectiveSSHPort(),
					nc.SSHUser,
					nc.EffectiveTimeout(),
				)
				collector = facts.NewRemoteCollector(nc.Name, nc.Role, nc.Hostname, exec)
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

