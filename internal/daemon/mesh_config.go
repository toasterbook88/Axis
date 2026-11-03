package daemon

import (
	"context"
	"fmt"
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

func (d *Daemon) applyMeshConfig(path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	settings := meshSettings(cfg)
	if settings != d.meshSettings {
		return fmt.Errorf("mesh transport, enablement or local identity changed: restart required")
	}
	if cfg.Discovery != nil && cfg.Discovery.UDPPort == settings.port {
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
		var applied configFingerprint
		ticker := time.NewTicker(watchConfigPollInterval)
		defer ticker.Stop()
		for {
			fingerprint, err := fingerprintFile(path)
			if err == nil && fingerprint != applied {
				err = d.applyMeshConfig(path)
				if err == nil {
					applied = fingerprint
					d.Invalidate()
					_ = d.refreshWithTrigger(ctx, RefreshTriggerConfigChange)
				}
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
