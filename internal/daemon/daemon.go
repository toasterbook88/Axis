package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/snapshot"
	"github.com/toasterbook88/axis/internal/state"
)

const (
	defaultRefreshInterval = time.Minute
	defaultRefreshTimeout  = 60 * time.Second
)

type Collector func(context.Context) (*models.ClusterSnapshot, error)

type Metadata struct {
	Source             string    `json:"source"`
	Ready              bool      `json:"ready"`
	RefreshIntervalSec int       `json:"refresh_interval_sec"`
	CollectedAt        time.Time `json:"collected_at,omitempty"`
	NextRefreshAt      time.Time `json:"next_refresh_at,omitempty"`
	LastError          string    `json:"last_error,omitempty"`
	SnapshotPath       string    `json:"snapshot_path,omitempty"`
	ReservedMB         int64     `json:"reserved_mb,omitempty"`
}

type Daemon struct {
	mu           sync.RWMutex
	collector    Collector
	interval     time.Duration
	snapshotPath string

	snapshot      *models.ClusterSnapshot
	collectedAt   time.Time
	nextRefreshAt time.Time
	lastError     string
}

func (d *Daemon) RefreshNow(ctx context.Context) error {
	return d.Refresh(ctx)
}

func NewDefault(interval time.Duration) *Daemon {
	return New(interval, defaultCollector())
}

func New(interval time.Duration, collector Collector) *Daemon {
	if interval <= 0 {
		interval = defaultRefreshInterval
	}
	if collector == nil {
		collector = defaultCollector()
	}
	return &Daemon{
		collector:    collector,
		interval:     interval,
		snapshotPath: DefaultSnapshotPath(),
	}
}

func DefaultSnapshotPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".axis", "snapshot.json")
}

func (d *Daemon) SetSnapshotPath(path string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.snapshotPath = path
}

func (d *Daemon) Start(ctx context.Context) {
	d.mu.Lock()
	d.nextRefreshAt = time.Now().UTC().Add(d.interval)
	d.mu.Unlock()

	go func() {
		_ = d.Refresh(ctx)

		ticker := time.NewTicker(d.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = d.Refresh(ctx)
			}
		}
	}()
}

func (d *Daemon) Refresh(ctx context.Context) error {
	snap, err := d.collector(ctx)
	now := time.Now().UTC()

	d.mu.Lock()
	defer d.mu.Unlock()

	d.nextRefreshAt = now.Add(d.interval)

	if err != nil {
		d.lastError = err.Error()
		return err
	}

	d.snapshot = cloneSnapshot(snap)
	d.collectedAt = now
	d.lastError = ""

	if d.snapshotPath == "" {
		return nil
	}
	if err := persistSnapshot(d.snapshotPath, snap); err != nil {
		d.lastError = err.Error()
		return err
	}

	// Trigger persisted reservation cleanup on every successful refresh so
	// stale exec state does not survive indefinitely across daemon runs.
	if _, err := state.Load(); err != nil {
		d.lastError = err.Error()
		return err
	}
	return nil
}

func (d *Daemon) Snapshot() (*models.ClusterSnapshot, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.snapshot == nil {
		return nil, false
	}
	return cloneSnapshot(d.snapshot), true
}

func (d *Daemon) Invalidate() {
	d.mu.Lock()
	path := d.snapshotPath
	d.snapshot = nil
	d.collectedAt = time.Time{}
	d.lastError = ""
	d.mu.Unlock()

	if path != "" {
		_ = os.Remove(path)
	}
}

func (d *Daemon) Meta() Metadata {
	d.mu.RLock()
	defer d.mu.RUnlock()
	meta := Metadata{
		Source:             "daemon-cache",
		Ready:              d.snapshot != nil,
		RefreshIntervalSec: int(d.interval / time.Second),
		CollectedAt:        d.collectedAt,
		NextRefreshAt:      d.nextRefreshAt,
		LastError:          d.lastError,
		SnapshotPath:       d.snapshotPath,
	}

	if st, err := state.Load(); err == nil && st != nil {
		for _, ns := range st.Nodes {
			meta.ReservedMB += ns.ReservedMB
		}
	}

	return meta
}

func CanReserve(snap *models.ClusterSnapshot, st *state.ClusterState, node string, mb int64) bool {
	if mb <= 0 || snap == nil || st == nil {
		return true
	}

	var totalRAM int64
	for _, n := range snap.Nodes {
		if n.Name == node && n.Resources != nil {
			totalRAM = n.Resources.RAMTotalMB
			break
		}
	}
	if totalRAM <= 0 {
		return true
	}

	capMB := totalRAM - 1024
	if capMB < 0 {
		capMB = 0
	}

	ns, ok := st.Nodes[node]
	if !ok {
		return mb <= capMB
	}
	return ns.ReservedMB+mb <= capMB
}

func defaultCollector() Collector {
	return func(ctx context.Context) (*models.ClusterSnapshot, error) {
		cfg, err := config.Load(config.DefaultConfigPath())
		if err != nil {
			return nil, err
		}

		runCtx, cancel := context.WithTimeout(ctx, defaultRefreshTimeout)
		defer cancel()

		nodes := discovery.Discover(runCtx, cfg)
		return snapshot.Build(nodes), nil
	}
}

func persistSnapshot(path string, snap *models.ClusterSnapshot) error {
	if snap == nil {
		return fmt.Errorf("nil snapshot")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func cloneSnapshot(snap *models.ClusterSnapshot) *models.ClusterSnapshot {
	if snap == nil {
		return nil
	}
	clone := *snap
	clone.Nodes = append([]models.NodeFacts(nil), snap.Nodes...)
	clone.Warnings = append([]models.Warning(nil), snap.Warnings...)
	return &clone
}
