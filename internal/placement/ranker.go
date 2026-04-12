// Package placement implements deterministic task placement logic reused by
// both advisory selection and guarded execution.
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
		if requiresAppleFoundationModels(reqs) {
			if !models.IsLocalNode(n) || !appleFoundationModelsReady(n) {
				continue
			}
		}
		if n.Status != models.StatusComplete {
			if !allowsIncompleteNode(n, reqs.RequiredTools) {
				continue
			}
		}
		if blocksForRuntimePressure(reqs, n) {
			continue
		}
		if blocksForThermalOrBattery(reqs, n) {
			continue
		}
		if st != nil && st.Failures != nil {
			rec, ok := st.Failures.NarrowestMatch(models.FailureScope{
				Node:     n.Name,
				Workload: reqs.Workload.Class,
			})
			if ok && isBlockingFailure(rec.Class) {
				continue
			}
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
	ranked := make([]models.NodeFacts, len(candidates))
	copy(ranked, candidates)

	// Precompute empirical observations once before sorting so the comparator
	// uses a consistent snapshot. Calling time.Now() inside the comparator
	// could produce non-deterministic results if an observation crosses the
	// staleness boundary mid-sort, potentially violating strict weak ordering.
	empirical := make(map[string]*models.ExecutionObservation, len(ranked))
	for _, n := range ranked {
		empirical[n.Name] = empiricalObservation(n, reqs, st)
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		ri := allocatableRAM(ranked[i], st)
		rj := allocatableRAM(ranked[j], st)
		if ri != rj {
			return ri > rj
		}

		if cmp := compareObservationPreference(empirical[ranked[i].Name], empirical[ranked[j].Name]); cmp != 0 {
			return cmp > 0
		}

		rmi := residentModelRank(ranked[i], reqs)
		rmj := residentModelRank(ranked[j], reqs)
		if rmi != rmj {
			return rmi > rmj
		}

		bi := preferredBackendRank(ranked[i], reqs)
		bj := preferredBackendRank(ranked[j], reqs)
		if bi != bj {
			return bi > bj
		}

		gi := gpuScore(ranked[i], reqs)
		gj := gpuScore(ranked[j], reqs)
		if gi != gj {
			return gi > gj
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

		pi := pressureOf(ranked[i])
		pj := pressureOf(ranked[j])
		if pi != pj {
			return pressureRank(pi) < pressureRank(pj)
		}

		ratioI := reservationRatio(ranked[i], st)
		ratioJ := reservationRatio(ranked[j], st)
		if ratioI != ratioJ {
			return ratioI < ratioJ
		}

		shareI := clusterReservationShare(ranked[i], st)
		shareJ := clusterReservationShare(ranked[j], st)
		if shareI != shareJ {
			return shareI < shareJ
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
	if st == nil && n.Resources.RAMAllocatableMB > 0 {
		return n.Resources.RAMAllocatableMB
	}
	return models.AllocatableRAMMB(n.Resources.RAMTotalMB, n.Resources.RAMFreeMB, reservedRAM(n, st))
}

func reservationRatio(n models.NodeFacts, st *state.ClusterState) float64 {
	if n.Resources == nil || n.Resources.RAMTotalMB <= 0 {
		return 1.0
	}
	return float64(reservedRAM(n, st)) / float64(n.Resources.RAMTotalMB)
}

func clusterReservationShare(n models.NodeFacts, st *state.ClusterState) float64 {
	clusterReserved := totalReserved(st)
	if clusterReserved <= 0 {
		return 0
	}

	reserved := reservedRAM(n, st)
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

func totalReserved(st *state.ClusterState) int64 {
	if st == nil || st.Nodes == nil {
		return 0
	}

	var sum int64
	for _, ns := range st.Nodes {
		if ns.ReservedMB > 0 {
			sum += ns.ReservedMB
		}
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

	share := clusterReservationShare(n, st)
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
