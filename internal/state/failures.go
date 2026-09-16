package state

import (
	"sort"
	"strings"
	"time"
)

type TombstoneEntry struct {
	TaskPattern string    `json:"task_pattern"`
	NodeName    string    `json:"node_name"`
	FailCount   int       `json:"fail_count"`
	LastFailure time.Time `json:"last_failure"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type PruneReport struct {
	Nodes        int
	Observations int
	TaskHistory  int
	Failures     int
	Tombstones   int
	Decisions    int
	Blocked      []PruneBlock
}

// PruneBlock describes live execution state that prevents a node prune.
// TrackingRecords counts the per-execution maps in NodeState in addition to
// ActiveExecs, so partially inconsistent state is still treated conservatively.
type PruneBlock struct {
	Node            string
	ReservedMB      int64
	ActiveTasks     int
	ActiveExecs     int
	TrackingRecords int
}

// Empty reports whether the prune removed nothing. Blocked nodes do not count
// as changes: PruneNodes returns without mutating anything when Blocked is set.
func (s *ClusterState) PruneBlockers(targets map[string]bool) []PruneBlock {
	if s == nil || len(targets) == 0 {
		return nil
	}
	norm := normalizedNodeTargets(targets)
	var blocked []PruneBlock
	for name, ns := range s.Nodes {
		if !matchesNodeTarget(norm, name) || !nodeStateHasExecutionState(ns) {
			continue
		}
		blocked = append(blocked, PruneBlock{
			Node:        name,
			ReservedMB:  ns.ReservedMB,
			ActiveTasks: ns.ActiveTasks,
			ActiveExecs: len(ns.ActiveExecs),
			TrackingRecords: len(ns.ExecReservationsMB) +
				len(ns.ExecHeartbeatAt) +
				len(ns.ExecOwnerPID) +
				len(ns.ExecOwnerSurface) +
				len(ns.ExecOwnerLabel) +
				len(ns.ExecOrigin),
		})
	}
	sort.Slice(blocked, func(i, j int) bool {
		left, right := strings.ToLower(blocked[i].Node), strings.ToLower(blocked[j].Node)
		if left == right {
			return blocked[i].Node < blocked[j].Node
		}
		return left < right
	})
	return blocked
}

// PruneNodes removes state records belonging to the named target nodes.
// targets holds nodes to REMOVE. An empty map removes nothing.
// It does not save; callers persist via Update.
//
// Node names are matched case-insensitively because failures.HashScope
// normalizes case, so an exact-match prune would leave failure records that
// the failure system still considers live for the pruned node.
func (s *ClusterState) PruneNodes(targets map[string]bool) PruneReport {
	var rep PruneReport
	if len(targets) == 0 {
		return rep
	}
	rep.Blocked = s.PruneBlockers(targets)
	if len(rep.Blocked) > 0 {
		return rep
	}

	norm := normalizedNodeTargets(targets)
	hit := func(name string) bool {
		return matchesNodeTarget(norm, name)
	}

	for name := range s.Nodes {
		if hit(name) {
			delete(s.Nodes, name)
			rep.Nodes++
		}
	}

	for key, obs := range s.Observations {
		if hit(obs.Scope.Node) {
			delete(s.Observations, key)
			rep.Observations++
		}
	}

	kept := s.TaskHistory[:0]
	for _, r := range s.TaskHistory {
		if hit(r.Node) {
			rep.TaskHistory++
			continue
		}
		kept = append(kept, r)
	}
	s.TaskHistory = kept

	// Node-scoped failure records outlive the node they blame. Leaving them
	// keeps a pruned node's tombstones influencing placement exclusions.
	// Records with an empty Scope.Node are cluster-wide and are never pruned.
	for key, f := range s.Failures {
		if hit(f.Scope.Node) {
			delete(s.Failures, key)
			rep.Failures++
		}
	}

	for key, t := range s.Tombstones {
		if hit(t.NodeName) {
			delete(s.Tombstones, key)
			rep.Tombstones++
		}
	}

	keptDecisions := s.Decisions[:0]
	for _, decision := range s.Decisions {
		node, ok := DecisionNodeName(decision)
		if ok && hit(node) {
			rep.Decisions++
			continue
		}
		keptDecisions = append(keptDecisions, decision)
	}
	s.Decisions = keptDecisions

	return rep
}

// LoadUnlocked reads the state file without acquiring the lock and without
// running migrations. Callers holding the lock via persist.LockFile MUST use
// this: state.Load persists migrations through Update and would deadlock.
//
// Pair it with MigratePending before any Save. Save stamps the current schema
// version unconditionally, so writing state that LoadUnlocked returned without
// migrating it would mark the file current while leaving legacy records
// unconverted — and every later Load would then skip them.
