package placement

import (
	"fmt"

	"github.com/toasterbook88/axis/internal/models"
)

// SelectBestNode runs the full placement pipeline: filter → rank → select.
// Returns a PlacementDecision with OK=false if no nodes qualify.
// Reasoning is diagnostic: on failure it explains why each node was excluded;
// on success it explains why the winner was chosen and how it compares.
func SelectBestNode(reqs models.TaskRequirements, nodes []models.NodeFacts) models.PlacementDecision {
	candidates := FilterCandidates(reqs, nodes)
	if len(candidates) == 0 {
		return buildFailureDecision(reqs, nodes)
	}

	ranked := RankCandidates(candidates)
	best := ranked[0]

	decision := models.PlacementDecision{
		Node: best.Name,
		OK:   true,
	}

	// Resource reasoning
	if best.Resources != nil {
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("%dMB free RAM (of %dMB total)", best.Resources.RAMFreeMB, best.Resources.RAMTotalMB))
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("pressure: %s", best.Resources.Pressure))
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("%d CPU cores", best.Resources.CPUCores))
	}

	// Tool match
	if reqs.RequiredTool != "" {
		for _, t := range best.Tools {
			if t.Name == reqs.RequiredTool {
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

	// Comparative: runner-up delta
	if len(ranked) > 1 {
		runnerUp := ranked[1]
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("selected from %d eligible nodes", len(ranked)))
		if runnerUp.Resources != nil && best.Resources != nil {
			delta := best.Resources.RAMFreeMB - runnerUp.Resources.RAMFreeMB
			if delta > 0 {
				decision.Reasoning = append(decision.Reasoning,
					fmt.Sprintf("runner-up %q has %dMB less free RAM", runnerUp.Name, delta))
			} else if delta == 0 {
				decision.Reasoning = append(decision.Reasoning,
					fmt.Sprintf("tied with %q on RAM; won by name ordering", runnerUp.Name))
			}
		}
	}

	return decision
}

// buildFailureDecision explains why every node was excluded.
func buildFailureDecision(reqs models.TaskRequirements, nodes []models.NodeFacts) models.PlacementDecision {
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
		// Node is complete but failed filter
		if reqs.MinFreeRAMMB > 0 && (n.Resources == nil || n.Resources.RAMFreeMB < reqs.MinFreeRAMMB) {
			actual := int64(0)
			if n.Resources != nil {
				actual = n.Resources.RAMFreeMB
			}
			d.Reasoning = append(d.Reasoning,
				fmt.Sprintf("  %s: need %dMB free RAM, have %dMB (short %dMB)",
					n.Name, reqs.MinFreeRAMMB, actual, reqs.MinFreeRAMMB-actual))
			continue
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

	return d
}

