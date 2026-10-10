package placement

import (
	"strings"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

// LlamaServerObservedExclusion reports whether a fresh llama-server RSS
// observation for modelName exceeds this node's allocatable RAM. modelName
// is the weights file base stored on the start observation. It is not parsed
// from a task description. The caller passes the snapshot node it already has.
func LlamaServerObservedExclusion(n models.NodeFacts, modelName string, st *state.ClusterState) (string, bool) {
	reqs := models.TaskRequirements{
		Workload:      models.WorkloadProfileMatch{Class: models.ClassLlamaServer},
		RequiredTools: []string{"llama-server"},
	}
	return empiricalObservedRAMExclusionReason(n, reqs, st, allocatableRAM(n), strings.TrimSpace(modelName))
}
