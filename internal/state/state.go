package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type NodeState struct {
	ReservedMB   int64     `json:"reserved_mb"`
	LastTask     string    `json:"last_task"`
	LastPlacedAt time.Time `json:"last_placed_at"`
	ActiveTasks  int       `json:"active_tasks"`
}

type ClusterState struct {
	Nodes     map[string]NodeState `json:"nodes"`
	Decisions []string             `json:"recent_decisions"` // last 20 for context
	UpdatedAt time.Time            `json:"updated_at"`
}

func Path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".axis", "state.json")
}

func Load() (*ClusterState, error) {
	data, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return &ClusterState{Nodes: make(map[string]NodeState)}, nil
		}
		return nil, err
	}
	var s ClusterState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	// expire reservations older than 45 min
	if s.Nodes != nil {
		for name, ns := range s.Nodes {
			if time.Since(ns.LastPlacedAt) > 45*time.Minute {
				delete(s.Nodes, name)
			}
		}
	} else {
		s.Nodes = make(map[string]NodeState)
	}
	return &s, nil
}

func (s *ClusterState) Save() error {
	if s.Nodes == nil {
		s.Nodes = make(map[string]NodeState)
	}
	s.UpdatedAt = time.Now().UTC()
	data, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(Path(), data, 0644)
}

func (s *ClusterState) RecordPlacement(node string, estRAMMB int64, taskDesc string) {
	if s.Nodes == nil {
		s.Nodes = make(map[string]NodeState)
	}
	ns := s.Nodes[node]
	ns.ReservedMB += estRAMMB
	ns.LastTask = taskDesc
	ns.LastPlacedAt = time.Now().UTC()
	ns.ActiveTasks++
	s.Nodes[node] = ns

	// keep last 20 decisions
	s.Decisions = append(s.Decisions, node+": "+taskDesc)
	if len(s.Decisions) > 20 {
		s.Decisions = s.Decisions[len(s.Decisions)-20:]
	}
}
