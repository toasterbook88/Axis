package placement

import (
	"fmt"
	"math"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
	"github.com/toasterbook88/axis/internal/workload"
)

type candidateEvaluation struct {
	Node             models.NodeFacts
	ExclusionReasons []string
}

func (e candidateEvaluation) Eligible() bool {
	return len(e.ExclusionReasons) == 0
}

func evaluateCandidates(reqs models.TaskRequirements, nodes []models.NodeFacts, st *state.ClusterState) []candidateEvaluation {
	cachedModelName := inferenceModelName(reqs)
	evals := make([]candidateEvaluation, 0, len(nodes))
	for _, n := range nodes {
		evals = append(evals, evaluateCandidate(reqs, n, st, cachedModelName))
	}
	return evals
}

func evaluateCandidate(reqs models.TaskRequirements, n models.NodeFacts, st *state.ClusterState, cachedModelName string) candidateEvaluation {
	eval := candidateEvaluation{Node: n}

	if requiresAppleFoundationModels(reqs) {
		switch {
		case !models.IsLocalNode(n):
			eval.ExclusionReasons = append(eval.ExclusionReasons, "apple foundation models are local-only")
		case !appleFoundationModelsReady(n):
			eval.ExclusionReasons = append(eval.ExclusionReasons, "apple foundation models not verified on local node")
		}
	}

	if n.Status != models.StatusComplete && !allowsIncompleteNode(n, reqs.RequiredTools) {
		eval.ExclusionReasons = append(eval.ExclusionReasons, fmt.Sprintf("status: %s", n.Status))
	}

	if blocksForRuntimePressure(reqs, n) {
		if n.Resources != nil && n.Resources.PressureSource == "linux-psi" {
			eval.ExclusionReasons = append(eval.ExclusionReasons,
				fmt.Sprintf("critical runtime memory pressure via linux-psi avg10=%.2f", n.Resources.PressureStall10))
		} else if n.Resources != nil && n.Resources.PressureSource == "darwin-vm-pressure" {
			eval.ExclusionReasons = append(eval.ExclusionReasons, "critical runtime memory pressure via darwin vm pressure")
		} else {
			eval.ExclusionReasons = append(eval.ExclusionReasons, "critical runtime memory pressure")
		}
	}

	if blocksForThermalOrBattery(reqs, n) {
		if n.Resources != nil && n.Resources.BatteryPercent != nil && *n.Resources.BatteryPercent < 20 {
			eval.ExclusionReasons = append(eval.ExclusionReasons,
				fmt.Sprintf("battery critically low: %d%%", *n.Resources.BatteryPercent))
		} else if n.Resources != nil {
			eval.ExclusionReasons = append(eval.ExclusionReasons,
				fmt.Sprintf("thermal throttling state: %s", n.Resources.ThermalState))
		} else {
			eval.ExclusionReasons = append(eval.ExclusionReasons, "thermal or battery guardrail active")
		}
	}

	if st != nil && st.Failures != nil {
		rec, ok := st.Failures.NarrowestMatch(models.FailureScope{
			Node:     n.Name,
			Workload: reqs.Workload.Class,
		})
		if ok && isBlockingFailure(rec.Class) {
			eval.ExclusionReasons = append(eval.ExclusionReasons,
				fmt.Sprintf("blocked by failure memory: %s repeated %d time(s)", rec.Class, rec.Count))
		}
	}

	nodeAllocatable := allocatableRAM(n)
	if reqs.MinFreeRAMMB > 0 {
		minNeeded := effectiveMinFreeRAM(reqs, n)
		actual := int64(0)
		if n.Resources != nil {
			actual = n.Resources.RAMFreeMB
		}
		if n.Resources == nil || nodeAllocatable < minNeeded {
			eval.ExclusionReasons = append(eval.ExclusionReasons,
				fmt.Sprintf("need %dMB free RAM, have %dMB effective (base %dMB, short %dMB)",
					minNeeded, nodeAllocatable, actual, minNeeded-nodeAllocatable))
		}
	}

	if reason, blocked := empiricalPeakRAMExclusionReason(n, reqs, st, nodeAllocatable, cachedModelName); blocked {
		eval.ExclusionReasons = append(eval.ExclusionReasons, reason)
	}

	if missing, available := missingRequiredTools(n, reqs.RequiredTools); len(missing) > 0 {
		eval.ExclusionReasons = append(eval.ExclusionReasons,
			fmt.Sprintf("missing required tools %v (has: %v)", missing, available))
	}

	return eval
}

