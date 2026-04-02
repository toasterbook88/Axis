package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

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
// Repeated failures extend the expiry via exponential back-off.
type TombstoneEntry struct {
	TaskPattern string    `json:"task_pattern"`
	NodeName    string    `json:"node_name"`
	FailCount   int       `json:"fail_count"`
	LastFailure time.Time `json:"last_failure"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// tombstoneBaseExpiry is the initial cooldown after a single failure.
const tombstoneBaseExpiry = 24 * time.Hour

// tombstoneMaxExpiry caps the exponential back-off.
const tombstoneMaxExpiry = 7 * 24 * time.Hour

type ClusterState struct {
	Nodes      map[string]NodeState      `json:"nodes"`
	Tombstones map[string]TombstoneEntry `json:"tombstones,omitempty"` // key: "taskPattern@nodeName"
	Decisions  []string                  `json:"recent_decisions"`     // last 20 for context
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

	mutated := false
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
	return &ClusterState{Nodes: make(map[string]NodeState)}
}

func (s *ClusterState) Save() error {
	if s.Nodes == nil {
		s.Nodes = make(map[string]NodeState)
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

// tombstoneKey produces the map key for a task+node combination.
func tombstoneKey(taskPattern, nodeName string) string {
	return taskPattern + "@" + nodeName
}

// RecordFailure registers (or escalates) a tombstone for the given task pattern
// on the specified node. Expiry uses exponential back-off: 24h base, doubling
// per consecutive failure, capped at 7 days.
func (s *ClusterState) RecordFailure(taskPattern, nodeName string) error {
	if s.Tombstones == nil {
		s.Tombstones = make(map[string]TombstoneEntry)
	}
	key := tombstoneKey(taskPattern, nodeName)
	now := time.Now().UTC()

	entry, exists := s.Tombstones[key]
	if !exists {
		entry = TombstoneEntry{
			TaskPattern: taskPattern,
			NodeName:    nodeName,
		}
	}
	entry.FailCount++
	entry.LastFailure = now

	// Exponential back-off: base * 2^(failCount-1), capped
	expiry := tombstoneBaseExpiry
	for i := 1; i < entry.FailCount; i++ {
		expiry *= 2
		if expiry > tombstoneMaxExpiry {
			expiry = tombstoneMaxExpiry
			break
		}
	}
	entry.ExpiresAt = now.Add(expiry)
	s.Tombstones[key] = entry

	return s.Save()
}

// IsTombstoned returns true if the given task pattern is currently blacklisted
// on the specified node (i.e., the tombstone has not expired).
func (s *ClusterState) IsTombstoned(taskPattern, nodeName string) bool {
	if s == nil || s.Tombstones == nil {
		return false
	}
	key := tombstoneKey(taskPattern, nodeName)
	entry, exists := s.Tombstones[key]
	if !exists {
		return false
	}
	return time.Now().UTC().Before(entry.ExpiresAt)
}

// PruneTombstones removes all expired tombstone entries and saves if any were
// removed. Returns the number of entries pruned.
func (s *ClusterState) PruneTombstones() (int, error) {
	if s == nil || len(s.Tombstones) == 0 {
		return 0, nil
	}
	now := time.Now().UTC()
	pruned := 0
	for key, entry := range s.Tombstones {
		if now.After(entry.ExpiresAt) || now.Equal(entry.ExpiresAt) {
			delete(s.Tombstones, key)
			pruned++
		}
	}
	if pruned > 0 {
		return pruned, s.Save()
	}
	return 0, nil
}

// ClearTombstone removes a specific tombstone entry, allowing the task to be
// placed on the node again. Returns true if an entry was actually removed.
func (s *ClusterState) ClearTombstone(taskPattern, nodeName string) (bool, error) {
	if s == nil || s.Tombstones == nil {
		return false, nil
	}
	key := tombstoneKey(taskPattern, nodeName)
	if _, exists := s.Tombstones[key]; !exists {
		return false, nil
	}
	delete(s.Tombstones, key)
	return true, s.Save()
}
