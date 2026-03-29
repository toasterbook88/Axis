// Package placement implements deterministic task placement logic.
// Phase 2: read-only, advisory — does NOT execute tasks.
package placement

import (
	"math"
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
//   - If RequiredTools are set, node must satisfy all of them
func FilterCandidates(reqs models.TaskRequirements, nodes []models.NodeFacts, st *state.ClusterState) []models.NodeFacts {
	var out []models.NodeFacts
	for _, n := range nodes {
		if n.Status != models.StatusComplete {
			if !allowsIncompleteNode(n, reqs.RequiredTools) {
				continue
			}
		}
		if blocksForRuntimePressure(reqs, n) {
			continue
		}
		if reqs.MinFreeRAMMB > 0 {
			minNeeded := effectiveMinFreeRAM(reqs, n)
			adjustedFree := freeRAMWithState(n, st)
			if n.Resources == nil || adjustedFree < minNeeded {
				continue
			}
		}
		if !satisfiesRequiredTools(n, reqs.RequiredTools) {
			continue
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
//  4. Highest unified-memory suitability for mlx/long-context asks
//  5. Highest allocatable RAM
//  6. Lowest reservation ratio (reserved / total RAM)
//  7. Node name ascending (stable tiebreak)
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

		if reqs.PrefersTurboQuant {
			ti := turboQuantRank(ranked[i])
			tj := turboQuantRank(ranked[j])
			if ti != tj {
				return ti > tj
			}
		}

		ui := unifiedMemoryRank(ranked[i], reqs)
		uj := unifiedMemoryRank(ranked[j], reqs)
		if ui != uj {
			return ui > uj
		}

		ri := allocatableRAM(ranked[i], st)
		rj := allocatableRAM(ranked[j], st)
		if ri != rj {
			return ri > rj
		}

		ratioI := reservationRatio(ranked[i], st)
		ratioJ := reservationRatio(ranked[j], st)
		if ratioI != ratioJ {
			return ratioI < ratioJ
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

func requiresTool(tools []string, name string) bool {
	for _, tool := range tools {
		if strings.EqualFold(tool, name) {
			return true
		}
	}
	return false
}

func allowsIncompleteNode(n models.NodeFacts, requiredTools []string) bool {
	return len(requiredTools) == 1 && requiresTool(requiredTools, "ollama") && isOllamaBootstrapPossible(n)
}

func satisfiesRequiredTools(n models.NodeFacts, requiredTools []string) bool {
	for _, tool := range requiredTools {
		switch {
		case strings.EqualFold(tool, "ollama"):
			if !ollamaIsReady(n) && !isOllamaBootstrapPossible(n) {
				return false
			}
		case !hasTool(n, tool):
			return false
		}
	}
	return true
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

func reservedRAM(n models.NodeFacts, st *state.ClusterState) int64 {
	if st != nil && st.Nodes != nil {
		if ns, ok := st.Nodes[n.Name]; ok {
			return ns.ReservedMB
		}
	}
	if n.Resources == nil {
		return 0
	}
	return n.Resources.RAMReservedMB
}

func allocatableRAM(n models.NodeFacts, st *state.ClusterState) int64 {
	if n.Resources == nil {
		return 0
	}
	if st == nil && (n.Resources.RAMReservedMB > 0 || n.Resources.RAMAllocatableMB > 0) {
		return n.Resources.RAMAllocatableMB
	}
	effective := n.Resources.RAMFreeMB - reservedRAM(n, st)
	if effective < 0 {
		return 0
	}
	return effective
}

func reservationRatio(n models.NodeFacts, st *state.ClusterState) float64 {
	if n.Resources == nil || n.Resources.RAMTotalMB <= 0 {
		return 1.0
	}
	return float64(reservedRAM(n, st)) / float64(n.Resources.RAMTotalMB)
}

func gpuPresent(n models.NodeFacts) bool {
	if n.Resources == nil {
		return false
	}
	return len(n.Resources.GPUs) > 0
}

// ComputeFitScore returns 0-100 indicating small-model suitability.
// Scoring breakdown:
//   - Allocatable RAM: up to 30 pts (1 pt per 256MB, capped at 30)
//   - Pressure:    up to 25 pts (none=25, low=20, medium=10, high=0)
//   - GPU present: +25 pts
//   - CPU cores:   up to 10 pts (1 pt per core, capped at 10)
//   - Local node:  +10 pts (no SSH hop = lower latency)
//   - TurboQuant-capable long-context backend: +15..25 pts for long-context asks
//   - Unified-memory topology bonus: +8..16 pts for mlx/long-context asks
//
// Max: capped at 100
func ComputeFitScore(n models.NodeFacts, isLocal bool, st *state.ClusterState) int {
	return ComputeTaskFitScore(n, isLocal, st, models.TaskRequirements{})
}

// ComputeTaskFitScore returns 0-100 indicating task-specific placement fit.
func ComputeTaskFitScore(n models.NodeFacts, isLocal bool, st *state.ClusterState, reqs models.TaskRequirements) int {
	if n.Resources == nil {
		return 0
	}

	score := 0

	// Allocatable RAM: 1pt per 256MB, cap 30
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

	if reqs.PrefersTurboQuant {
		score += turboQuantFitBonus(n, reqs)
	}
	score += unifiedMemoryFitBonus(n, reqs)

	if score > 100 {
		score = 100
	}
	return score
}

func freeRAMWithState(n models.NodeFacts, st *state.ClusterState) int64 {
	return allocatableRAM(n, st)
}

func headroom(n models.NodeFacts, st *state.ClusterState, reqs models.TaskRequirements) int64 {
	minNeeded := effectiveMinFreeRAM(reqs, n)
	free := freeRAMWithState(n, st)
	if free < minNeeded {
		return -1
	}

	penalty := clusterPressurePenalty(n, st, minNeeded)
	adjusted := free - penalty
	if adjusted < 0 {
		adjusted = 0
	}
	return adjusted - minNeeded
}

func effectiveMinFreeRAM(reqs models.TaskRequirements, n models.NodeFacts) int64 {
	minNeeded := reqs.MinFreeRAMMB
	if minNeeded <= 0 {
		return 0
	}
	if reqs.PrefersTurboQuant && turboQuantVerified(n) {
		minNeeded = turboQuantAdjustedMinFreeRAM(minNeeded)
	}
	if reqs.ContextWindowTokens == 0 && requiresTool(reqs.RequiredTools, "ollama") && ollamaWarm(n) && minNeeded > 600 {
		return 600
	}
	return minNeeded
}

func turboQuantAdjustedMinFreeRAM(minNeeded int64) int64 {
	if minNeeded <= 0 {
		return 0
	}
	adjusted := (minNeeded + 5) / 6
	if adjusted < 1024 {
		return 1024
	}
	return adjusted
}

func blocksForRuntimePressure(reqs models.TaskRequirements, n models.NodeFacts) bool {
	if !heavyInferenceTask(reqs) || n.Resources == nil {
		return false
	}
	switch strings.ToLower(n.Resources.PressureSource) {
	case "linux-psi":
		return n.Resources.PressureStall10 >= 15
	case "darwin-vm-pressure":
		return strings.EqualFold(n.Resources.Pressure, "high")
	default:
		return false
	}
}

func heavyInferenceTask(reqs models.TaskRequirements) bool {
	if reqs.ContextWindowTokens > 0 || reqs.PrefersTurboQuant {
		return true
	}
	if requiresTool(reqs.RequiredTools, "ollama") && reqs.MinFreeRAMMB >= 1024 {
		return true
	}
	return reqs.MinFreeRAMMB >= 4096
}

func prefersUnifiedMemory(reqs models.TaskRequirements) bool {
	if reqs.ContextWindowTokens > 0 || reqs.PrefersTurboQuant {
		return true
	}
	for _, backend := range reqs.PreferredBackends {
		if strings.EqualFold(backend, "mlx") {
			return true
		}
	}
	return false
}

func unifiedMemoryRank(n models.NodeFacts, reqs models.TaskRequirements) int {
	if !prefersUnifiedMemory(reqs) || n.Resources == nil || n.Resources.MemoryTopology != models.MemoryTopologyUnified {
		return 0
	}
	rank := 1
	if n.Resources.MemoryClass > 0 {
		rank += n.Resources.MemoryClass
	}
	if turboQuantVerified(n) {
		rank++
	}
	return rank
}

func unifiedMemoryFitBonus(n models.NodeFacts, reqs models.TaskRequirements) int {
	if !prefersUnifiedMemory(reqs) || n.Resources == nil || n.Resources.MemoryTopology != models.MemoryTopologyUnified {
		return 0
	}
	bonus := 8
	if n.Resources.MemoryClass > 0 {
		bonus += minInt(n.Resources.MemoryClass*2, 8)
	}
	if turboQuantVerified(n) {
		bonus += 2
	}
	return bonus
}

func turboQuantSupported(n models.NodeFacts) bool {
	return n.TurboQuant != nil && n.TurboQuant.Supported
}

func turboQuantVerified(n models.NodeFacts) bool {
	return n.TurboQuant != nil && n.TurboQuant.Supported && n.TurboQuant.Verified
}

func turboQuantRank(n models.NodeFacts) int {
	switch {
	case turboQuantVerified(n):
		return 2
	case turboQuantSupported(n):
		return 1
	default:
		return 0
	}
}

func turboQuantFitBonus(n models.NodeFacts, reqs models.TaskRequirements) int {
	if !turboQuantSupported(n) {
		return 0
	}

	switch {
	case reqs.ContextWindowTokens >= 1000000:
		if turboQuantVerified(n) {
			return 25
		}
		return 10
	case reqs.ContextWindowTokens >= 256000:
		if turboQuantVerified(n) {
			return 20
		}
		return 8
	default:
		if turboQuantVerified(n) {
			return 15
		}
		return 5
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func turboQuantStatusLabel(n models.NodeFacts) string {
	if turboQuantVerified(n) {
		return "verified"
	}
	if turboQuantSupported(n) {
		return "detected"
	}
	return ""
}

func turboQuantCapabilities(n models.NodeFacts) string {
	if n.TurboQuant == nil || len(n.TurboQuant.Capabilities) == 0 {
		return ""
	}
	return strings.Join(n.TurboQuant.Capabilities, ", ")
}

func turboQuantBackends(n models.NodeFacts) string {
	if n.TurboQuant == nil || len(n.TurboQuant.Backends) == 0 {
		return ""
	}
	return strings.Join(n.TurboQuant.Backends, ", ")
}

func totalReserved(st *state.ClusterState) int64 {
	if st == nil || st.Nodes == nil {
		return 0
	}

	var sum int64
	for _, ns := range st.Nodes {
		sum += ns.ReservedMB
	}
	return sum
}

func clusterPressurePenalty(n models.NodeFacts, st *state.ClusterState, requestMB int64) int64 {
	if st == nil || st.Nodes == nil || requestMB <= 0 {
		return 0
	}

	ns, ok := st.Nodes[n.Name]
	if !ok || ns.ReservedMB <= 0 {
		return 0
	}

	clusterReserved := totalReserved(st)
	if clusterReserved <= 0 {
		return 0
	}

	share := float64(ns.ReservedMB) / float64(clusterReserved+1)
	penalty := int64(math.Round(share * float64(requestMB) * 1.5))
	if penalty < 0 {
		return 0
	}
	return penalty
}

func ollamaIsReady(n models.NodeFacts) bool {
	return n.Ollama != nil && n.Ollama.Running
}

func isOllamaBootstrapPossible(n models.NodeFacts) bool {
	return n.Ollama != nil && n.Ollama.Installed
}

func ollamaWarm(n models.NodeFacts) bool {
	return n.Ollama != nil && (n.Ollama.Listening || len(n.Ollama.Models) > 0 || n.Ollama.Running)
}
