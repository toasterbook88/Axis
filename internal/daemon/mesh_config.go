package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
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

// errMeshRestartRequired marks a config version that can only take effect
// after the daemon restarts.
var errMeshRestartRequired = errors.New("restart required")

func (d *Daemon) applyMeshConfig(data []byte) (mesh.ReconcileCounts, error) {
	cfg, err := config.Parse(data)
	if err != nil {
		return mesh.ReconcileCounts{}, err
	}
	settings := meshSettings(cfg)
	// A port collision is a config error, not a pending restart: a daemon
	// started on this config disables the mesh (NewDefault), so it must not
	// carry errMeshRestartRequired or status/doctor would advise a restart.
	// Check it first so a transport change that also collides reports the
	// collision.
	beaconPort := 42424
	if cfg.Discovery != nil && cfg.Discovery.UDPPort > 0 {
		beaconPort = cfg.Discovery.UDPPort
	}
	if beaconPort == settings.port {
		return mesh.ReconcileCounts{}, fmt.Errorf("mesh and beacon ports conflict on %d; set distinct discovery.gossip_port and discovery.udp_port", beaconPort)
	}
	if settings != d.meshSettings {
		return mesh.ReconcileCounts{}, fmt.Errorf("mesh transport, enablement or local identity changed: %w", errMeshRestartRequired)
	}
	seeds := make([]mesh.Peer, 0, len(cfg.Nodes))
	for _, n := range cfg.Nodes {
		if n.Name != settings.selfName {
			seeds = append(seeds, meshPeerFromNode(n, settings.port))
		}
	}
	return d.mesh.ReconcileSeeds(seeds)
}

// setMeshRestartRequired records whether the last evaluated readable config
// version was refused because it needs a restart; any other outcome clears it.
func (d *Daemon) setMeshRestartRequired(err error) {
	if errors.Is(err, errMeshRestartRequired) {
		reason := err.Error()
		d.meshRestartReason.Store(&reason)
		return
	}
	d.meshRestartReason.Store(nil)
}

func (d *Daemon) watchMeshConfig(ctx context.Context, path string) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		var observed, applied, reported configFingerprint
		var observedSet, appliedSet, reportedSet, unreadable bool
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
					var counts mesh.ReconcileCounts
					counts, err = d.applyMeshConfig(data)
					if err == nil {
						// The first apply at watcher start usually repeats the
						// startup seeds; log it only when it changed something.
						if appliedSet || counts != (mesh.ReconcileCounts{}) {
							slog.Info("daemon: mesh config applied", "path", path, "added", counts.Added, "updated", counts.Updated, "removed", counts.Removed)
						}
						applied, appliedSet = fingerprint, true
						refresh = true
					}
				}
				d.setMeshRestartRequired(err)
			}
			// Rejected versions still invalidate snapshot truth once per change;
			// retrying mesh reconciliation must not repeatedly refresh that cache.
			if refresh {
				d.Invalidate()
				_ = d.refreshWithTrigger(ctx, RefreshTriggerConfigChange)
			}
			// The apply is retried every tick, but a version is reported once:
			// the same bytes fail the same way until the file changes.
			if err == nil {
				reportedSet = false
			} else if !reportedSet || fingerprint != reported {
				reported, reportedSet = fingerprint, true
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
