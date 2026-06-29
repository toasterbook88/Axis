// Package reservation is EXPERIMENTAL — double-entry reservation ledger for cluster RAM and VRAM.
// Not wired into the stable operator path.

// Integration: The daemon holds the Ledger. Placement consults it for
// allocatable headroom. Execution creates/releases entries via the Ledger API.
package reservation

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/toasterbook88/axis/internal/events"
	"github.com/toasterbook88/axis/internal/models"
)

// Entry represents a single reservation on a node.
type Entry struct {
	ID            string                 `json:"id"`
	Node          string                 `json:"node"`
	OwnerExecID   string                 `json:"owner_exec_id"`
	OwnerSurface  string                 `json:"owner_surface"`
	OwnerPID      int                    `json:"owner_pid,omitempty"`
	OwnerOrigin   models.ExecutionOrigin `json:"owner_origin,omitempty"`
	RAMMB         int64                  `json:"ram_mb"`
	VRAMMB        int64                  `json:"vram_mb,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
	LastHeartbeat time.Time              `json:"last_heartbeat"`
	ExpiresAt     time.Time              `json:"expires_at,omitempty"` // 0 = no hard expiry
	Description   string                 `json:"description,omitempty"`
}

// IsStale returns true if the entry has missed its heartbeat window.
func (e Entry) IsStale(now time.Time, window time.Duration) bool {
	diff := now.Sub(e.LastHeartbeat)
	if diff < 0 {
		return false
	}
	return diff > window
}

// IsExpired returns true if the entry has a hard expiry that has passed.
func (e Entry) IsExpired(now time.Time) bool {
	return !e.ExpiresAt.IsZero() && now.After(e.ExpiresAt)
}

type LivenessState string

const (
	LivenessActive  LivenessState = "active"
	LivenessStale   LivenessState = "stale"
	LivenessExpired LivenessState = "expired"
)

// ClassifyLiveness classifies the entry's liveness state based on full limits.
func (e Entry) ClassifyLiveness(now time.Time, limits Limits) LivenessState {
	if e.IsExpired(now) {
		return LivenessExpired
	}
	if !e.LastHeartbeat.IsZero() {
		diff := now.Sub(e.LastHeartbeat)
		if diff > 0 && diff > limits.HeartbeatStaleWindow {
			return LivenessStale
		}
	} else {
		diff := now.Sub(e.CreatedAt)
		if diff > 0 && diff > limits.LegacyStaleWindow {
			return LivenessStale
		}
	}
	return LivenessActive
}

// NodeSummary aggregates reservation state for a single node.
type NodeSummary struct {
	Node           string  `json:"node"`
	TotalRAMMB     int64   `json:"total_ram_mb"`
	ReservedRAMMB  int64   `json:"reserved_ram_mb"`
	ReservedVRAMMB int64   `json:"reserved_vram_mb"`
	ActiveEntries  int     `json:"active_entries"`
	StaleEntries   int     `json:"stale_entries"`
	UtilizationPct float64 `json:"utilization_pct"`
}

// ClusterSummary aggregates reservation state across all nodes.
type ClusterSummary struct {
	Nodes           []NodeSummary `json:"nodes"`
	TotalReservedMB int64         `json:"total_reserved_mb"`
	TotalVRAMMB     int64         `json:"total_vram_mb"`
	ActiveEntries   int           `json:"active_entries"`
	StaleEntries    int           `json:"stale_entries"`
}

// Limits controls over-commitment policy.
type Limits struct {
	// MaxOvercommitRatio is the max ratio of reserved/total RAM per node.
	// 1.0 = no overcommit. 1.5 = allow 50% overcommit. 0 = unlimited.
	MaxOvercommitRatio float64 `yaml:"max_overcommit_ratio" json:"max_overcommit_ratio"`
	// SystemReserveMB is RAM held back from allocation (default 1024).
	SystemReserveMB int64 `yaml:"system_reserve_mb" json:"system_reserve_mb"`
	// HeartbeatStaleWindow is how long since last heartbeat before stale.
	HeartbeatStaleWindow time.Duration `yaml:"heartbeat_stale_window" json:"heartbeat_stale_window"`
	// MaxEntriesPerNode caps concurrent reservations per node.
	MaxEntriesPerNode int `yaml:"max_entries_per_node" json:"max_entries_per_node"`
	// LegacyStaleWindow is the legacy fallback stale window for zero heartbeats.
	LegacyStaleWindow time.Duration `yaml:"legacy_stale_window" json:"legacy_stale_window"`
}

// DefaultLimits returns conservative defaults.
func DefaultLimits() Limits {
	return Limits{
		MaxOvercommitRatio:   1.0, // no overcommit
		SystemReserveMB:      1024,
		HeartbeatStaleWindow: 2 * time.Minute,
		MaxEntriesPerNode:    32,
		LegacyStaleWindow:    45 * time.Minute,
	}
}

// Ledger is the central reservation accounting system.
type Ledger struct {
	mu      sync.RWMutex
	entries map[string]*Entry // key: entry ID
	limits  Limits
	logger  *slog.Logger
	now     func() time.Time

	// nodeRAM maps node name → total RAM in MB (populated from snapshots).
	nodeRAM map[string]int64

	// nodeReserve maps node name → system reserve in MB (per-node override).
	// When zero or missing, falls back to limits.SystemReserveMB.
	nodeReserve map[string]int64

	lockFile *os.File // file lock for cross-process synchronization

	// Metrics
	totalReserved   int64
	totalReleased   int64
	totalReclaimed  int64
	reserveFailures int64
}

// NewLedger creates a reservation ledger.
func NewLedger(limits Limits, logger *slog.Logger) *Ledger {
	if logger == nil {
		logger = slog.Default()
	}
	return &Ledger{
		entries:     make(map[string]*Entry),
		limits:      limits,
		logger:      logger.With("component", "reservation-ledger"),
		nodeRAM:     make(map[string]int64),
		nodeReserve: make(map[string]int64),
		now:         time.Now,
	}
}

// SetNodeCapacity updates the total RAM capacity for a node.
// Called when snapshots are refreshed.
func (l *Ledger) SetNodeCapacity(node string, totalRAMMB int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nodeRAM[node] = totalRAMMB
}

// SetNodeReserve updates the system reserve override for a node.
// A value <= 0 clears the override and falls back to limits.SystemReserveMB.
func (l *Ledger) SetNodeReserve(node string, reserveMB int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if reserveMB <= 0 {
		delete(l.nodeReserve, node)
		return
	}
	l.nodeReserve[node] = reserveMB
}

func (l *Ledger) systemReserveFor(node string) int64 {
	if r, ok := l.nodeReserve[node]; ok && r > 0 {
		return r
	}
	return l.limits.SystemReserveMB
}

// Reserve attempts to create a reservation. Returns the Entry on success.
// Fails if node capacity is unknown or if it would violate overcommit limits
// or per-node caps.
func (l *Ledger) Reserve(req Entry) (*Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	wasLocked := l.lockFile != nil
	if !wasLocked {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := l.lockFileLocked(ctx); err != nil {
			return nil, err
		}
		defer l.unlockFileLocked()
	}

	if req.ID == "" {
		return nil, fmt.Errorf("reservation: entry ID required")
	}
	if req.Node == "" {
		return nil, fmt.Errorf("reservation: node required")
	}
	if req.RAMMB <= 0 {
		return nil, fmt.Errorf("reservation: RAM must be > 0")
	}
	totalRAM, ok := l.nodeRAM[req.Node]
	if !ok || totalRAM <= 0 {
		l.reserveFailures++
		return nil, fmt.Errorf("reservation: node %q capacity unknown", req.Node)
	}

	// Check existing
	if _, exists := l.entries[req.ID]; exists {
		return nil, fmt.Errorf("reservation: duplicate ID %q", req.ID)
	}

	// Check per-node cap
	nodeCount := 0
	var nodeReserved int64
	var parentEntry *Entry
	parentID := os.Getenv("AXIS_EXECUTION_PARENT_ID")
	for _, e := range l.entries {
		if e.Node == req.Node {
			nodeCount++
			nodeReserved += e.RAMMB
			if parentID != "" && (e.ID == parentID || e.OwnerExecID == parentID) {
				parentEntry = e
			}
		}
	}
	if parentEntry != nil {
		nodeReserved -= parentEntry.RAMMB
	}

	if l.limits.MaxEntriesPerNode > 0 && nodeCount >= l.limits.MaxEntriesPerNode {
		l.reserveFailures++
		return nil, fmt.Errorf("reservation: node %q at max entries (%d)", req.Node, l.limits.MaxEntriesPerNode)
	}

	// Check overcommit ratio
	if l.limits.MaxOvercommitRatio > 0 {
		allocatable := totalRAM - l.systemReserveFor(req.Node)
		if allocatable <= 0 {
			l.reserveFailures++
			return nil, fmt.Errorf("reservation: node %q has no allocatable RAM after system reserve", req.Node)
		}
		newTotal := nodeReserved + req.RAMMB
		ratio := float64(newTotal) / float64(allocatable)
		if ratio > l.limits.MaxOvercommitRatio {
			l.reserveFailures++
			return nil, fmt.Errorf("reservation: node %q would exceed overcommit ratio (%.2f > %.2f, reserved=%dMB, allocatable=%dMB)",
				req.Node, ratio, l.limits.MaxOvercommitRatio, newTotal, allocatable)
		}
	}

	now := l.now()
	req.CreatedAt = now
	req.LastHeartbeat = now
	l.entries[req.ID] = &req
	l.totalReserved += req.RAMMB

	l.logger.Info("reservation created",
		"id", req.ID,
		"node", req.Node,
		"ram_mb", req.RAMMB,
		"owner", req.OwnerSurface,
	)

	// Advisory event for external observers
	events.Emit(events.NoopEmitter{}, events.EventReservationGranted, map[string]any{
		events.PayloadKeyNode: req.Node,
		"id":                  req.ID,
		"ram_mb":              req.RAMMB,
		"owner":               req.OwnerSurface,
	})

	if err := l.saveLocked(); err != nil {
		l.logger.Error("failed to persist ledger", "error", err)
	}
	return &req, nil
}

// Release removes a reservation by ID.
func (l *Ledger) Release(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	wasLocked := l.lockFile != nil
	if !wasLocked {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := l.lockFileLocked(ctx); err != nil {
			return err
		}
		defer l.unlockFileLocked()
	}

	e, ok := l.entries[id]
	if !ok {
		return fmt.Errorf("reservation: unknown entry %q", id)
	}
	l.totalReleased += e.RAMMB
	delete(l.entries, id)
	l.logger.Info("reservation released", "id", id, "node", e.Node, "ram_mb", e.RAMMB)

	// Advisory event
	events.Emit(events.NoopEmitter{}, events.EventReservationReleased, map[string]any{
		"id":     id,
		"node":   e.Node,
		"ram_mb": e.RAMMB,
	})

	return l.saveLocked()
}

// Heartbeat updates the liveness timestamp for a reservation.
func (l *Ledger) Heartbeat(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	wasLocked := l.lockFile != nil
	if !wasLocked {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := l.lockFileLocked(ctx); err != nil {
			return err
		}
		defer l.unlockFileLocked()
	}

	e, ok := l.entries[id]
	if !ok {
		return fmt.Errorf("reservation: unknown entry %q for heartbeat", id)
	}
	e.LastHeartbeat = l.now()
	return l.saveLocked()
}

// Reclaim removes all stale and expired reservations. Returns count reclaimed.
// This is the primary orphan sweeper for the ledger.
func (l *Ledger) Reclaim() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	wasLocked := l.lockFile != nil
	if !wasLocked {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := l.lockFileLocked(ctx); err != nil {
			l.logger.Error("failed to acquire file lock for reclaim", "error", err)
			return 0
		}
		defer l.unlockFileLocked()
	}

	return l.reclaimLocked()
}

// Reconcile is a semantic alias for Reclaim used during startup or recovery
// to emphasize the reconciliation pass.
func (l *Ledger) Reconcile() int {
	return l.Reclaim()
}

// Prune is a synonym for Reclaim to support the lease eviction loop interface.
func (l *Ledger) Prune() int {
	return l.Reclaim()
}

func (l *Ledger) reclaimLocked() int {
	now := l.now()
	reclaimed := 0
	for id, e := range l.entries {
		liveness := e.ClassifyLiveness(now, l.limits)
		if liveness == LivenessExpired || liveness == LivenessStale {
			l.totalReclaimed += e.RAMMB
			delete(l.entries, id)
			reclaimed++
			l.logger.Info("reservation reclaimed",
				"id", id,
				"node", e.Node,
				"ram_mb", e.RAMMB,
				"reason", string(liveness),
			)
		}
	}
	if reclaimed > 0 {
		if err := l.saveLocked(); err != nil {
			l.logger.Error("failed to persist ledger during reclaim", "error", err)
		}
	}
	return reclaimed
}

// AllocatableRAM returns the allocatable RAM on a node after subtracting
// current reservations and system reserve.
func (l *Ledger) AllocatableRAM(node string) int64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	total, ok := l.nodeRAM[node]
	if !ok {
		return 0
	}
	allocatable := total - l.systemReserveFor(node)
	var reserved int64
	for _, e := range l.entries {
		if e.Node == node {
			reserved += e.RAMMB
		}
	}
	result := allocatable - reserved
	if result < 0 {
		return 0
	}
	return result
}

// NodeSummaryFor returns the reservation summary for a specific node.
func (l *Ledger) NodeSummaryFor(node string) NodeSummary {
	l.mu.RLock()
	defer l.mu.RUnlock()
	now := l.now()
	summary := NodeSummary{
		Node:       node,
		TotalRAMMB: l.nodeRAM[node],
	}
	for _, e := range l.entries {
		if e.Node != node {
			continue
		}
		summary.ReservedRAMMB += e.RAMMB
		summary.ReservedVRAMMB += e.VRAMMB
		if e.ClassifyLiveness(now, l.limits) != LivenessActive {
			summary.StaleEntries++
		} else {
			summary.ActiveEntries++
		}
	}
	if summary.TotalRAMMB > 0 {
		summary.UtilizationPct = float64(summary.ReservedRAMMB) / float64(summary.TotalRAMMB) * 100
	}
	return summary
}

// Summary returns the cluster-wide reservation summary.
func (l *Ledger) Summary() ClusterSummary {
	l.mu.RLock()
	defer l.mu.RUnlock()
	now := l.now()

	nodeMap := make(map[string]*NodeSummary)
	for node, ram := range l.nodeRAM {
		nodeMap[node] = &NodeSummary{Node: node, TotalRAMMB: ram}
	}

	var cs ClusterSummary
	for _, e := range l.entries {
		ns, ok := nodeMap[e.Node]
		if !ok {
			ns = &NodeSummary{Node: e.Node}
			nodeMap[e.Node] = ns
		}
		ns.ReservedRAMMB += e.RAMMB
		ns.ReservedVRAMMB += e.VRAMMB
		cs.TotalReservedMB += e.RAMMB
		cs.TotalVRAMMB += e.VRAMMB
		if e.ClassifyLiveness(now, l.limits) != LivenessActive {
			ns.StaleEntries++
			cs.StaleEntries++
		} else {
			ns.ActiveEntries++
			cs.ActiveEntries++
		}
	}

	for _, ns := range nodeMap {
		if ns.TotalRAMMB > 0 {
			ns.UtilizationPct = float64(ns.ReservedRAMMB) / float64(ns.TotalRAMMB) * 100
		}
		cs.Nodes = append(cs.Nodes, *ns)
	}
	sort.Slice(cs.Nodes, func(i, j int) bool { return cs.Nodes[i].Node < cs.Nodes[j].Node })
	return cs
}

// Entries returns all current reservation entries.
func (l *Ledger) Entries() []Entry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Entry, 0, len(l.entries))
	for _, e := range l.entries {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// EntriesForNode returns reservations on a specific node.
func (l *Ledger) EntriesForNode(node string) []Entry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []Entry
	for _, e := range l.entries {
		if e.Node == node {
			out = append(out, *e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Metrics returns ledger-level metrics.
type Metrics struct {
	TotalReservedMB  int64 `json:"total_reserved_mb"`
	TotalReleasedMB  int64 `json:"total_released_mb"`
	TotalReclaimedMB int64 `json:"total_reclaimed_mb"`
	ReserveFailures  int64 `json:"reserve_failures"`
	ActiveEntries    int   `json:"active_entries"`
}

func (l *Ledger) Metrics() Metrics {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return Metrics{
		TotalReservedMB:  l.totalReserved,
		TotalReleasedMB:  l.totalReleased,
		TotalReclaimedMB: l.totalReclaimed,
		ReserveFailures:  l.reserveFailures,
		ActiveEntries:    len(l.entries),
	}
}