func empiricalPeakRAMExclusionReason(n models.NodeFacts, reqs models.TaskRequirements, st *state.ClusterState, nodeAllocatableMB int64, modelName string) (string, bool) {
	tool := inferredToolForObservation(reqs, "")
	obs, ok := freshObservationForScope(models.ObservationScope{
		Node:      strings.TrimSpace(n.Name),
		Workload:  reqs.Workload.Class,
		Backend:   observationBackend(reqs, tool),
		Tool:      tool,
		ModelName: modelName,
	}, st)
	if !ok || obs.PeakRAMMB <= 0 || nodeAllocatableMB >= obs.PeakRAMMB {
		return "", false
	}
	return fmt.Sprintf("empirical peak RAM %dMB exceeds allocatable %dMB", obs.PeakRAMMB, nodeAllocatableMB), true
}

func missingRequiredTools(n models.NodeFacts, requiredTools []string) ([]string, []string) {
	if len(requiredTools) == 0 {
		return nil, nil
	}

	missing := make([]string, 0, len(requiredTools))
	for _, required := range requiredTools {
		if strings.EqualFold(required, "ollama") {
			if !ollamaIsReady(n) && !isOllamaBootstrapPossible(n) {
				missing = append(missing, required)
			}
			continue
		}
		if !hasTool(n, required) {
			missing = append(missing, required)
		}
	}

	available := make([]string, 0, len(n.Tools))
	for _, t := range n.Tools {
		available = append(available, t.Name)
	}
	return missing, available
}

func matchedRequiredTools(n models.NodeFacts, requiredTools []string) []string {
	matched := make([]string, 0, len(requiredTools))
	for _, required := range requiredTools {
		switch {
		case strings.EqualFold(required, "ollama"):
			if ollamaIsReady(n) || isOllamaBootstrapPossible(n) {
				matched = append(matched, "ollama")
			}
		case hasTool(n, required):
			matched = append(matched, required)
		}
	}
	return matched
}

