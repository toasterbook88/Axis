package mesh

import (
	"fmt"
	"strings"
	"time"
)

// ReconcileCounts reports how a ReconcileSeeds call changed configured peers.
type ReconcileCounts struct {
	Added, Updated, Removed int
}

// ReconcileSeeds atomically replaces configured peers while preserving unrelated
// learned peers. Unchanged peers retain observations; changed endpoints or
// identities start with no reachability observations.
func (m *Mesh) ReconcileSeeds(seeds []Peer) (ReconcileCounts, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make(map[string]bool, len(seeds))
	names := make(map[string]bool, len(seeds))
	for _, p := range seeds {
		key, name := m.peerKey(p), strings.ToLower(p.Name)
		if name == "" || p.Hostname == "" || p.Port <= 0 || p.Port > 65535 {
			return ReconcileCounts{}, fmt.Errorf("mesh: invalid configured peer %q", p.Name)
		}
		if keys[key] || names[name] || (m.self.Name != "" && strings.EqualFold(p.Name, m.self.Name)) || (m.self.StableID != "" && p.StableID == m.self.StableID) {
			return ReconcileCounts{}, fmt.Errorf("mesh: conflicting configured identity %q", p.Name)
		}
		keys[key], names[name] = true, true
	}
	next := make(map[string]*Peer, len(m.peers)+len(seeds))
	for key, p := range m.peers {
		if p.Source != "config" && !keys[key] && !names[strings.ToLower(p.Name)] {
			next[key] = p
		}
	}
	var counts ReconcileCounts
	oldConfigNames := make(map[string]bool)
	for _, p := range m.peers {
		if p.Source == "config" {
			oldConfigNames[strings.ToLower(p.Name)] = true
		}
	}
	now := time.Now()
	for _, p := range seeds {
		key := m.peerKey(p)
		if old, ok := m.peers[key]; ok && old.Source == "config" && old.Name == p.Name && old.StableID == p.StableID && old.Hostname == p.Hostname && old.Port == p.Port {
			next[key] = old
			continue
		}
		if oldConfigNames[strings.ToLower(p.Name)] {
			counts.Updated++
		} else {
			counts.Added++
		}
		p.Source, p.State = "config", PeerTrusted
		p.FirstSeen, p.LastSeen = now, now
		p.MissedPings, p.Generation = 0, 1
		next[key] = &p
	}
	for name := range oldConfigNames {
		if !names[name] {
			counts.Removed++
		}
	}
	m.peers = next
	return counts, nil
}
