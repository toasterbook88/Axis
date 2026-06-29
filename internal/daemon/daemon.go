package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/discovery"
	"github.com/toasterbook88/axis/internal/events"
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
	Mesh() *mesh.Mesh
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

	// Snapshot change subscribers (O-10). Hooks are invoked once per refresh
	// whose content hash differs from the previous one for that subscriber,
	// so a periodic RefreshTriggerInterval tick with unchanged cluster state
	// does not fire hooks (OQ-5 resolution).
	hooksMu       sync.Mutex
	snapshotHooks map[string]*snapshotHook
}

// snapshotHook records a subscriber to the OnSnapshotChanged event plus its
// per-subscriber content hash used for debounce.
type snapshotHook struct {
	mu       sync.Mutex
	fn       SnapshotChangedFunc
	lastHash [sha256.Size]byte
	lastSet  bool
}

// SnapshotChangedFunc is invoked by the daemon after a successful refresh
// whose content hash differs from the previous one for this subscriber. The
// hook runs synchronously on the refresh goroutine but the daemon
// intentionally does not hold its write lock during dispatch (callers must
// be safe to call without holding d.mu).
//
// Implementations should be non-blocking; a slow handler will stall the next
// refresh tick. The trigger label is one of the RefreshTrigger* constants.
type SnapshotChangedFunc func(snap *models.ClusterSnapshot, trigger string)

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

	cfg, _ := config.Load(config.DefaultConfigPath())
	if cfg == nil || cfg.IsMeshEnabled() {
		var selfPeer mesh.Peer
		var seedPeers []mesh.Peer

		if cfg != nil {
			// Find the local node config
			var localNode config.NodeConfig
			var foundLocal bool
			for _, n := range cfg.Nodes {
				if models.IsLocalConfig(n.Name, n.Hostname, n.StableID) {
					localNode = n
					foundLocal = true
					break
				}
			}

			udpPort := 42426
			if cfg.Discovery != nil && cfg.Discovery.UDPPort > 0 {
				udpPort = cfg.Discovery.UDPPort
			}

			if foundLocal {
				selfPeer = mesh.Peer{
					Name:       localNode.Name,
					Hostname:   localNode.Hostname,
					Port:       udpPort,
					StableID:   localNode.StableID,
					State:      mesh.PeerTrusted,
					Source:     "config",
					FirstSeen:  time.Now().UTC(),
					LastSeen:   time.Now().UTC(),
					Generation: 1,
				}
			}

			for _, n := range cfg.Nodes {
				// Don't add ourselves as a seed
				if foundLocal && n.Name == localNode.Name {
					continue
				}
				seedPeers = append(seedPeers, mesh.Peer{
					Name:       n.Name,
					Hostname:   n.Hostname,
					Port:       udpPort,
					StableID:   n.StableID,
					State:      mesh.PeerTrusted,
					Source:     "config",
					FirstSeen:  time.Now().UTC(),
					LastSeen:   time.Now().UTC(),
					Generation: 1,
				})
			}
		}

		meshCfg := mesh.DefaultConfig()
		if cfg != nil && cfg.Discovery != nil {
			if cfg.Discovery.UDPPort > 0 {
				meshCfg.ListenAddr = fmt.Sprintf(":%d", cfg.Discovery.UDPPort)
			}
			if cfg.Discovery.BeaconInterval > 0 {
				meshCfg.GossipInterval = time.Duration(cfg.Discovery.BeaconInterval) * time.Second
			}
			if cfg.Discovery.Secret != "" {
				meshCfg.SharedSecret = cfg.Discovery.Secret
			}
		}

		d.mesh = mesh.New(selfPeer, meshCfg, nil)
		for _, seed := range seedPeers {
			d.mesh.AddSeed(seed)
		}
	}
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
		snapshotHooks:   make(map[string]*snapshotHook),
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

	// Periodic cache refresh loop
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

	// Periodic lease eviction loop (every 10 seconds)
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		evictTicker := time.NewTicker(10 * time.Second)
		defer evictTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-evictTicker.C:
				if d.ledger != nil {
					d.ledger.Prune()
				}
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

	// We are the winner: record start time for latency fallback
	startTime := time.Now()

	// Do the refresh
	err := d.doRefresh(ctx, trigger)

	// After refresh, process pending triggers that came in while we were refreshing
	var pendingTriggers map[string]bool
	var pendingRequestedAt time.Time

	d.pendingMu.Lock()
	pendingTriggers = d.pendingTriggers
	pendingRequestedAt = d.pendingRequestedAt
	// Clear the pending triggers and reset the requestedAt for next cycle
	d.pendingTriggers = make(map[string]bool)
	d.pendingRequestedAt = time.Time{}
	if !pendingRequestedAt.IsZero() {
		d.activeRequestedAt = pendingRequestedAt
	} else {
		d.activeRequestedAt = startTime
	}
	d.pendingMu.Unlock()

	defer func() {
		d.refreshing.Store(false)

		var nextTrigger string
		if len(pendingTriggers) > 0 {
			var keys []string
			for k := range pendingTriggers {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			nextTrigger = strings.Join(keys, ",")
		}

		select {
		case <-d.pendingRefresh:
		default:
		}

		if nextTrigger != "" {
			d.scheduleRefresh(nextTrigger)
		}
	}()

	// Latency measurement
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
	// Advisory pre-refresh event
	events.EmitToBuffer(events.NoopEmitter{}, events.EventDaemonRefreshPre, map[string]any{
		events.PayloadKeyTrigger: trigger,
	})

	start := time.Now()
	snap, err := d.collector(ctx)
	now := time.Now().UTC()

	var st *state.ClusterState
	var stateWarning error
	if err == nil {
		if d.ledger != nil {
			if loadErr := d.ledger.Load(); loadErr != nil {
				slog.Error("failed to reload reservation ledger during refresh", "error", loadErr)
			}
			if snap != nil {
				for _, n := range snap.Nodes {
					if n.Resources != nil {
						d.ledger.SetNodeCapacity(n.Name, n.Resources.RAMTotalMB)
					}
					d.ledger.SetNodeReserve(n.Name, n.SystemReserveMB)
				}
			}
		}
		st, stateWarning = state.Load()
		if st != nil {
			if state.Maintain(st) {
				if err := st.Save(); err != nil {
					slog.Error("failed to save maintained state", "path", state.Path(), "error", err)
				}
			}
		}
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

	d.nextRefreshAt = now.Add(d.interval)
	d.lastTrigger = trigger
	if trigger == RefreshTriggerConfigChange {
		d.lastConfigAt = now
	}
	d.refreshCount++
	d.lastRefreshDuration = time.Since(start)

	if err != nil {
		d.lastError = err.Error()
		d.mu.Unlock()
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

	// Advisory snapshot collected event
	events.EmitToBuffer(events.NoopEmitter{}, events.EventSnapshotCollected, map[string]any{
		events.PayloadKeyTrigger: trigger,
		"node_count":             len(snap.Nodes),
	})

	if stateWarning != nil {
		d.snapshot.Warnings = append(d.snapshot.Warnings, models.Warning{
			Kind:    "state",
			Message: stateWarning.Error(),
		})
	}
	if skillStore, skillErr := skills.Load(); skillErr != nil {
		if skillStore == nil {
			d.lastError = skillErr.Error()
			d.mu.Unlock()
			return skillErr
		}
		d.snapshot.Warnings = append(d.snapshot.Warnings, models.Warning{
			Kind:    "skills",
			Message: skillErr.Error(),
		})
	}
	d.collectedAt = now
	d.lastError = ""

	snapCopy := d.snapshot
	d.mu.Unlock()

	// Emit advisory post-refresh event for external observers (MCP agents, hooks, etc.)
	events.EmitToBuffer(events.NoopEmitter{}, events.EventDaemonRefreshPost, map[string]any{
		events.PayloadKeyTrigger: trigger,
		"nodes":                  len(snapCopy.Nodes),
	})

	if d.snapshotPath == "" {
		// No persistence configured, but still need to fire hooks if content
		// changed (test paths and in-process consumers rely on this).
		d.dispatchSnapshotHooks(snapCopy, trigger)
		return nil
	}
	if err := persistSnapshot(d.snapshotPath, snapCopy); err != nil {
		d.mu.Lock()
		d.lastError = err.Error()
		d.mu.Unlock()
		return err
	}
	d.dispatchSnapshotHooks(snapCopy, trigger)
	return nil
}

// AddOnSnapshotChanged registers a subscriber that will be invoked after each
// successful refresh whose content hash differs from the previous one seen
// by that subscriber. The returned function unregisters the hook.
//
// The daemon's interval refresh can produce a byte-identical snapshot when
// the cluster is idle; hooks are debounced per-subscriber so an idle cluster
// does not generate redundant notifications (OQ-5).
//
// The hook runs on the refresh goroutine after d.mu has been released. It
// must not call back into d.Snapshot() with a long-held read lock; clone the
// snapshot first if persistence is required beyond the callback.
//
// This API is intended for in-process consumers (MCP server, HTTP cache
// watcher). It is not part of the public HTTP surface.
func (d *Daemon) AddOnSnapshotChanged(fn SnapshotChangedFunc) (remove func()) {
	if fn == nil {
		return func() {}
	}
	id := uuid.NewString()
	d.hooksMu.Lock()
	if d.snapshotHooks == nil {
		d.snapshotHooks = make(map[string]*snapshotHook)
	}
	d.snapshotHooks[id] = &snapshotHook{fn: fn}
	d.hooksMu.Unlock()
	return func() {
		d.hooksMu.Lock()
		delete(d.snapshotHooks, id)
		d.hooksMu.Unlock()
	}
}

// dispatchSnapshotHooks fires every registered hook whose stored hash differs
// from the SHA-256 of snap. Must not be called with d.mu held.
//
// Errors from individual hooks are logged and swallowed; one bad subscriber
// must not block the others or the next refresh.
func (d *Daemon) dispatchSnapshotHooks(snap *models.ClusterSnapshot, trigger string) {
	if snap == nil {
		return
	}
	d.hooksMu.Lock()
	hooks := make([]*snapshotHook, 0, len(d.snapshotHooks))
	for _, h := range d.snapshotHooks {
		hooks = append(hooks, h)
	}
	d.hooksMu.Unlock()

	hash := hashSnapshot(snap)
	for _, h := range hooks {
		h.mu.Lock()
		if h.lastSet && h.lastHash == hash {
			h.mu.Unlock()
			continue
		}
		// Update before invocation so a re-entrant call from inside the hook
		// does not see a stale hash.
		h.lastHash = hash
		h.lastSet = true
		h.mu.Unlock()

		func(h *snapshotHook) {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("daemon: snapshot hook panic recovered",
						"trigger", trigger, "panic", r)
				}
			}()
			h.fn(snap, trigger)
		}(h)
	}
}

// hashSnapshot computes a deterministic SHA-256 over the entire snapshot by
// marshaling its fields to JSON. It intentionally zeroes out the Timestamp
// field first so that refresh timestamps do not defeat the debounce logic.
func hashSnapshot(snap *models.ClusterSnapshot) [sha256.Size]byte {
	if snap == nil {
		return [sha256.Size]byte{}
	}
	// Shallow copy the snapshot to zero out the Timestamp so it doesn't defeat the debounce.
	snapCopy := *snap
	snapCopy.Timestamp = time.Time{}

	// Use the canonical JSON for stability. A malformed snapshot will hash
	// to the empty struct, which still produces a deterministic value.
	data, err := json.Marshal(&snapCopy)
	if err != nil {
		return [sha256.Size]byte{}
	}
	return sha256.Sum256(data)
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
