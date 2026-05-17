package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/mesh"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
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
	defaultStaleThreshold  = 5 * time.Minute
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
	LastRefreshTrigger string    `json:"last_refresh_trigger,omitempty"`
	LastConfigEventAt  time.Time `json:"last_config_event_at,omitempty"`
	CollectedAt        time.Time `json:"collected_at,omitempty"`
	NextRefreshAt      time.Time `json:"next_refresh_at,omitempty"`
	LastError          string    `json:"last_error,omitempty"`
	SnapshotPath       string    `json:"snapshot_path,omitempty"`
	ReservedMB         int64     `json:"reserved_mb,omitempty"`
	Version            string    `json:"version,omitempty"`
	CacheAgeSec        int       `json:"cache_age_sec,omitempty"`
	Stale              bool      `json:"stale,omitempty"`
	MeshPeers          int       `json:"mesh_peers,omitempty"`
	StaleThresholdSec  int       `json:"stale_threshold_sec,omitempty"`
	// Phase 3: refresh metrics
	RefreshCount        int64                      `json:"refresh_count"`
	LastRefreshMs       int64                      `json:"last_refresh_duration_ms,omitempty"`
	MaxRefreshLatencyMs int64                      `json:"max_refresh_latency_ms,omitempty"`
	StaleNodes          []string                   `json:"stale_nodes,omitempty"`
	Freshness           *models.DiscoveryFreshness `json:"freshness,omitempty"`
}

type Daemon struct {
	mu             sync.RWMutex
	refreshing     atomic.Bool
	pendingRefresh chan string
	wg             sync.WaitGroup // tracks background goroutines for graceful drain
	collector      Collector
	interval       time.Duration
	staleThreshold time.Duration
	snapshotPath   string
	beaconRegistry *discovery.BeaconRegistry
	ledger         *reservation.Ledger
	mesh           *mesh.Mesh

	snapshot      *models.ClusterSnapshot
	collectedAt   time.Time
	nextRefreshAt time.Time
	lastTrigger   string
	lastConfigAt  time.Time
	lastError     string

	// Phase 3: refresh metrics
	refreshCount        int64
	lastRefreshDuration time.Duration
	staleNodes          []string // nodes that degraded in the last refresh

	pendingMu          sync.Mutex
	pendingTriggers    map[string]bool
	pendingRequestedAt time.Time
	activeRequestedAt  time.Time
	maxRefreshLatency  time.Duration
}

type configFingerprint struct {
	exists bool
	sum    [sha256.Size]byte
}

var watchConfigPollInterval = 500 * time.Millisecond
var reportWatchFingerprintError = func(path, trigger string, err error) {
	slog.Error("daemon: watch fingerprint failed", "path", path, "trigger", trigger, "error", err)
}

func (d *Daemon) RefreshNow(ctx context.Context) error {
	return d.RefreshWithTrigger(ctx, RefreshTriggerManual)
}

func (d *Daemon) RefreshWithTrigger(ctx context.Context, trigger string) error {
	normalized, err := NormalizeRefreshTrigger(trigger)
	if err != nil {
		return err
	}
	return d.refreshWithTrigger(ctx, normalized)
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
	registry := discovery.NewBeaconRegistry()
	d := New(interval, defaultCollector(registry))
	d.beaconRegistry = registry
	d.mesh = mesh.New(mesh.Peer{}, mesh.DefaultConfig(), nil)
	return d
}

func New(interval time.Duration, collector Collector) *Daemon {
	if interval <= 0 {
		interval = defaultRefreshInterval
	}
	if collector == nil {
		collector = defaultCollector(nil)
	}
	d := &Daemon{
		collector:       collector,
		interval:        interval,
		staleThreshold:  defaultStaleThreshold,
		snapshotPath:    DefaultSnapshotPath(),
		ledger:          reservation.NewLedger(reservation.DefaultLimits(), nil),
		pendingRefresh:  make(chan string, 1),
		pendingTriggers: make(map[string]bool),
	}
	if err := d.ledger.Load(); err != nil {
		slog.Error("failed to load reservation ledger", "error", err)
	}
	return d
}

