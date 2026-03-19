// Package placement implements deterministic task placement logic.
// Phase 2: read-only, advisory — does NOT execute tasks.
package placement

import (
	"sort"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
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
func FilterCandidates(reqs models.TaskRequirements, nodes []models.NodeFacts) []models.NodeFacts {
	var out []models.NodeFacts
	for _, n := range nodes {
		if n.Status != models.StatusComplete {
			continue
		}
		if reqs.MinFreeRAMMB > 0 {
			if n.Resources == nil || n.Resources.RAMFreeMB < reqs.MinFreeRAMMB {
				continue
			}
		}
		if reqs.RequiredTool != "" {
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
//  2. Highest free RAM
//  3. Node name ascending (stable tiebreak)
func RankCandidates(candidates []models.NodeFacts) []models.NodeFacts {
	ranked := make([]models.NodeFacts, len(candidates))
	copy(ranked, candidates)

	sort.SliceStable(ranked, func(i, j int) bool {
		pi := pressureOf(ranked[i])
		pj := pressureOf(ranked[j])
		if pi != pj {
			return pressureRank(pi) < pressureRank(pj)
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
