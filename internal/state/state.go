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
	ReservedMB   int64     `json:"reserved_mb"`
	LastTask     string    `json:"last_task"`
	LastPlacedAt time.Time `json:"last_placed_at"`
	ActiveTasks  int       `json:"active_tasks"`
	ActiveExecs  []string  `json:"active_execs,omitempty"`
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
	Nodes      map[string]NodeState      `json:"nodes"`
	Tombstones map[string]TombstoneEntry `json:"tombstones,omitempty"` // legacy
	Failures   failures.Store            `json:"failures,omitempty"`
	Decisions  []string                  `json:"recent_decisions"` // last 20 for context
	UpdatedAt  time.Time                 `json:"updated_at"`
}

var quarantineCorruptStateFile = persist.QuarantineCorruptFile

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

	mutated := false
	if s.Tombstones != nil && len(s.Tombstones) > 0 {
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

	for name, ns := range s.Nodes {
		if time.Since(ns.LastPlacedAt) > 45*time.Minute {
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

		if ns.ActiveTasks != len(ns.ActiveExecs) {
			ns.ActiveTasks = len(ns.ActiveExecs)
			s.Nodes[name] = ns
			mutated = true
		}
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
		Nodes:    make(map[string]NodeState),
		Failures: failures.NewStore(),
	}
}

func (s *ClusterState) Save() error {
	if s.Nodes == nil {
		s.Nodes = make(map[string]NodeState)
	}
	if s.Failures == nil {
		s.Failures = failures.NewStore()
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
	return os.WriteFile(path, data, 0644)
}

func (s *ClusterState) RecordPlacement(node string, estRAMMB int64, taskDesc string) {
	_, _ = s.AcquireTask(node, taskDesc, estRAMMB)
}

func (s *ClusterState) AcquireTask(node, taskDesc string, estRAMMB int64) (string, error) {
	if s.Nodes == nil {
		s.Nodes = make(map[string]NodeState)
	}
	ns := s.Nodes[node]
	execID := time.Now().UTC().Format("20060102-150405.000000000") + "-" + node
	ns.ReservedMB += estRAMMB
	ns.LastTask = taskDesc
	ns.LastPlacedAt = time.Now().UTC()
	ns.ActiveTasks++
	ns.ActiveExecs = append(ns.ActiveExecs, execID)
	s.Nodes[node] = ns

	// keep last 20 decisions
	s.Decisions = append(s.Decisions, node+": "+taskDesc)
	if len(s.Decisions) > 20 {
		s.Decisions = s.Decisions[len(s.Decisions)-20:]
	}
	return execID, s.Save()
}

func (s *ClusterState) ReleaseTask(node, execID string, estRAMMB int64) error {
	if s.Nodes == nil {
		return nil
	}

	ns, ok := s.Nodes[node]
	if !ok {
		return nil
	}

	for i, id := range ns.ActiveExecs {
		if id == execID {
			ns.ActiveExecs = append(ns.ActiveExecs[:i], ns.ActiveExecs[i+1:]...)
			break
		}
	}

	ns.ReservedMB -= estRAMMB
	if ns.ReservedMB < 0 {
		ns.ReservedMB = 0
	}
	ns.ActiveTasks--
	if ns.ActiveTasks < 0 {
		ns.ActiveTasks = 0
	}

	if ns.ActiveTasks == 0 && ns.ReservedMB == 0 {
		delete(s.Nodes, node)
	} else {
		s.Nodes[node] = ns
	}

	return s.Save()
}
