package discovery

import (
	"strings"
	"sync"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

const beaconTTL = 30 * time.Second

type beaconEntry struct {
	node      config.NodeConfig
	expiresAt time.Time
}

// BeaconRegistry tracks UDP-discovered nodes from live beacons so a long-lived
// daemon can merge them into later refreshes without reopening the beacon
// listener on every snapshot build.
type BeaconRegistry struct {
	mu    sync.RWMutex
	nodes map[string]beaconEntry
}

func NewBeaconRegistry() *BeaconRegistry {
	return &BeaconRegistry{
		nodes: make(map[string]beaconEntry),
	}
}

func (r *BeaconRegistry) Snapshot() []config.NodeConfig {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	discovered := make(map[string]config.NodeConfig, len(r.nodes))
	for key, entry := range r.nodes {
		discovered[key] = entry.node
	}
	return orderedNodes(discovered)
}

func (r *BeaconRegistry) Reset() bool {
	if r == nil {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.nodes) == 0 {
		return false
	}
	r.nodes = make(map[string]beaconEntry)
	return true
}

func (r *BeaconRegistry) UpdateFromBeacon(b Beacon) bool {
	if r == nil {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	projected := beaconNodeConfig(b)
	key := beaconRegistryKey(r.nodes, projected.Name, projected.StableID)
	entry, exists := r.nodes[key]
	changed := !exists || !sameNodeConfig(entry.node, projected)

	r.nodes[key] = beaconEntry{
		node:      projected,
		expiresAt: time.Now().UTC().Add(beaconTTL),
	}
	return changed
}

func (r *BeaconRegistry) PruneExpired(now time.Time) bool {
	if r == nil {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	pruned := false
	for key, entry := range r.nodes {
		if now.After(entry.expiresAt) {
			delete(r.nodes, key)
			pruned = true
		}
	}
	return pruned
}

func beaconNodeConfig(b Beacon) config.NodeConfig {
	return config.NodeConfig{
		Name:       strings.TrimSpace(b.Name),
		Hostname:   strings.TrimSpace(b.IP),
		StableID:   models.NormalizeStableID(b.StableID),
		SSHUser:    "axis",
		Role:       strings.TrimSpace(b.Role),
		SSHPort:    b.SSHPort,
		TimeoutSec: 10,
	}
}

func beaconRegistryKey(nodes map[string]beaconEntry, name, stableID string) string {
	normalizedStableID := models.NormalizeStableID(stableID)
	if normalizedStableID != "" {
		for key, entry := range nodes {
			if models.NormalizeStableID(entry.node.StableID) == normalizedStableID {
				return key
			}
		}
	}

	trimmedName := strings.TrimSpace(name)
	if trimmedName != "" {
		for key, entry := range nodes {
			if strings.EqualFold(entry.node.Name, trimmedName) {
				return key
			}
		}
	}

	if normalizedStableID != "" {
		return "id:" + normalizedStableID
	}
	return "name:" + strings.ToLower(trimmedName)
}

func sameNodeConfig(a, b config.NodeConfig) bool {
	return a.Name == b.Name &&
		a.Hostname == b.Hostname &&
		a.StableID == b.StableID &&
		a.SSHUser == b.SSHUser &&
		a.Role == b.Role &&
		a.SSHPort == b.SSHPort &&
		a.TimeoutSec == b.TimeoutSec
}
