package placement

import (
	"fmt"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

// SelectBestNode runs the full placement pipeline: filter → rank → select.
// Reasoning is diagnostic: on failure it explains why each node was excluded;
// on success it explains fit score, locality, and runner-up comparison.
func SelectBestNode(reqs models.TaskRequirements, nodes []models.NodeFacts, st *state.ClusterState) models.PlacementDecision {
	candidates := FilterCandidates(reqs, nodes, st)
	if len(candidates) == 0 {
		return buildFailureDecision(reqs, nodes, st)
	}

	ranked := RankCandidates(candidates, reqs, st)
	best := ranked[0]
	local := models.IsLocalNode(best)

	decision := models.PlacementDecision{
		Node:     best.Name,
		OK:       true,
		FitScore: ComputeFitScore(best, local, st),
		IsLocal:  local,
	}

	// Fit score summary
	fitLabel := fitLabel(decision.FitScore)
	decision.Reasoning = append(decision.Reasoning,
		fmt.Sprintf("LLM fit: %d/100 (%s)", decision.FitScore, fitLabel))

	// Locality
	if local {
		decision.Reasoning = append(decision.Reasoning, "local node (no SSH hop)")
	} else {
		decision.Reasoning = append(decision.Reasoning, "remote node (via SSH)")
	}

	// Resource details
	if best.Resources != nil {
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("%dMB free RAM (of %dMB total)", best.Resources.RAMFreeMB, best.Resources.RAMTotalMB))
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("pressure: %s", best.Resources.Pressure))
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("%d CPU cores", best.Resources.CPUCores))
		if len(best.Resources.GPUs) > 0 {
			decision.Reasoning = append(decision.Reasoning,
				fmt.Sprintf("GPU: %s", strings.Join(best.Resources.GPUs, ", ")))
		}
	}

	// Tool match
	if reqs.RequiredTool != "" {
		for _, t := range best.Tools {
			if strings.EqualFold(t.Name, reqs.RequiredTool) {
				decision.Tool = t.Name
				ver := t.Version
				if ver == "" {
					ver = "detected"
				}
				decision.Reasoning = append(decision.Reasoning,
					fmt.Sprintf("required tool %q available (version: %s)", t.Name, ver))
				break
			}
		}
	}

	// Runner-up comparison
	if len(ranked) > 1 {
		runnerUp := ranked[1]
		ruLocal := models.IsLocalNode(runnerUp)
		ruScore := ComputeFitScore(runnerUp, ruLocal, st)
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("selected from %d eligible nodes", len(ranked)))
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("runner-up %q scored %d/100", runnerUp.Name, ruScore))
	}

	return decision
}

// buildFailureDecision explains why every node was excluded.
func buildFailureDecision(reqs models.TaskRequirements, nodes []models.NodeFacts, st *state.ClusterState) models.PlacementDecision {
	d := models.PlacementDecision{OK: false}

	if len(nodes) == 0 {
		d.Reasoning = []string{"no nodes in cluster"}
		return d
	}

	d.Reasoning = []string{fmt.Sprintf("0 of %d nodes qualify", len(nodes))}

	for _, n := range nodes {
		if n.Status != models.StatusComplete {
			d.Reasoning = append(d.Reasoning,
				fmt.Sprintf("  %s: excluded (status: %s)", n.Name, n.Status))
			continue
		}
		if reqs.MinFreeRAMMB > 0 {
			actual := int64(0)
			adjusted := int64(0)
			if n.Resources != nil {
				actual = n.Resources.RAMFreeMB
				adjusted = freeRAMWithState(n, st)
			}
			if adjusted < reqs.MinFreeRAMMB {
				d.Reasoning = append(d.Reasoning,
					fmt.Sprintf("  %s: need %dMB free RAM, have %dMB effective (base %dMB, short %dMB)",
						n.Name, reqs.MinFreeRAMMB, adjusted, actual, reqs.MinFreeRAMMB-adjusted))
				continue
			}
		}
		if reqs.RequiredTool != "" && !hasTool(n, reqs.RequiredTool) {
			available := make([]string, 0, len(n.Tools))
			for _, t := range n.Tools {
				available = append(available, t.Name)
			}
			d.Reasoning = append(d.Reasoning,
				fmt.Sprintf("  %s: missing required tool %q (has: %v)",
					n.Name, reqs.RequiredTool, available))
			continue
		}
	}

	// If heavy RAM was required, explain AXIS scope
	if reqs.MinFreeRAMMB >= 4096 {
		d.Reasoning = append(d.Reasoning,
			"note: AXIS targets small assistive models, not 70B+ inference")
	}

	return d
}

func fitLabel(score int) string {
	switch {
	case score >= 75:
		return "excellent for small models"
	case score >= 50:
		return "good for small models"
	case score >= 25:
		return "adequate"
	default:
		return "limited"
	}
}