func (d *Daemon) Ledger() *reservation.Ledger {
	return d.ledger
}

func (d *Daemon) Mesh() *mesh.Mesh {
	return d.mesh
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

// SetStaleThreshold configures how old the cache must be before it is
// considered stale. The default is 5 minutes. A value <= 0 resets to default.
func (d *Daemon) SetStaleThreshold(threshold time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if threshold <= 0 {
		threshold = defaultStaleThreshold
	}
	d.staleThreshold = threshold
}

func (d *Daemon) Start(ctx context.Context) {
	d.mu.Lock()
	d.nextRefreshAt = time.Now().UTC().Add(d.interval)
	d.mu.Unlock()

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		_ = d.refreshWithTrigger(ctx, RefreshTriggerStartup)

		ticker := time.NewTicker(d.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = d.refreshWithTrigger(ctx, RefreshTriggerInterval)
			}
		}
	}()
}

// WatchConfig polls configPath and triggers Invalidate+Refresh whenever the
// file contents appear or change, or when the file disappears. Runs until ctx
// is cancelled. This is the primary config-driven cache trigger.
func (d *Daemon) WatchConfig(ctx context.Context, configPath string) {
	d.watchFile(ctx, configPath, RefreshTriggerConfigChange)
}

// WatchState polls statePath and triggers Invalidate+Refresh whenever local
// reservation/failure memory changes on disk. This keeps cached snapshots in
// step with guarded execution and local placement memory.
func (d *Daemon) WatchState(ctx context.Context, statePath string) {
	d.watchFileWithFingerprint(ctx, statePath, RefreshTriggerStateChange, fingerprintStateFile)
}

// WatchSkills polls skillsPath and triggers Invalidate+Refresh whenever the
// learned skills/failures store changes on disk.
func (d *Daemon) WatchSkills(ctx context.Context, skillsPath string) {
	d.watchFile(ctx, skillsPath, RefreshTriggerSkillsChange)
}

// WatchDiscovery keeps a long-lived UDP discovery watcher aligned with the
// current config file and refreshes the daemon cache when beacon-derived nodes
// appear, change, or age out.
func (d *Daemon) WatchDiscovery(ctx context.Context, configPath string) {
	if d.beaconRegistry == nil {
		return
	}

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()

		var (
			lastFingerprint configFingerprint
			watcherCancel   context.CancelFunc
			initialized     bool
		)
		defer func() {
			if watcherCancel != nil {
				watcherCancel()
			}
		}()

		applyWatcher := func() {
			if watcherCancel != nil {
				watcherCancel()
				watcherCancel = nil
			}
			_ = d.beaconRegistry.Reset()

			cfg, err := config.Load(configPath)
			if err != nil || cfg == nil || cfg.Discovery == nil || !cfg.Discovery.Enabled {
				return
			}

			watchCtx, cancel := context.WithCancel(ctx)
			watcherCancel = cancel
			discovery.WatchBeaconChanges(watchCtx, cfg, d.beaconRegistry, func() {
				d.scheduleRefresh(RefreshTriggerBeaconChange)
			})
		}

		lastFingerprint, _ = fingerprintFile(configPath)
		initialized = true
		applyWatcher()

		ticker := time.NewTicker(watchConfigPollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fingerprint, err := fingerprintFile(configPath)
				if err != nil {
					continue
				}
				if !initialized || fingerprint != lastFingerprint {
					lastFingerprint = fingerprint
					initialized = true
					applyWatcher()
				}
			}
		}
	}()
}

func (d *Daemon) watchFile(ctx context.Context, path, trigger string) {
	d.watchFileWithFingerprint(ctx, path, trigger, fingerprintFile)
}

