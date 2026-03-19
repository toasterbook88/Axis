package placement

import (
	"fmt"

	"github.com/toasterbook88/axis/internal/models"
)

// SelectBestNode runs the full placement pipeline: filter → rank → select.
// Returns a PlacementDecision with OK=false if no nodes qualify.
func SelectBestNode(reqs models.TaskRequirements, nodes []models.NodeFacts) models.PlacementDecision {
	candidates := FilterCandidates(reqs, nodes)
	if len(candidates) == 0 {
		return models.PlacementDecision{
			OK:        false,
			Reasoning: []string{"no nodes meet task requirements"},
		}
	}

	ranked := RankCandidates(candidates)
	best := ranked[0]

	decision := models.PlacementDecision{
		Node: best.Name,
		OK:   true,
	}

	// Build reasoning
	if best.Resources != nil {
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("%dMB free RAM (of %dMB total)", best.Resources.RAMFreeMB, best.Resources.RAMTotalMB))
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("pressure: %s", best.Resources.Pressure))
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("%d CPU cores", best.Resources.CPUCores))
	}

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

	if len(ranked) > 1 {
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("selected from %d eligible nodes", len(ranked)))
	}

	return decision
}
