package placement

import (
	"strings"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

// LlamaServerPeakExclusion reports whether a fresh llama-server RAM peak for
// modelName exceeds this node's allocatable RAM. modelName is the weights
// file base stored on the start observation. It is not parsed from a task
// description. The caller passes the snapshot node it already has.
func LlamaServerPeakExclusion(n models.NodeFacts, modelName string, st *state.ClusterState) (string, bool) {
	reqs := models.TaskRequirements{
		Workload:      models.WorkloadProfileMatch{Class: models.ClassLlamaServer},
		RequiredTools: []string{"llama-server"},
	}
	return empiricalPeakRAMExclusionReason(n, reqs, st, allocatableRAM(n), strings.TrimSpace(modelName))
}
