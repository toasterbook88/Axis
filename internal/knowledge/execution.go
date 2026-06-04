package knowledge

import (
	"encoding/json"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

// ExecutionContextJSON returns the execution context payload consumed by
// scripts and learned skills. It preserves the existing top-level keys used by
// the current script registry while adding execution-specific detail.
func ExecutionContextJSON(
	snap *models.ClusterSnapshot,
	st *state.ClusterState,
	decision models.PlacementDecision,
	taskDesc string,
	script any,
	skill any,
) ([]byte, error) {
	k := Build(snap, st, decision.Node)
	payload := map[string]any{
		"timestamp": k.Timestamp,
		"best_node": k.BestNode,
		"snapshot":  k.Snapshot,
		"state":     k.State,
		"ollama":    k.Ollama,
		"load":      k.Load,
		"decision":  decision,
		"task_desc": taskDesc,
	}
	if k.Git != nil {
		payload["git"] = k.Git
	}
	if script != nil {
		payload["script"] = script
	}
	if skill != nil {
		payload["skill"] = skill
	}
	return json.MarshalIndent(payload, "", "  ")
}
