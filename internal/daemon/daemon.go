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

func (d *Daemon) Meta() Metadata {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return Metadata{
		Source:             "daemon-cache",
		Ready:              d.snapshot != nil,
		RefreshIntervalSec: int(d.interval / time.Second),
		CollectedAt:        d.collectedAt,
		NextRefreshAt:      d.nextRefreshAt,
		LastError:          d.lastError,
		SnapshotPath:       d.snapshotPath,
	}
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