func (d *Daemon) watchFileWithFingerprint(ctx context.Context, path, trigger string, fingerprintFn func(string) (configFingerprint, error)) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()

		lastFingerprint, err := fingerprintFn(path)
		if err != nil {
			reportWatchFingerprintError(path, trigger, err)
		}

		ticker := time.NewTicker(watchConfigPollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fingerprint, err := fingerprintFn(path)
				if err != nil {
					reportWatchFingerprintError(path, trigger, err)
					continue
				}
				if fingerprint != lastFingerprint {
					lastFingerprint = fingerprint
					d.Invalidate()
					_ = d.refreshWithTrigger(ctx, trigger)
				}
			}
		}
	}()
}

// WatchMesh starts the mesh gossip layer and refreshes cache on peer events
func (d *Daemon) WatchMesh(ctx context.Context, self mesh.Peer) {
	if d.mesh == nil {
		return
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()

		d.mesh.OnPeerJoin = func(p mesh.Peer) {
			slog.Info("mesh: peer joined", "peer", p.Name)
			_ = d.RefreshWithTrigger(ctx, RefreshTriggerDiscovery)
		}

		d.mesh.OnPeerLeave = func(p mesh.Peer) {
			slog.Info("mesh: peer left", "peer", p.Name)
			_ = d.RefreshWithTrigger(ctx, RefreshTriggerDiscovery)
		}

		if err := d.mesh.Start(ctx); err != nil {
			slog.Error("mesh: failed to start", "error", err)
		}

		<-ctx.Done()
		d.mesh.Stop()
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
	return d.RefreshWithTrigger(ctx, RefreshTriggerManual)
}

func (d *Daemon) scheduleRefresh(trigger string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), runtimeRefreshTimeout)
		defer cancel()
		_ = d.RefreshWithTrigger(ctx, trigger)
	}()
}

func (d *Daemon) refreshWithTrigger(ctx context.Context, trigger string) error {
	if !d.refreshing.CompareAndSwap(false, true) {
		// Already refreshing, queue the trigger (coalescing triggers and measuring requestedAt)
		d.pendingMu.Lock()
		if d.pendingTriggers == nil {
			d.pendingTriggers = make(map[string]bool)
		}
		d.pendingTriggers[trigger] = true
		if d.pendingRequestedAt.IsZero() {
			d.pendingRequestedAt = time.Now()
		}
		d.pendingMu.Unlock()

		select {
		case <-d.pendingRefresh:
		default:
		}
		select {
		case d.pendingRefresh <- "coalesced":
		default:
		}
		return nil
	}

	d.pendingMu.Lock()
	if !d.pendingRequestedAt.IsZero() {
		d.activeRequestedAt = d.pendingRequestedAt
		d.pendingRequestedAt = time.Time{}
		d.pendingTriggers = make(map[string]bool)
	} else {
		d.activeRequestedAt = time.Now()
	}
	d.pendingMu.Unlock()

	defer func() {
		d.refreshing.Store(false)

		var nextTrigger string
		d.pendingMu.Lock()
		if len(d.pendingTriggers) > 0 {
			var keys []string
			for k := range d.pendingTriggers {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			nextTrigger = strings.Join(keys, ",")
		}
		d.pendingMu.Unlock()

		select {
		case <-d.pendingRefresh:
		default:
		}

		if nextTrigger != "" {
			d.scheduleRefresh(nextTrigger)
		}
	}()

	err := d.doRefresh(ctx, trigger)

	now := time.Now()
	d.pendingMu.Lock()
	latency := now.Sub(d.activeRequestedAt)
	if latency > d.maxRefreshLatency {
		d.maxRefreshLatency = latency
	}
	d.pendingMu.Unlock()

	return err
}

func (d *Daemon) doRefresh(ctx context.Context, trigger string) error {
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
			d.lastTrigger = trigger
			if trigger == RefreshTriggerConfigChange {
				d.lastConfigAt = now
			}
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
	d.lastTrigger = trigger
	if trigger == RefreshTriggerConfigChange {
		d.lastConfigAt = now
	}
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
	ApplyReservationView(d.snapshot, st, d.ledger)
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

func fingerprintFile(path string) (configFingerprint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return configFingerprint{}, nil
		}
		return configFingerprint{}, err
	}
	return configFingerprint{
		exists: true,
		sum:    sha256.Sum256(data),
	}, nil
}

