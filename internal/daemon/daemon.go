package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/snapshot"
	"github.com/toasterbook88/axis/internal/snapshotview"
	"github.com/toasterbook88/axis/internal/state"
)

// ShutdownDrainTimeout is the maximum time to wait for in-flight requests and
// the background refresh goroutines to finish when the daemon is stopping.
const ShutdownDrainTimeout = 10 * time.Second

const (
	defaultRefreshInterval = time.Minute
	defaultRefreshTimeout  = 60 * time.Second
	DefaultAddr            = "127.0.0.1:42425"
)

type Collector func(context.Context) (*models.ClusterSnapshot, error)

type SnapshotCache interface {
	Snapshot() (*models.ClusterSnapshot, bool)
	Meta() Metadata
	Invalidate()
	RefreshNow(context.Context) error
}

type Metadata struct {
	Source             string    `json:"source"`
	Ready              bool      `json:"ready"`
	RefreshIntervalSec int       `json:"refresh_interval_sec"`
	CollectedAt        time.Time `json:"collected_at,omitempty"`
	NextRefreshAt      time.Time `json:"next_refresh_at,omitempty"`
	LastError          string    `json:"last_error,omitempty"`
	SnapshotPath       string    `json:"snapshot_path,omitempty"`
	ReservedMB         int64     `json:"reserved_mb,omitempty"`
	Version            string    `json:"version,omitempty"`
	CacheAgeSec        int       `json:"cache_age_sec,omitempty"`
	Stale              bool      `json:"stale,omitempty"`
	// Phase 3: refresh metrics
	RefreshCount  int64    `json:"refresh_count"`
	LastRefreshMs int64    `json:"last_refresh_duration_ms,omitempty"`
	StaleNodes    []string `json:"stale_nodes,omitempty"`
}

type Daemon struct {
	mu           sync.RWMutex
	wg           sync.WaitGroup // tracks background goroutines for graceful drain
	collector    Collector
	interval     time.Duration
	snapshotPath string

	snapshot      *models.ClusterSnapshot
	collectedAt   time.Time
	nextRefreshAt time.Time
	lastError     string

	// Phase 3: refresh metrics
	refreshCount        int64
	lastRefreshDuration time.Duration
	staleNodes          []string // nodes that degraded in the last refresh
}

func (d *Daemon) RefreshNow(ctx context.Context) error {
	return d.Refresh(ctx)
}

func Serve(addr string, cache SnapshotCache) error {
	mux := http.NewServeMux()
	RegisterRoutes(mux, cache)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.ListenAndServe()
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

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
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

// WatchConfig polls configPath every 500ms and triggers Invalidate+Refresh
// whenever the file's modification time changes. Runs until ctx is cancelled.
// This is the primary event-driven cache trigger: config changes are reflected
// immediately without restarting the daemon.
func (d *Daemon) WatchConfig(ctx context.Context, configPath string) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()

		var lastMod time.Time
		if fi, err := os.Stat(configPath); err == nil {
			lastMod = fi.ModTime()
		}

		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fi, err := os.Stat(configPath)
				if err != nil {
					continue
				}
				if fi.ModTime().After(lastMod) {
					lastMod = fi.ModTime()
					d.Invalidate()
					_ = d.Refresh(ctx)
				}
			}
		}
	}()
}

