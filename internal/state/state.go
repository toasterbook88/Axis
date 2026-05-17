package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/failures"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/persist"
)

type NodeState struct {
	ReservedMB         int64                             `json:"reserved_mb"`
	LastTask           string                            `json:"last_task"`
	LastPlacedAt       time.Time                         `json:"last_placed_at"`
	ActiveTasks        int                               `json:"active_tasks"`
	ActiveExecs        []string                          `json:"active_execs,omitempty"`
	ExecReservationsMB map[string]int64                  `json:"exec_reservations_mb,omitempty"`
	ExecHeartbeatAt    map[string]time.Time              `json:"exec_heartbeat_at,omitempty"`
	ExecOwnerPID       map[string]int                    `json:"exec_owner_pid,omitempty"`
	ExecOwnerSurface   map[string]string                 `json:"exec_owner_surface,omitempty"`
	ExecOwnerLabel     map[string]string                 `json:"exec_owner_label,omitempty"`
	ExecOrigin         map[string]models.ExecutionOrigin `json:"exec_origin,omitempty"`
}

type ExecutionOwner struct {
	Surface string
	Label   string
	Origin  models.ExecutionOrigin
}

// TombstoneEntry records a task-node failure for the immune system.
// Deprecated: Migrated to failures.Store.
type TombstoneEntry struct {
	TaskPattern string    `json:"task_pattern"`
	NodeName    string    `json:"node_name"`
	FailCount   int       `json:"fail_count"`
	LastFailure time.Time `json:"last_failure"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type ClusterState struct {
	Nodes        map[string]NodeState                   `json:"nodes"`
	Tombstones   map[string]TombstoneEntry              `json:"tombstones,omitempty"` // legacy
	Failures     failures.Store                         `json:"failures,omitempty"`
	Observations map[string]models.ExecutionObservation `json:"observations,omitempty"`
	Decisions    []string                               `json:"recent_decisions"` // last 20 for context
	UpdatedAt    time.Time                              `json:"updated_at"`
}

var quarantineCorruptStateFile = persist.QuarantineCorruptFile
var execOwnerAlive = processAlive

const (
	staleReservationReclaimAfter = 45 * time.Minute
	staleReservationHardExpiry   = 24 * time.Hour
	execHeartbeatStaleAfter      = 2 * time.Minute
)

func Path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".axis", "state.json")
}

func Load() (*ClusterState, error) {
	path := Path()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return emptyClusterState(), nil
		}
		return nil, err
	}
	var s ClusterState
	if err := json.Unmarshal(data, &s); err != nil {
		warnErr := quarantineCorruptStateFile(path, err)
		if _, ok := warnErr.(*persist.RecoveryWarning); ok {
			return emptyClusterState(), fmt.Errorf("recovered local AXIS state: %w", warnErr)
		}
		return nil, warnErr
	}
	if s.Nodes == nil {
		s.Nodes = make(map[string]NodeState)
	}
	if s.Failures == nil {
		s.Failures = failures.NewStore()
	}
	if s.Observations == nil {
		s.Observations = make(map[string]models.ExecutionObservation)
	}

	mutated := false
	if len(s.Tombstones) > 0 {
		for _, t := range s.Tombstones {
			// Migrate to {Node}-only scope so placement queries on {Node, Workload}
			// can match via the broad-scope Match semantics.  The legacy TaskPattern
			// was a free-form string (not a canonical tool name), so including it as
			// Tool would prevent placement from seeing these records.
			scope := models.FailureScope{
				Node: t.NodeName,
			}
			class := models.FailureUnknown
			lowerPat := strings.ToLower(t.TaskPattern)
			if strings.Contains(lowerPat, "ollama") || strings.Contains(lowerPat, "llama") || strings.Contains(lowerPat, "mlx") {
				class = models.FailureBackendMisfit
			} else {
				class = models.FailureExecCrash
			}
			entry := models.FailureRecord{
				ID:         "legacy-" + strconv.FormatInt(t.LastFailure.UnixNano(), 16),
				Class:      class,
				Scope:      scope,
				OccurredAt: t.LastFailure,
				ExpiresAt:  t.ExpiresAt,
				Count:      t.FailCount,
				Reason:     "migrated from legacy tombstone",
			}
			s.Failures[failures.HashScope(scope)] = entry
		}
		s.Tombstones = nil
		mutated = true
	}

	pruned := s.Failures.Prune()
	if pruned > 0 {
		mutated = true
	}

	now := time.Now().UTC()
	for name, ns := range s.Nodes {
		legacyHeartbeatMode := len(ns.ActiveExecs) > 0 && len(ns.ExecHeartbeatAt) == 0
		ns, normalized := normalizeNodeStateExecTracking(ns, now)
		if normalized {
			mutated = true
		}

		if shouldDropAncientNodeState(now, ns, legacyHeartbeatMode) {
			delete(s.Nodes, name)
			mutated = true
			continue
		}

		// Legacy state written before explicit acquire/release had no exec IDs,
		// so any retained reservation is untrustworthy and should be discarded.
		if len(ns.ActiveExecs) == 0 && (ns.ActiveTasks > 0 || ns.ReservedMB > 0) {
			delete(s.Nodes, name)
			mutated = true
			continue
		}

		reclaimed, reclaimedAny := reclaimStaleReservation(now, ns, legacyHeartbeatMode)
		if reclaimedAny {
			ns = reclaimed
			mutated = true
		}

		if ns.ActiveTasks == 0 && ns.ReservedMB == 0 {
			delete(s.Nodes, name)
			mutated = true
			continue
		}

		s.Nodes[name] = ns
	}

	if mutated {
		if err := s.Save(); err != nil {
			return nil, err
		}
	}
	return &s, nil
}

func emptyClusterState() *ClusterState {
	return &ClusterState{
		Nodes:        make(map[string]NodeState),
		Failures:     failures.NewStore(),
		Observations: make(map[string]models.ExecutionObservation),
	}
}

func shouldDropAncientNodeState(now time.Time, ns NodeState, legacyHeartbeatMode bool) bool {
	if ns.LastPlacedAt.IsZero() {
		return false
	}
	if len(ns.ActiveExecs) == 0 {
		return now.Sub(ns.LastPlacedAt) > staleReservationReclaimAfter
	}
	if !legacyHeartbeatMode {
		return false
	}
	return now.Sub(ns.LastPlacedAt) > staleReservationHardExpiry
}

func normalizeNodeStateExecTracking(ns NodeState, now time.Time) (NodeState, bool) {
	mutated := false

	if ns.ActiveTasks != len(ns.ActiveExecs) {
		ns.ActiveTasks = len(ns.ActiveExecs)
		mutated = true
	}

	if len(ns.ActiveExecs) == 0 {
		if len(ns.ExecReservationsMB) > 0 {
			ns.ExecReservationsMB = nil
			mutated = true
		}
		if len(ns.ExecHeartbeatAt) > 0 {
			ns.ExecHeartbeatAt = nil
			mutated = true
		}
		if len(ns.ExecOwnerPID) > 0 {
			ns.ExecOwnerPID = nil
			mutated = true
		}
		if len(ns.ExecOwnerSurface) > 0 {
			ns.ExecOwnerSurface = nil
			mutated = true
		}
		if len(ns.ExecOwnerLabel) > 0 {
			ns.ExecOwnerLabel = nil
			mutated = true
		}
		if len(ns.ExecOrigin) > 0 {
			ns.ExecOrigin = nil
			mutated = true
		}
		return ns, mutated
	}

	reservations := make(map[string]int64, len(ns.ActiveExecs))
	var reservedSum int64
	var missingReservations []string
	for _, execID := range ns.ActiveExecs {
		if amount, ok := ns.ExecReservationsMB[execID]; ok && amount > 0 {
			reservations[execID] = amount
			reservedSum += amount
			continue
		}
		missingReservations = append(missingReservations, execID)
	}
	if len(missingReservations) > 0 {
		remainder := ns.ReservedMB - reservedSum
		if remainder < 0 {
			remainder = 0
		}
		base := int64(0)
		extra := int64(0)
		if len(missingReservations) > 0 {
			base = remainder / int64(len(missingReservations))
			extra = remainder % int64(len(missingReservations))
		}
		for _, execID := range missingReservations {
			amount := base
			if extra > 0 {
				amount++
				extra--
			}
			reservations[execID] = amount
			reservedSum += amount
		}
	}
	if !sameExecReservationMap(ns.ExecReservationsMB, reservations) {
		ns.ExecReservationsMB = reservations
		mutated = true
	}

	heartbeats := make(map[string]time.Time, len(ns.ActiveExecs))
	fallback := ns.LastPlacedAt
	if fallback.IsZero() {
		fallback = now
	}
	for _, execID := range ns.ActiveExecs {
		hb, ok := ns.ExecHeartbeatAt[execID]
		if !ok || hb.IsZero() {
			hb = fallback
		}
		heartbeats[execID] = hb.UTC()
	}
	if !sameExecHeartbeatMap(ns.ExecHeartbeatAt, heartbeats) {
		ns.ExecHeartbeatAt = heartbeats
		mutated = true
	}

	owners := make(map[string]int, len(ns.ActiveExecs))
	for _, execID := range ns.ActiveExecs {
		if pid, ok := ns.ExecOwnerPID[execID]; ok && pid > 0 {
			owners[execID] = pid
		}
	}
	if !sameExecOwnerMap(ns.ExecOwnerPID, owners) {
		if len(owners) == 0 {
			ns.ExecOwnerPID = nil
		} else {
			ns.ExecOwnerPID = owners
		}
		mutated = true
	}

	ownerSurfaces := make(map[string]string, len(ns.ActiveExecs))
	for _, execID := range ns.ActiveExecs {
		if surface, ok := ns.ExecOwnerSurface[execID]; ok && strings.TrimSpace(surface) != "" {
			ownerSurfaces[execID] = strings.TrimSpace(surface)
		}
	}
	if !sameExecStringMap(ns.ExecOwnerSurface, ownerSurfaces) {
		if len(ownerSurfaces) == 0 {
			ns.ExecOwnerSurface = nil
		} else {
			ns.ExecOwnerSurface = ownerSurfaces
		}
		mutated = true
	}

	ownerLabels := make(map[string]string, len(ns.ActiveExecs))
	for _, execID := range ns.ActiveExecs {
		if label, ok := ns.ExecOwnerLabel[execID]; ok && strings.TrimSpace(label) != "" {
			ownerLabels[execID] = strings.TrimSpace(label)
		}
	}
	if !sameExecStringMap(ns.ExecOwnerLabel, ownerLabels) {
		if len(ownerLabels) == 0 {
			ns.ExecOwnerLabel = nil
		} else {
			ns.ExecOwnerLabel = ownerLabels
		}
		mutated = true
	}

	origins := make(map[string]models.ExecutionOrigin, len(ns.ActiveExecs))
	for _, execID := range ns.ActiveExecs {
		if origin, ok := ns.ExecOrigin[execID]; ok {
			origin = origin.Normalized()
			if !origin.IsZero() {
				origins[execID] = origin
			}
		}
	}
	if !sameExecOriginMap(ns.ExecOrigin, origins) {
		if len(origins) == 0 {
			ns.ExecOrigin = nil
		} else {
			ns.ExecOrigin = origins
		}
		mutated = true
	}

	if ns.ReservedMB != reservedSum {
		ns.ReservedMB = reservedSum
		mutated = true
	}

	return ns, mutated
}

func reclaimStaleReservation(now time.Time, ns NodeState, legacyHeartbeatMode bool) (NodeState, bool) {
	if ns.LastPlacedAt.IsZero() || len(ns.ActiveExecs) == 0 {
		return ns, false
	}

	reclaimed, changed := reclaimDeadOwnerExecutions(ns)
	if changed {
		ns = reclaimed
		if len(ns.ActiveExecs) == 0 {
			return ns, true
		}
	}

	if !legacyHeartbeatMode {
		reclaimed, heartbeatChanged := reclaimHeartbeatStaleExecutions(now, ns)
		return reclaimed, changed || heartbeatChanged
	}

	if now.Sub(ns.LastPlacedAt) <= staleReservationReclaimAfter {
		return ns, changed
	}

	capMB := staleReservationCapMB(ns)
	if capMB <= 0 || ns.ReservedMB <= capMB {
		return ns, changed
	}
	ns.ReservedMB = capMB
	return ns, true
}

func staleReservationCapMB(ns NodeState) int64 {
	if len(ns.ActiveExecs) == 0 {
		return 0
	}
	return int64(len(ns.ActiveExecs)) * models.MinimumSystemReserveMB
}

func reclaimHeartbeatStaleExecutions(now time.Time, ns NodeState) (NodeState, bool) {
	if len(ns.ActiveExecs) == 0 {
		return ns, false
	}

	var (
		activeExecs []string
		changed     bool
	)
	reservations := make(map[string]int64, len(ns.ExecReservationsMB))
	heartbeats := make(map[string]time.Time, len(ns.ExecHeartbeatAt))
	owners := make(map[string]int, len(ns.ExecOwnerPID))
	ownerSurfaces := make(map[string]string, len(ns.ExecOwnerSurface))
	ownerLabels := make(map[string]string, len(ns.ExecOwnerLabel))
	origins := make(map[string]models.ExecutionOrigin, len(ns.ExecOrigin))

	for _, execID := range ns.ActiveExecs {
		hb, ok := ns.ExecHeartbeatAt[execID]
		if !ok || hb.IsZero() || now.Sub(hb) > execHeartbeatStaleAfter {
			changed = true
			continue
		}
		activeExecs = append(activeExecs, execID)
		if amount, ok := ns.ExecReservationsMB[execID]; ok && amount > 0 {
			reservations[execID] = amount
		}
		heartbeats[execID] = hb
		if pid, ok := ns.ExecOwnerPID[execID]; ok && pid > 0 {
			owners[execID] = pid
		}
		if surface, ok := ns.ExecOwnerSurface[execID]; ok && strings.TrimSpace(surface) != "" {
			ownerSurfaces[execID] = strings.TrimSpace(surface)
		}
		if label, ok := ns.ExecOwnerLabel[execID]; ok && strings.TrimSpace(label) != "" {
			ownerLabels[execID] = strings.TrimSpace(label)
		}
		if origin, ok := ns.ExecOrigin[execID]; ok {
			origin = origin.Normalized()
			if !origin.IsZero() {
				origins[execID] = origin
			}
		}
	}

	if !changed {
		return ns, false
	}

	ns.ActiveExecs = activeExecs
	ns.ActiveTasks = len(activeExecs)
	if len(activeExecs) == 0 {
		ns.ExecReservationsMB = nil
		ns.ExecHeartbeatAt = nil
		ns.ExecOwnerPID = nil
		ns.ExecOwnerSurface = nil
		ns.ExecOwnerLabel = nil
		ns.ExecOrigin = nil
		ns.ReservedMB = 0
		return ns, true
	}

	ns.ExecReservationsMB = reservations
	ns.ExecHeartbeatAt = heartbeats
	if len(owners) == 0 {
		ns.ExecOwnerPID = nil
	} else {
		ns.ExecOwnerPID = owners
	}
	if len(ownerSurfaces) == 0 {
		ns.ExecOwnerSurface = nil
	} else {
		ns.ExecOwnerSurface = ownerSurfaces
	}
	if len(ownerLabels) == 0 {
		ns.ExecOwnerLabel = nil
	} else {
		ns.ExecOwnerLabel = ownerLabels
	}
	if len(origins) == 0 {
		ns.ExecOrigin = nil
	} else {
		ns.ExecOrigin = origins
	}
	ns.ReservedMB = sumExecReservations(reservations)
	return ns, true
}

func reclaimDeadOwnerExecutions(ns NodeState) (NodeState, bool) {
	if len(ns.ActiveExecs) == 0 || len(ns.ExecOwnerPID) == 0 {
		return ns, false
	}

	var (
		activeExecs []string
		changed     bool
	)
	reservations := make(map[string]int64, len(ns.ExecReservationsMB))
	heartbeats := make(map[string]time.Time, len(ns.ExecHeartbeatAt))
	owners := make(map[string]int, len(ns.ExecOwnerPID))
	ownerSurfaces := make(map[string]string, len(ns.ExecOwnerSurface))
	ownerLabels := make(map[string]string, len(ns.ExecOwnerLabel))
	origins := make(map[string]models.ExecutionOrigin, len(ns.ExecOrigin))
	aliveCache := make(map[int]bool)

	for _, execID := range ns.ActiveExecs {
		pid, hasOwner := ns.ExecOwnerPID[execID]
		if hasOwner && pid > 0 {
			alive, ok := aliveCache[pid]
			if !ok {
				alive = execOwnerAlive(pid)
				aliveCache[pid] = alive
			}
			if !alive {
				changed = true
				continue
			}
			owners[execID] = pid
		}

		activeExecs = append(activeExecs, execID)
		if amount, ok := ns.ExecReservationsMB[execID]; ok && amount > 0 {
			reservations[execID] = amount
		}
		if hb, ok := ns.ExecHeartbeatAt[execID]; ok && !hb.IsZero() {
			heartbeats[execID] = hb
		}
		if surface, ok := ns.ExecOwnerSurface[execID]; ok && strings.TrimSpace(surface) != "" {
			ownerSurfaces[execID] = strings.TrimSpace(surface)
		}
		if label, ok := ns.ExecOwnerLabel[execID]; ok && strings.TrimSpace(label) != "" {
			ownerLabels[execID] = strings.TrimSpace(label)
		}
		if origin, ok := ns.ExecOrigin[execID]; ok {
			origin = origin.Normalized()
			if !origin.IsZero() {
				origins[execID] = origin
			}
		}
	}

	if !changed {
		return ns, false
	}

	ns.ActiveExecs = activeExecs
	ns.ActiveTasks = len(activeExecs)
	if len(activeExecs) == 0 {
		ns.ExecReservationsMB = nil
		ns.ExecHeartbeatAt = nil
		ns.ExecOwnerPID = nil
		ns.ExecOwnerSurface = nil
		ns.ExecOwnerLabel = nil
		ns.ExecOrigin = nil
		ns.ReservedMB = 0
		return ns, true
	}

	ns.ExecReservationsMB = reservations
	ns.ExecHeartbeatAt = heartbeats
	if len(owners) == 0 {
		ns.ExecOwnerPID = nil
	} else {
		ns.ExecOwnerPID = owners
	}
	if len(ownerSurfaces) == 0 {
		ns.ExecOwnerSurface = nil
	} else {
		ns.ExecOwnerSurface = ownerSurfaces
	}
	if len(ownerLabels) == 0 {
		ns.ExecOwnerLabel = nil
	} else {
		ns.ExecOwnerLabel = ownerLabels
	}
	if len(origins) == 0 {
		ns.ExecOrigin = nil
	} else {
		ns.ExecOrigin = origins
	}
	ns.ReservedMB = sumExecReservations(reservations)
	return ns, true
}

func sameExecReservationMap(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for key, av := range a {
		if bv, ok := b[key]; !ok || av != bv {
			return false
		}
	}
	return true
}

func sameExecHeartbeatMap(a, b map[string]time.Time) bool {
	if len(a) != len(b) {
		return false
	}
	for key, av := range a {
		bv, ok := b[key]
		if !ok || !av.Equal(bv) {
			return false
		}
	}
	return true
}

func sameExecOwnerMap(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for key, av := range a {
		if bv, ok := b[key]; !ok || av != bv {
			return false
		}
	}
	return true
}

func sameExecStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, av := range a {
		if bv, ok := b[key]; !ok || av != bv {
			return false
		}
	}
	return true
}

func sameExecOriginMap(a, b map[string]models.ExecutionOrigin) bool {
	if len(a) != len(b) {
		return false
	}
	for key, av := range a {
		bv, ok := b[key]
		if !ok || av.Normalized() != bv.Normalized() {
			return false
		}
	}
	return true
}

func sumExecReservations(m map[string]int64) int64 {
	var total int64
	for _, amount := range m {
		if amount > 0 {
			total += amount
		}
	}
	return total
}

func (s *ClusterState) Save() error {
	if s.Nodes == nil {
		s.Nodes = make(map[string]NodeState)
	}
	if s.Failures == nil {
		s.Failures = failures.NewStore()
	}
	if s.Observations == nil {
		s.Observations = make(map[string]models.ExecutionObservation)
	}
	s.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return persist.WriteFileAtomic(path, data, 0o644)
}

func (s *ClusterState) RecordPlacement(node string, estRAMMB int64, taskDesc string) {
	s.Decisions = append(s.Decisions, node+": "+taskDesc)
	if len(s.Decisions) > 20 {
		s.Decisions = s.Decisions[len(s.Decisions)-20:]
	}
	_ = s.Save()
}
