// Package placement is STABLE — deterministic filter, rank, and select for task placement.
// It is part of the stable operator path.
package placement

import (
	"math"
	"sort"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
	"github.com/toasterbook88/axis/internal/workload"
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
	for _, eval := range evaluateCandidates(reqs, nodes, st) {
		if eval.Eligible() {
			out = append(out, eval.Node)
		}
	}
	return out
}

func isBlockingFailure(class models.FailureClass) bool {
	switch class {
	case models.FailureExecCrash, models.FailureThermal, models.FailureBattery, models.FailureBackendMisfit:
		return true
	}
	return false
}

// RankCandidates sorts nodes deterministically.
// Priority order:
//  1. Highest allocatable RAM
//  2. Best exact-scope empirical observation (fresh only)
//  3. Resident model locality for the requested runtime
//  4. Preferred backend rank
//  5. GPU score
//  6. Highest effective headroom (free-with-state - requirement)
//  7. Highest unified-memory suitability / TurboQuant for matching asks
//  8. Lowest RAM pressure (soft tie-break after hard blockers)
//  9. Lowest reservation ratio and cluster reservation share
//
// 10. Node name ascending (stable tiebreak)
func RankCandidates(candidates []models.NodeFacts, reqs models.TaskRequirements, st *state.ClusterState) []models.NodeFacts {
	// Precompute cluster-level constants once to avoid O(N) scans inside
	// the per-node loop and the comparator.
	clusterReserved := totalReservedFromNodes(candidates)

	type rankKey struct {
		idx                     int
		allocatableRAM          int64
		empirical               *models.ExecutionObservation
		residentModelRank       int
		preferredBackendRank    int
		gpuScore                int
		headroom                int64
		turboQuantRank          int
		unifiedMemoryRank       int
		pressureRank            int
		reservationRatio        float64
		clusterReservationShare float64
	}

	keys := make([]rankKey, len(candidates))
	for i, n := range candidates {
		share := 0.0
		if clusterReserved > 0 && n.RAMReservedMB > 0 {
			share = float64(n.RAMReservedMB) / float64(clusterReserved)
		}

		// Inline headroom computation using precomputed clusterReserved and
		// share to avoid redundant O(N) scans inside clusterPressurePenalty.
		minNeeded := effectiveMinFreeRAM(reqs, n)
		if minNeeded <= 0 {
			if hint := workload.PeakRAMHint(reqs.Workload.Class); hint > 0 {
				minNeeded = hint
			}
		}
		free := freeRAMWithState(n)
		var hr int64 = -1
		if free >= minNeeded {
			var penalty int64
			if minNeeded > 0 && n.RAMReservedMB > 0 && clusterReserved > 0 {
				penalty = int64(math.Round(share * float64(minNeeded) * 1.5))
				if penalty < 0 {
					penalty = 0
				}
			}
			adjusted := free - penalty
			if adjusted < 0 {
				adjusted = 0
			}
			hr = adjusted - minNeeded
		}

		// Precompute empirical observations once before sorting so the comparator
		// uses a consistent snapshot. Calling time.Now() inside the comparator
		// could produce non-deterministic results if an observation crosses the
		// staleness boundary mid-sort, potentially violating strict weak ordering.
		keys[i] = rankKey{
			idx:                     i,
			allocatableRAM:          allocatableRAM(n),
			empirical:               empiricalObservation(n, reqs, st),
			residentModelRank:       residentModelRank(n, reqs),
			preferredBackendRank:    preferredBackendRank(n, reqs),
			gpuScore:                gpuScore(n, reqs),
			headroom:                hr,
			turboQuantRank:          turboQuantRank(n),
			unifiedMemoryRank:       unifiedMemoryRank(n, reqs),
			pressureRank:            pressureRank(pressureOf(n)),
			reservationRatio:        reservationRatio(n),
			clusterReservationShare: share,
		}
	}

	sort.SliceStable(keys, func(i, j int) bool {
		if keys[i].allocatableRAM != keys[j].allocatableRAM {
			return keys[i].allocatableRAM > keys[j].allocatableRAM
		}

		if cmp := compareObservationPreference(keys[i].empirical, keys[j].empirical); cmp != 0 {
			return cmp > 0
		}

		if keys[i].residentModelRank != keys[j].residentModelRank {
			return keys[i].residentModelRank > keys[j].residentModelRank
		}

		if keys[i].preferredBackendRank != keys[j].preferredBackendRank {
			return keys[i].preferredBackendRank > keys[j].preferredBackendRank
		}

		if keys[i].gpuScore != keys[j].gpuScore {
			return keys[i].gpuScore > keys[j].gpuScore
		}

		if keys[i].headroom != keys[j].headroom {
			return keys[i].headroom > keys[j].headroom
		}

		if reqs.PrefersTurboQuant {
			if keys[i].turboQuantRank != keys[j].turboQuantRank {
				return keys[i].turboQuantRank > keys[j].turboQuantRank
			}
		}

		if keys[i].unifiedMemoryRank != keys[j].unifiedMemoryRank {
			return keys[i].unifiedMemoryRank > keys[j].unifiedMemoryRank
		}

		if keys[i].pressureRank != keys[j].pressureRank {
			return keys[i].pressureRank < keys[j].pressureRank
		}

		if keys[i].reservationRatio != keys[j].reservationRatio {
			return keys[i].reservationRatio < keys[j].reservationRatio
		}

		if keys[i].clusterReservationShare != keys[j].clusterReservationShare {
			return keys[i].clusterReservationShare < keys[j].clusterReservationShare
		}

		return candidates[keys[i].idx].Name < candidates[keys[j].idx].Name
	})

	ranked := make([]models.NodeFacts, len(keys))
	for i, k := range keys {
		ranked[i] = candidates[k.idx]
	}

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

func prefersBackend(backends []string, name string) bool {
	for _, backend := range backends {
		if strings.EqualFold(backend, name) {
			return true
		}
	}
	return false
}

func requiresAppleFoundationModels(reqs models.TaskRequirements) bool {
	return requiresTool(reqs.RequiredTools, "apple-foundation-models") || prefersBackend(reqs.PreferredBackends, "apple-foundation-models")
}

func appleFoundationModelsReady(n models.NodeFacts) bool {
	return n.AppleFM != nil && n.AppleFM.Available && n.AppleFM.Verified && hasTool(n, "apple-foundation-models")
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

func reservedRAM(n models.NodeFacts) int64 {
	if n.RAMReservedMB > 0 {
		return n.RAMReservedMB
	}
	return 0
}

func allocatableRAM(n models.NodeFacts) int64 {
	if n.Resources == nil {
		return 0
	}
	if n.RAMAllocatableMB > 0 {
		return n.RAMAllocatableMB
	}
	return models.AllocatableRAMMB(n.Resources.RAMTotalMB, n.Resources.RAMFreeMB, n.RAMReservedMB)
}

func reservableRAM(n models.NodeFacts) int64 {
	if n.Resources == nil {
		return 0
	}
	return n.ReservableRAM()
}

func reservationRatio(n models.NodeFacts) float64 {
	reservable := reservableRAM(n)
	if reservable <= 0 {
		return 1.0
	}
	return float64(n.RAMReservedMB) / float64(reservable)
}

func clusterReservationShare(n models.NodeFacts, nodes []models.NodeFacts) float64 {
	clusterReserved := totalReservedFromNodes(nodes)
	if clusterReserved <= 0 {
		return 0
	}

	reserved := n.RAMReservedMB
	if reserved <= 0 {
		return 0
	}
	return float64(reserved) / float64(clusterReserved)
}

// gpuScore returns a weighted GPU score considering VRAM and capabilities.
// Discrete GPUs with more VRAM score higher. Integrated-only GPUs (Intel) get reduced scores.
func gpuScore(n models.NodeFacts, reqs models.TaskRequirements) int {
	if n.Resources == nil || len(n.Resources.GPUs) == 0 {
		return 0
	}
	best := 0
	for _, gpu := range n.Resources.GPUs {
		score := 10 // base score for any GPU

		// Capability match with preferred backends
		for _, backend := range reqs.PreferredBackends {
			switch strings.ToLower(backend) {
			case "cuda":
				if gpu.HasCapability("cuda") {
					score += 10
				}
			case "metal", "mlx":
				if gpu.HasCapability("metal") {
					score += 10
				}
			case "rocm":
				if gpu.HasCapability("rocm") {
					score += 10
				}
			}
		}

		// VRAM bonus: 1 pt per GB, capped at 5
		if gpu.VRAMMB > 0 {
			vramPts := gpu.VRAMMB / 1024
			if vramPts > 5 {
				vramPts = 5
			}
			score += vramPts
		}

		// Penalty for integrated-only GPUs (Intel without discrete capabilities)
		if gpu.Vendor == "intel" && len(gpu.Capabilities) == 0 {
			score = score / 2
		}

		if score > best {
			best = score
		}
	}
	return best
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
	adjusted := freeRAMWithState(n)
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

	// GPU — capability-weighted score (up to 25 pts)
	gpuPts := gpuScore(n, reqs)
	if gpuPts > 25 {
		gpuPts = 25
	}
	score += gpuPts

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

	// HDD penalty for heavy inference tasks
	score -= storageClassPenalty(n, reqs)

	// Soft failure memory penalty
	if st != nil && st.Failures != nil {
		rec, ok := st.Failures.NarrowestMatch(models.FailureScope{
			Node:     n.Name,
			Workload: reqs.Workload.Class,
		})
		if ok && !isBlockingFailure(rec.Class) {
			score -= 10 * rec.Count // 10 point penalty per occurrence
		}
	}

	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return score
}

func freeRAMWithState(n models.NodeFacts) int64 {
	if n.RAMAllocatableMB > 0 {
		return n.RAMAllocatableMB
	}
	return allocatableRAM(n)
}

// Headroom computes the effective free RAM headroom for a node after subtracting
// the task's minimum requirement and a cluster-pressure penalty. Returns -1 if
// the node cannot meet the minimum. Used by execution and explain surfaces.
func Headroom(n models.NodeFacts, nodes []models.NodeFacts, reqs models.TaskRequirements) int64 {
	minNeeded := effectiveMinFreeRAM(reqs, n)
	// When no explicit floor is set, use the profile's peak RAM hint as a
	// soft sizing signal so the ranker prefers nodes that can comfortably
	// handle the expected workload.
	if minNeeded <= 0 {
		if hint := workload.PeakRAMHint(reqs.Workload.Class); hint > 0 {
			minNeeded = hint
		}
	}
	free := freeRAMWithState(n)
	if free < minNeeded {
		return -1
	}

	penalty := clusterPressurePenalty(n, nodes, minNeeded)
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
	return minNeeded
}

// MinFreeRAMForNode exposes the effective placement floor used for a node.
// Guarded execution reuses this to keep placement and last-second safety checks aligned.
func MinFreeRAMForNode(reqs models.TaskRequirements, n models.NodeFacts) int64 {
	return effectiveMinFreeRAM(reqs, n)
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

// blocksForThermalOrBattery disqualifies nodes that are thermally throttled or
// critically low on battery for heavy inference tasks.
func blocksForThermalOrBattery(reqs models.TaskRequirements, n models.NodeFacts) bool {
	if !heavyInferenceTask(reqs) || n.Resources == nil {
		return false
	}
	// Battery below 20% blocks heavy tasks
	if n.Resources.BatteryPercent != nil && *n.Resources.BatteryPercent < 20 {
		return true
	}
	// Thermal throttle blocks heavy tasks
	switch strings.ToLower(n.Resources.ThermalState) {
	case "serious", "critical":
		return true
	}
	return false
}

// blocksForEmpiricalPeakRAM hard-excludes a node when a fresh empirical
// observation for this workload recorded a PeakRAMMB that exceeds the node's
// current allocatable RAM. This prevents scheduling on nodes that are
// empirically too small for the workload even if no explicit MinFreeRAMMB was
// set by the caller.
//
// nodeAllocatableMB and modelName are precomputed by the caller (FilterCandidates)
// to avoid recomputing allocatableRAM and re-running regex extraction for every
// node in the loop.
//
// The check is intentionally conservative: it only fires when both (a) a fresh
// observation exists and (b) PeakRAMMB > 0. Absent or stale observations leave
// the node eligible — we don't penalise nodes that haven't been observed yet.
// Profile-based PeakRAMHint is used as a soft ranking signal only (see
// RankCandidates), not a hard filter.

// storageClassPenalty reduces fit score for HDD nodes on heavy inference tasks.
func storageClassPenalty(n models.NodeFacts, reqs models.TaskRequirements) int {
	if n.Resources == nil || !heavyInferenceTask(reqs) {
		return 0
	}
	switch strings.ToLower(n.Resources.StorageClass) {
	case "hdd":
		return 15
	default:
		return 0
	}
}

func heavyInferenceTask(reqs models.TaskRequirements) bool {
	if reqs.ContextWindowTokens > 0 || reqs.PrefersTurboQuant {
		return true
	}
	if requiresTool(reqs.RequiredTools, "ollama") || requiresTool(reqs.RequiredTools, "llama-server") || requiresTool(reqs.RequiredTools, "apple-foundation-models") {
		return true
	}
	for _, backend := range reqs.PreferredBackends {
		switch strings.ToLower(backend) {
		case "llama.cpp", "mlx", "apple-foundation-models":
			return true
		}
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

func preferredBackendRank(n models.NodeFacts, reqs models.TaskRequirements) int {
	best := 0
	for _, backend := range reqs.PreferredBackends {
		rank := 0
		switch strings.ToLower(backend) {
		case "apple-foundation-models":
			if models.IsLocalNode(n) && appleFoundationModelsReady(n) {
				rank = 4
			}
		case "llama.cpp":
			switch {
			case hasTool(n, "llama-server") && turboQuantHasBackend(n, "llama.cpp"):
				rank = 3
			case hasTool(n, "llama-server"):
				rank = 2
			case turboQuantHasBackend(n, "llama.cpp"):
				rank = 1
			}
		case "mlx":
			switch {
			case turboQuantHasBackend(n, "mlx") && n.Resources != nil && n.Resources.MemoryTopology == models.MemoryTopologyUnified:
				rank = 3
			case turboQuantHasBackend(n, "mlx"):
				rank = 2
			case n.Resources != nil && n.Resources.MemoryTopology == models.MemoryTopologyUnified:
				rank = 1
			}
		}
		if rank > best {
			best = rank
		}
	}
	return best
}

func turboQuantHasBackend(n models.NodeFacts, backend string) bool {
	if n.TurboQuant == nil {
		return false
	}
	for _, candidate := range n.TurboQuant.Backends {
		if strings.EqualFold(candidate, backend) {
			return true
		}
	}
	return false
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

func totalReservedFromNodes(nodes []models.NodeFacts) int64 {
	var sum int64
	for _, n := range nodes {
		if n.RAMReservedMB > 0 {
			sum += n.RAMReservedMB
		}
	}
	return sum
}

func clusterPressurePenalty(n models.NodeFacts, nodes []models.NodeFacts, requestMB int64) int64 {
	if requestMB <= 0 || n.RAMReservedMB <= 0 {
		return 0
	}

	clusterReserved := totalReservedFromNodes(nodes)
	if clusterReserved <= 0 {
		return 0
	}

	share := clusterReservationShare(n, nodes)
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