// WaitStopped blocks until all background goroutines have finished or ctx
// expires. Call after cancelling the context passed to Start/WatchConfig to
// ensure in-flight refreshes complete before the process exits.
func (d *Daemon) WaitStopped(ctx context.Context) {
	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (d *Daemon) Refresh(ctx context.Context) error {
	start := time.Now()
	snap, err := d.collector(ctx)
	now := time.Now().UTC()

	var st *state.ClusterState
	var stateWarning error
	if err == nil {
		st, stateWarning = state.Load()
		if stateWarning != nil && st == nil {
			d.mu.Lock()
			d.nextRefreshAt = now.Add(d.interval)
			d.lastError = stateWarning.Error()
			d.refreshCount++
			d.lastRefreshDuration = time.Since(start)
			d.mu.Unlock()
			return stateWarning
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.nextRefreshAt = now.Add(d.interval)
	d.refreshCount++
	d.lastRefreshDuration = time.Since(start)

	if err != nil {
		d.lastError = err.Error()
		return err
	}

	// Detect nodes that degraded since the last refresh (complete → error/unreachable).
	// These are surfaced in Metadata.StaleNodes for operator visibility.
	var staleNodes []string
	if d.snapshot != nil && snap != nil {
		prevStatus := make(map[string]models.NodeStatus, len(d.snapshot.Nodes))
		for _, n := range d.snapshot.Nodes {
			prevStatus[n.Name] = n.Status
		}
		for _, n := range snap.Nodes {
			if prev, ok := prevStatus[n.Name]; ok &&
				prev == models.StatusComplete &&
				(n.Status == models.StatusError || n.Status == models.StatusUnreachable) {
				staleNodes = append(staleNodes, n.Name)
			}
		}
	}
	d.staleNodes = staleNodes

	d.snapshot = snapshotview.Clone(snap)
	ApplyReservationView(d.snapshot, st)
	if stateWarning != nil {
		d.snapshot.Warnings = append(d.snapshot.Warnings, models.Warning{
			Kind:    "state",
			Message: stateWarning.Error(),
		})
	}
	if skillStore, skillErr := skills.Load(); skillErr != nil {
		if skillStore == nil {
			d.lastError = skillErr.Error()
			return skillErr
		}
		d.snapshot.Warnings = append(d.snapshot.Warnings, models.Warning{
			Kind:    "skills",
			Message: skillErr.Error(),
		})
	}
	d.collectedAt = now
	d.lastError = ""

	if d.snapshotPath == "" {
		return nil
	}
	if err := persistSnapshot(d.snapshotPath, d.snapshot); err != nil {
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
	return snapshotview.Clone(d.snapshot), true
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

	age := 0
	stale := false
	if !d.collectedAt.IsZero() {
		age = int(time.Since(d.collectedAt).Seconds())
		if age < 0 {
			age = 0
		}
		stale = time.Since(d.collectedAt) > 5*time.Minute
	}

	meta := Metadata{
		Source:             "daemon-cache",
		Ready:              d.snapshot != nil,
		RefreshIntervalSec: int(d.interval / time.Second),
		CollectedAt:        d.collectedAt,
		NextRefreshAt:      d.nextRefreshAt,
		LastError:          d.lastError,
		SnapshotPath:       d.snapshotPath,
		Version:            Version,
		CacheAgeSec:        age,
		Stale:              stale,
		RefreshCount:       d.refreshCount,
		LastRefreshMs:      d.lastRefreshDuration.Milliseconds(),
		StaleNodes:         d.staleNodes,
	}

	if st, err := state.Load(); st != nil {
		for _, ns := range st.Nodes {
			meta.ReservedMB += ns.ReservedMB
		}
		if err != nil {
			if meta.LastError == "" {
				meta.LastError = err.Error()
			}
		}
	}

	return meta
}

func CanReserve(snap *models.ClusterSnapshot, st *state.ClusterState, node string, mb int64) bool {
	return execution.CanReserve(snap, st, node, mb)
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
	return snapshotview.Clone(snap)
}

func CloneSnapshot(snap *models.ClusterSnapshot) *models.ClusterSnapshot {
	return cloneSnapshot(snap)
}

// ApplyReservationView overlays locally persisted reservations onto a snapshot
// so read paths can reason about allocatable RAM without requiring daemon-only
// semantics.
func ApplyReservationView(snap *models.ClusterSnapshot, st *state.ClusterState) {
	snapshotview.ApplyReservationView(snap, st)
}
