// Package discovery is STABLE — configured-node fan-out and optional UDP beacon discovery.
// It is part of the stable operator path.
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

type Result struct {
	Nodes     []models.NodeFacts
	Warnings  []models.Warning
	Freshness *models.DiscoveryFreshness
}

func Discover(ctx context.Context, cfg *config.Config) []models.NodeFacts {
	return DiscoverResult(ctx, cfg).Nodes
}

// DiscoverWithWarnings probes all configured nodes and returns any discovery
// warnings that should be surfaced to operators.
func DiscoverWithWarnings(ctx context.Context, cfg *config.Config) ([]models.NodeFacts, []models.Warning) {
	result := DiscoverResult(ctx, cfg)
	return result.Nodes, result.Warnings
}

// DiscoverResult probes configured nodes and returns the additive structured
// discovery metadata alongside the observed node set.
func DiscoverResult(ctx context.Context, cfg *config.Config) Result {
	return discover(ctx, cfg, nil, true)
}

// DiscoverSeeded probes configured nodes plus long-lived beacon-discovered
// nodes supplied by the caller. Unlike Discover, it does not reopen the UDP
// listener or wait for a fresh beacon accumulation window.
func DiscoverSeeded(ctx context.Context, cfg *config.Config, seeded []config.NodeConfig) []models.NodeFacts {
	return DiscoverSeededResult(ctx, cfg, seeded).Nodes
}

// DiscoverSeededResult probes configured nodes plus long-lived beacon-derived
// nodes supplied by the caller without opening a new UDP accumulation window.
func DiscoverSeededResult(ctx context.Context, cfg *config.Config, seeded []config.NodeConfig) Result {
	return discover(ctx, cfg, seeded, false)
}

// BuildFreshness applies the shared discovery freshness policy used by both
// live UDP discovery windows and long-lived daemon beacon watchers.
func BuildFreshness(source string, expected, observed time.Duration, seededCount, beaconCount int, completed bool) *models.DiscoveryFreshness {
	if expected < 0 {
		expected = 0
	}
	if observed < 0 {
		observed = 0
	}
	if completed && expected > 0 && observed < expected {
		observed = expected
	}
	if expected > 0 && observed > expected {
		observed = expected
	}

	freshness := &models.DiscoveryFreshness{
		Source:           source,
		ExpectedWindowMS: expected.Milliseconds(),
		ObservedWindowMS: observed.Milliseconds(),
		SeededNodeCount:  seededCount,
		BeaconNodeCount:  beaconCount,
		CompletedWindow:  completed,
	}
	if freshness.Warning == "" && expected > 0 && !completed {
		freshness.Warning = fmt.Sprintf("discovery source %s observed only %s of expected %s; results may miss peer nodes",
			source,
			observed.Round(time.Millisecond),
			expected.Round(time.Millisecond),
		)
	}
	return freshness
}

func discover(ctx context.Context, cfg *config.Config, seeded []config.NodeConfig, includeUDP bool) Result {
	discovered := make(map[string]config.NodeConfig)
	var mu sync.Mutex
	result := Result{}
	seededCount := 0
	if cfg != nil {
		seededCount = len(cfg.Nodes)
	}

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
		wait := beaconWaitDuration(cfg)
		windowStart := time.Now().UTC()
		discoveryStartUDP(ctx, cfg, discovered, &mu)
		completed := discoveryBeaconWait(ctx, wait)
		observed := time.Since(windowStart)
		result.Freshness = BuildFreshness("udp-window", wait, observed, seededCount, 0, completed)
		if !completed && result.Freshness != nil && result.Freshness.Warning != "" {
			result.Warnings = append(result.Warnings, models.Warning{
				Kind:    "discovery",
				Message: result.Freshness.Warning,
			})
		}
	} else {
		// Use a distinct source when the caller supplied beacon-registry nodes
		// so the freshness contract accurately reflects that the node set
		// includes daemon-tracked beacon nodes and not just nodes.yaml contents.
		// seeded is nil for plain DiscoverResult calls without a UDP window.
		source := "static-config"
		if len(seeded) > 0 {
			source = "beacon-registry"
		}
		result.Freshness = BuildFreshness(source, 0, 0, seededCount, 0, true)
	}

	mu.Lock()
	finalNodes := orderedNodes(discovered)
	mu.Unlock()
	if result.Freshness != nil {
		additive := len(finalNodes) - seededCount
		if additive < 0 {
			additive = 0
		}
		result.Freshness.BeaconNodeCount = additive
	}

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

			// Assign Epistemic identity
			if nf.Epistemic == nil {
				verifiedBy := models.VerifiedByMesh
				// If a node has a Role or was defined statically in config, it's configured
				if nc.Role != "" {
					verifiedBy = models.VerifiedByConfig
				} else if cfg != nil {
					for _, c := range cfg.Nodes {
						if c.Name == nc.Name {
							verifiedBy = models.VerifiedByConfig
							break
						}
					}
				}

				nf.Epistemic = &models.EpistemicState{
					Source:     models.SourceLiveProbe,
					VerifiedBy: verifiedBy,
					Degraded:   nf.Status != models.StatusComplete,
				}
			}

			// Propagate config-driven per-node system reserve into observed facts.
			nf.SystemReserveMB = nc.SystemReserveMB

			results[idx] = *nf
		}(i, node)
	}

	wg.Wait()
	result.Nodes = results
	return result
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