func fingerprintStateFile(path string) (configFingerprint, error) {
	sum, exists, err := state.SemanticFingerprint(path)
	if err != nil {
		return configFingerprint{}, err
	}
	return configFingerprint{
		exists: exists,
		sum:    sum,
	}, nil
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

	age := time.Duration(0)
	stale := false
	if !d.collectedAt.IsZero() {
		age = time.Since(d.collectedAt)
		stale = age > d.staleThreshold
	}

	meta := Metadata{
		Source:             "daemon-cache",
		Ready:              d.snapshot != nil,
		RefreshIntervalSec: int(d.interval / time.Second),
		LastRefreshTrigger: d.lastTrigger,
		LastConfigEventAt:  d.lastConfigAt,
		CollectedAt:        d.collectedAt,
		NextRefreshAt:      d.nextRefreshAt,
		LastError:          d.lastError,
		SnapshotPath:       d.snapshotPath,
		Version:            Version,
		CacheAgeSec:        int(age.Seconds()),
		Stale:              stale,
		StaleThresholdSec:  int(d.staleThreshold / time.Second),
	}
	if d.mesh != nil {
		meta.MeshPeers = len(d.mesh.ActivePeers())
	}
	meta.RefreshCount = d.refreshCount
	meta.LastRefreshMs = d.lastRefreshDuration.Milliseconds()
	d.pendingMu.Lock()
	meta.MaxRefreshLatencyMs = d.maxRefreshLatency.Milliseconds()
	d.pendingMu.Unlock()
	meta.StaleNodes = d.staleNodes
	if d.snapshot != nil && d.snapshot.Freshness != nil {
		freshness := *d.snapshot.Freshness
		meta.Freshness = &freshness
	}

	if d.ledger != nil {
		meta.ReservedMB = d.ledger.Summary().TotalReservedMB
	} else if st, err := state.Load(); st != nil {
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

func CanReserve(snap *models.ClusterSnapshot, node string, mb int64) bool {
	return execution.CanReserve(snap, node, mb)
}

func defaultCollector(registry *discovery.BeaconRegistry) Collector {
	return func(ctx context.Context) (*models.ClusterSnapshot, error) {
		cfg, err := config.Load(config.DefaultConfigPath())
		if err != nil {
			return nil, err
		}

		runCtx, cancel := context.WithTimeout(ctx, defaultRefreshTimeout)
		defer cancel()

		var result discovery.Result
		if registry != nil && cfg.Discovery != nil && cfg.Discovery.Enabled {
			result = discovery.DiscoverSeededResult(runCtx, cfg, registry.Snapshot())
			result.Freshness = registry.Freshness(cfg, len(cfg.Nodes))
		} else {
			result = discovery.DiscoverResult(runCtx, cfg)
		}
		snap := snapshot.Build(result.Nodes)
		if snap == nil {
			snap = &models.ClusterSnapshot{}
		}
		snap.Freshness = result.Freshness
		for _, warning := range result.Warnings {
			models.AppendWarningIfMissing(snap, warning)
		}
		if snap.Freshness != nil && snap.Freshness.Warning != "" {
			models.AppendWarningIfMissing(snap, models.Warning{
				Kind:    "discovery",
				Message: snap.Freshness.Warning,
			})
		}
		return snap, nil
	}
}

func persistSnapshot(path string, snap *models.ClusterSnapshot) error {
	if snap == nil {
		return errors.New("nil snapshot")
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
func ApplyReservationView(snap *models.ClusterSnapshot, st *state.ClusterState, ledger *reservation.Ledger) {
	snapshotview.ApplyReservationView(snap, st, ledger)
}

func (d *Daemon) MaxRefreshLatency() time.Duration {
	d.pendingMu.Lock()
	defer d.pendingMu.Unlock()
	return d.maxRefreshLatency
}
