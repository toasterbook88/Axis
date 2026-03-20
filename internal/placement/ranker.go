// Package placement implements deterministic task placement logic.
// Phase 2: read-only, advisory — does NOT execute tasks.
package placement

import (
	"sort"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

// pressureRank maps pressure strings to sort-order integers.
// Lower value = better (less pressure).
func pressureRank(p string) int {
	switch strings.ToLower(p) {
	case "none":
		return 0
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	default:
		return 4
	}
}

// FilterCandidates returns nodes that meet all task requirements.
// Rules (all must pass):
//   - Status must be complete
//   - If MinFreeRAMMB > 0, node must have resources with enough free RAM
//   - If RequiredTool is set, node must have a tool with that name
func FilterCandidates(reqs models.TaskRequirements, nodes []models.NodeFacts, st *state.ClusterState) []models.NodeFacts {
	var out []models.NodeFacts
	for _, n := range nodes {
		if n.Status != models.StatusComplete {
			if reqs.RequiredTool != "ollama" || !isOllamaBootstrapPossible(n) {
				continue
			}
		}
		if reqs.MinFreeRAMMB > 0 {
			adjustedFree := freeRAMWithState(n, st)
			if n.Resources == nil || adjustedFree < reqs.MinFreeRAMMB {
				continue
			}
		}
		if reqs.RequiredTool == "ollama" && !ollamaIsReady(n) && !isOllamaBootstrapPossible(n) {
			continue
		}
		if reqs.RequiredTool != "" && reqs.RequiredTool != "ollama" {
			if !hasTool(n, reqs.RequiredTool) {
				continue
			}
		}
		out = append(out, n)
	}
	return out
}

// RankCandidates sorts nodes deterministically.
// Priority order:
//  1. Lowest RAM pressure (none < low < medium < high)
//  2. GPU present
//  3. Highest effective headroom (free-with-state - requirement)
//  4. Highest raw free RAM
//  5. Node name ascending (stable tiebreak)
func RankCandidates(candidates []models.NodeFacts, reqs models.TaskRequirements, st *state.ClusterState) []models.NodeFacts {
	ranked := make([]models.NodeFacts, len(candidates))
	copy(ranked, candidates)

	sort.SliceStable(ranked, func(i, j int) bool {
		pi := pressureOf(ranked[i])
		pj := pressureOf(ranked[j])
		if pi != pj {
			return pressureRank(pi) < pressureRank(pj)
		}

		gi := gpuPresent(ranked[i])
		gj := gpuPresent(ranked[j])
		if gi != gj {
			return gi && !gj
		}

		hi := headroom(ranked[i], st, reqs)
		hj := headroom(ranked[j], st, reqs)
		if hi != hj {
			return hi > hj
		}

		ri := freeRAM(ranked[i])
		rj := freeRAM(ranked[j])
		if ri != rj {
			return ri > rj
		}

		return ranked[i].Name < ranked[j].Name
	})

	return ranked
}

func hasTool(n models.NodeFacts, name string) bool {
	for _, t := range n.Tools {
		if strings.EqualFold(t.Name, name) {
			return true
		}
	}
	return false
}

func pressureOf(n models.NodeFacts) string {
	if n.Resources == nil {
		return "high"
	}
	return n.Resources.Pressure
}

func freeRAM(n models.NodeFacts) int64 {
	if n.Resources == nil {
		return 0
	}
	return n.Resources.RAMFreeMB
}

func gpuPresent(n models.NodeFacts) bool {
	if n.Resources == nil {
		return false
	}
	return len(n.Resources.GPUs) > 0
}

// ComputeFitScore returns 0-100 indicating small-model suitability.
// Scoring breakdown:
//   - Free RAM:    up to 30 pts (1 pt per 256MB, capped at 30)
//   - Pressure:    up to 25 pts (none=25, low=20, medium=10, high=0)
//   - GPU present: +25 pts
//   - CPU cores:   up to 10 pts (1 pt per core, capped at 10)
//   - Local node:  +10 pts (no SSH hop = lower latency)
//
// Max: 30+25+25+10+10 = 100
func ComputeFitScore(n models.NodeFacts, isLocal bool, st *state.ClusterState) int {
	if n.Resources == nil {
		return 0
	}

	score := 0

	// Free RAM: 1pt per 256MB, cap 30
	adjusted := freeRAMWithState(n, st)
	ramPts := int(adjusted / 256)
	if ramPts > 30 {
		ramPts = 30
	}
	score += ramPts

	// Pressure
	switch strings.ToLower(n.Resources.Pressure) {
	case "none":
		score += 25
	case "low":
		score += 20
	case "medium":
		score += 10
	}

	// GPU
	if len(n.Resources.GPUs) > 0 {
		score += 25
	}

	// CPU: 1pt per core, cap 10
	cpuPts := n.Resources.CPUCores
	if cpuPts > 10 {
		cpuPts = 10
	}
	score += cpuPts

	// Local bonus
	if isLocal {
		score += 10
	}

	if score > 100 {
		score = 100
	}
	return score
}

func freeRAMWithState(n models.NodeFacts, st *state.ClusterState) int64 {
	if n.Resources == nil {
		return 0
	}
	committed := int64(0)
	if st != nil && st.Nodes != nil {
		if ns, ok := st.Nodes[n.Name]; ok {
			committed = ns.ReservedMB
		}
	}
	effective := n.Resources.RAMFreeMB - committed
	if effective < 0 {
		return 0
	}
	return effective
}

func headroom(n models.NodeFacts, st *state.ClusterState, reqs models.TaskRequirements) int64 {
	return freeRAMWithState(n, st) - reqs.MinFreeRAMMB
}

func ollamaIsReady(n models.NodeFacts) bool {
	return n.Ollama != nil && n.Ollama.Running
}

func isOllamaBootstrapPossible(n models.NodeFacts) bool {
	return n.Ollama != nil && n.Ollama.Installed
}