func explainEligibleCandidate(n models.NodeFacts, reqs models.TaskRequirements, st *state.ClusterState, clusterReserved int64) models.PlacementCandidateExplanation {
	reasoning := make([]string, 0, 4)

	if reason := empiricalReason(empiricalObservation(n, reqs, st)); reason != "" {
		reasoning = append(reasoning, reason)
	}
	if reason := residentModelReason(n, reqs); reason != "" {
		reasoning = append(reasoning, reason)
	}
	if requiresAppleFoundationModels(reqs) && models.IsLocalNode(n) && appleFoundationModelsReady(n) {
		reasoning = append(reasoning, "local Apple Foundation Models path verified")
	} else if reqs.PrefersTurboQuant && turboQuantSupported(n) {
		backends := turboQuantBackends(n)
		if backends == "" {
			backends = "detected backend"
		}
		status := turboQuantStatusLabel(n)
		if status == "" {
			status = "detected"
		}
		reasoning = append(reasoning, fmt.Sprintf("turboquant-aware backend %s: %s", status, backends))
	} else if len(reqs.PreferredBackends) > 0 && preferredBackendRank(n, reqs) > 0 {
		reasoning = append(reasoning, fmt.Sprintf("preferred backend match: %s", strings.Join(reqs.PreferredBackends, ", ")))
	}

	if n.Resources != nil {
		allocatable := allocatableRAM(n)
		if reqs.MinFreeRAMMB > 0 {
			reasoning = append(reasoning,
				fmt.Sprintf("%dMB allocatable against %dMB requirement", allocatable, effectiveMinFreeRAM(reqs, n)))
		} else {
			reasoning = append(reasoning,
				fmt.Sprintf("%dMB allocatable, pressure %s", allocatable, n.Resources.Pressure))
		}
	}

	if matched := matchedRequiredTools(n, reqs.RequiredTools); len(matched) > 0 {
		reasoning = append(reasoning, fmt.Sprintf("required tools available: %s", strings.Join(matched, ", ")))
	}

	isLocal := models.IsLocalNode(n)
	if isLocal {
		reasoning = append(reasoning, "+10 Local Node Bonus")
	} else if hasThunderboltLink(n) {
		reasoning = append(reasoning, "+15 High-Speed Compute-Pair Link (Thunderbolt)")
	}

	if n.NetworkClass == models.NetworkClassTailscale {
		reasoning = append(reasoning, "-20 VPN Latency Penalty (Tailscale overlay detected)")
	} else if n.NetworkClass == models.NetworkClassVPN {
		reasoning = append(reasoning, "-20 VPN Latency Penalty")
	} else if n.NetworkClass == models.NetworkClassRelayed {
		reasoning = append(reasoning, "-20 VPN Latency Penalty")
	}

	if n.NetworkClass != "" && n.NetworkClass != models.NetworkClassUnknown {
		reasoning = append(reasoning, fmt.Sprintf("network connection class: %s (latency: %dms)", n.NetworkClass, n.SSHHandshakeLatencyMs))
	}

	if len(reasoning) == 0 {
		reasoning = append(reasoning, "eligible under current placement rules")
	}

	// Inline headroom computation using precomputed clusterReserved to avoid
	// redundant O(N) scans inside Headroom and clusterPressurePenalty.
	minNeeded := effectiveMinFreeRAM(reqs, n)
	if minNeeded <= 0 {
		if hint := workload.PeakRAMHint(reqs.Workload.Class); hint > 0 {
			minNeeded = hint
		}
	}
	free := freeRAMWithState(n)
	var headroom int64 = -1
	if free >= minNeeded {
		var penalty int64
		if minNeeded > 0 && n.RAMReservedMB > 0 && clusterReserved > 0 {
			share := float64(n.RAMReservedMB) / float64(clusterReserved)
			penalty = int64(math.Round(share * float64(minNeeded) * 1.5))
			if penalty < 0 {
				penalty = 0
			}
		}
		adjusted := free - penalty
		if adjusted < 0 {
			adjusted = 0
		}
		headroom = adjusted - minNeeded
	}

	return models.PlacementCandidateExplanation{
		Node:       n.Name,
		FitScore:   ComputeTaskFitScore(n, models.IsLocalNode(n), st, reqs),
		IsLocal:    models.IsLocalNode(n),
		HeadroomMB: headroom,
		Reasoning:  reasoning,
	}
}

func ExplainPlacement(reqs models.TaskRequirements, nodes []models.NodeFacts, st *state.ClusterState) models.PlacementExplanation {
	evals := evaluateCandidates(reqs, nodes, st)
	eligibleNodes := make([]models.NodeFacts, 0, len(evals))
	excluded := make([]models.PlacementExclusion, 0, len(evals))
	for _, eval := range evals {
		if eval.Eligible() {
			eligibleNodes = append(eligibleNodes, eval.Node)
			continue
		}
		excluded = append(excluded, models.PlacementExclusion{
			Node:    eval.Node.Name,
			Reasons: append([]string(nil), eval.ExclusionReasons...),
		})
	}

	ranked := RankCandidates(eligibleNodes, reqs, st)
	clusterReserved := totalReservedFromNodes(nodes)
	eligible := make([]models.PlacementCandidateExplanation, 0, len(ranked))
	for _, node := range ranked {
		eligible = append(eligible, explainEligibleCandidate(node, reqs, st, clusterReserved))
	}

	return models.PlacementExplanation{
		Decision: SelectBestNode(reqs, nodes, st),
		Eligible: eligible,
		Excluded: excluded,
	}
}
