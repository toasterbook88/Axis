package daemon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/mesh"
)

// Mesh transport and self identity are fixed for the lifetime of its listener.
// Peer membership may change without partially applying those settings.
type meshRuntimeSettings struct {
	enabled                    bool
	port                       int
	interval                   time.Duration
	secret                     string
	selfName, selfHost, selfID string
}

func meshSettings(cfg *config.Config) meshRuntimeSettings {
	s := meshRuntimeSettings{enabled: cfg.IsMeshEnabled(), port: 42426, interval: mesh.DefaultConfig().GossipInterval}
	if cfg == nil {
		return s
	}
	if d := cfg.Discovery; d != nil {
		if d.GossipPort > 0 {
			s.port = d.GossipPort
		}
		if d.BeaconInterval > 0 {
			s.interval = time.Duration(d.BeaconInterval) * time.Second
		}
		s.secret = d.Secret
	}
	for _, n := range cfg.Nodes {
		if n.IsLocal() {
			s.selfName, s.selfHost, s.selfID = n.Name, n.PrimaryHostname(), n.StableID
			break
		}
	}
	return s
}

func (d *Daemon) applyMeshConfig(data []byte) error {
	cfg, err := config.Parse(data)
	if err != nil {
		return err
	}
	settings := meshSettings(cfg)
	if settings != d.meshSettings {
		return fmt.Errorf("mesh transport, enablement or local identity changed: restart required")
	}
	beaconPort := 42424
	if cfg.Discovery != nil && cfg.Discovery.UDPPort > 0 {
		beaconPort = cfg.Discovery.UDPPort
	}
	if beaconPort == settings.port {
		return fmt.Errorf("mesh and beacon ports conflict: restart required")
	}
	seeds := make([]mesh.Peer, 0, len(cfg.Nodes))
	for _, n := range cfg.Nodes {
		if n.Name != settings.selfName {
			seeds = append(seeds, meshPeerFromNode(n, settings.port))
		}
	}
	return d.mesh.ReconcileSeeds(seeds)
}

func (d *Daemon) watchMeshConfig(ctx context.Context, path string) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		var observed, applied configFingerprint
		var observedSet, appliedSet, unreadable bool
		ticker := time.NewTicker(watchConfigPollInterval)
		defer ticker.Stop()
		for {
			// Hash and parse the same bytes: a second read can apply version B
			// under A's fingerprint and then incorrectly skip a later return to A.
			data, err := os.ReadFile(path)
			fingerprint := configFingerprint{exists: err == nil, sum: sha256.Sum256(data)}
			refresh := false
			if err != nil {
				refresh = !unreadable
				unreadable = true
			} else {
				refresh = unreadable || !observedSet || fingerprint != observed
				observed, observedSet, unreadable = fingerprint, true, false
				if !appliedSet || fingerprint != applied {
					err = d.applyMeshConfig(data)
					if err == nil {
						applied, appliedSet = fingerprint, true
						refresh = true
					}
				}
			}
			// Rejected versions still invalidate snapshot truth once per change;
			// retrying mesh reconciliation must not repeatedly refresh that cache.
			if refresh {
				d.Invalidate()
				_ = d.refreshWithTrigger(ctx, RefreshTriggerConfigChange)
			}
			if err != nil {
				reportWatchFingerprintError(path, RefreshTriggerConfigChange, fmt.Errorf("mesh config not applied; keeping last valid peers: %w", err))
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
