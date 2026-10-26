package placement

import (
	"fmt"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

// SelectBestNode runs the full placement pipeline: filter → rank → select.
// Reasoning is diagnostic: on failure it explains why each node was excluded;
// on success it states the ranking objective/metric, locality, and runner-up
// comparison under the same metric (not FitScore).
func SelectBestNode(reqs models.TaskRequirements, nodes []models.NodeFacts, st *state.ClusterState) models.PlacementDecision {
	candidates := FilterCandidates(reqs, nodes, st)
	if len(candidates) == 0 {
		return buildFailureDecision(reqs, nodes, st)
	}

	ranked, rankedKeys := rankCandidatesWithKeys(candidates, reqs, st)
	best := ranked[0]
	local := models.IsLocalNode(best)

	decision := buildSuccessDecision(best, ranked, rankedKeys, reqs, local, st)
	decision.Workload = reqs.Workload
	return decision
}

func buildPlacementRanking(best models.NodeFacts, ranked []models.NodeFacts, rankedKeys []rankKey, reqs models.TaskRequirements) *models.PlacementRanking {
	obj, source := ResolveObjective(reqs)
	ranking := &models.PlacementRanking{
		Objective:  obj,
		Source:     source,
		Metric:     rankingMetricFor(obj, best, ranked, rankedKeys, reqs),
		DecisiveBy: models.RankingCriterionOnlyCandidate,
	}
	if len(ranked) > 1 && len(rankedKeys) > 1 {
		ranking.DecisiveBy, ranking.TieBreak = decisiveComparison(best, ranked[1], rankedKeys[0], rankedKeys[1], reqs)
	}
	return ranking
}

func rankingMetricFor(obj models.PlacementObjective, best models.NodeFacts, ranked []models.NodeFacts, rankedKeys []rankKey, reqs models.TaskRequirements) models.RankingMetric {
	if obj == models.PlacementObjectiveSpread {
		if len(rankedKeys) > 0 {
			return models.RankingMetric{
				Name:       RankingMetricReservationSkew,
				Value:      rankedKeys[0].reservationSkew,
				Unit:       RankingMetricUnitStddev,
				Provenance: models.MetricProvenanceDerived,
			}
		}
		return SpreadMetric(best, ranked, reqs)
	}
	return CapacityMetric(best)
}

// FormatRankingMetricValue renders the ranking metric with units. Capacity uses
// whole MB; spread uses fractional stddev so the displayed number matches the
// sort key (placement-selection-contract P2).
func FormatRankingMetricValue(m models.RankingMetric) string {
	if m.Unit == RankingMetricUnitStddev {
		return fmt.Sprintf("%.6f%s", m.Value, m.Unit)
	}
	return fmt.Sprintf("%.0f%s", m.Value, m.Unit)
}

func buildSuccessDecision(best models.NodeFacts, ranked []models.NodeFacts, rankedKeys []rankKey, reqs models.TaskRequirements, local bool, st *state.ClusterState) models.PlacementDecision {
	decision := models.PlacementDecision{
		Node:     best.Name,
		OK:       true,
		FitScore: ComputeTaskFitScore(best, local, st, reqs), // diagnostic only
		IsLocal:  local,
		Ranking:  buildPlacementRanking(best, ranked, rankedKeys, reqs),
	}

	// Structured ranking is authoritative; prose is rendered from it.
	decision.Reasoning = append(decision.Reasoning,
		fmt.Sprintf("ranking objective: %s (%s)", decision.Ranking.Objective, decision.Ranking.Source),
		fmt.Sprintf("ranking metric: %s = %s (%s)", decision.Ranking.Metric.Name, FormatRankingMetricValue(decision.Ranking.Metric), decision.Ranking.Metric.Provenance),
		fmt.Sprintf("decisive criterion: %s", decision.Ranking.DecisiveBy))
	if tie := decision.Ranking.TieBreak; tie != nil {
		tieLabel := "headline tied"
		if decision.Ranking.Objective == models.PlacementObjectiveCapacity {
			tieLabel = "capacity tied"
		}
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("%s; %s winner=%s runner-up=%s", tieLabel, tie.Criterion, tie.WinnerValue, tie.RunnerValue))
	}

	if reqs.Workload.Class != "" && reqs.Workload.Class != models.ClassUnknown {
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("workload class: %s", reqs.Workload.Class))
	}
	for _, note := range reqs.Workload.Notes {
		decision.Reasoning = append(decision.Reasoning, fmt.Sprintf("workload note: %s", note))
	}

	if requiresTool(reqs.RequiredTools, "ollama") && models.IsLocalNode(best) {
		decision.Reasoning = append(decision.Reasoning, "local node preferred for ollama")
	}
	if requiresAppleFoundationModels(reqs) && appleFoundationModelsReady(best) {
		if models.IsLocalNode(best) {
			decision.Reasoning = append(decision.Reasoning, "local Apple Foundation Models path verified")
		} else {
			decision.Reasoning = append(decision.Reasoning, "remote Apple Foundation Models path verified")
		}
	}
	if reqs.ContextWindowTokens > 0 {
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("long-context task hint: ~%d tokens", reqs.ContextWindowTokens))
	}
	if best.Resources != nil && best.Resources.MemoryTopology == models.MemoryTopologyUnified && prefersUnifiedMemory(reqs) {
		if best.Resources.MemoryClass > 0 {
			decision.Reasoning = append(decision.Reasoning,
				fmt.Sprintf("unified memory topology (class %d)", best.Resources.MemoryClass))
		} else {
			decision.Reasoning = append(decision.Reasoning, "unified memory topology")
		}
	}
	if reqs.PrefersTurboQuant && turboQuantSupported(best) {
		backends := turboQuantBackends(best)
		if backends == "" {
			backends = "detected backend"
		}
		status := turboQuantStatusLabel(best)
		if status == "" {
			status = "detected"
		}
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("turboquant-aware backend %s: %s", status, backends))
		if caps := turboQuantCapabilities(best); caps != "" {
			decision.Reasoning = append(decision.Reasoning,
				fmt.Sprintf("turboquant capabilities: %s", caps))
		}
	}
	bestObservation := empiricalObservation(best, reqs, st)
	if reason := empiricalReason(bestObservation); reason != "" {
		decision.Reasoning = append(decision.Reasoning, reason)
	}
	if reason := residentModelReason(best, reqs); reason != "" {
		decision.Reasoning = append(decision.Reasoning, reason)
	}

	// Diagnostic suitability only — not the ranking key (contract P5).
	decision.Reasoning = append(decision.Reasoning,
		fmt.Sprintf("diagnostic suitability: %d/100 (%s; not the ranking key)", decision.FitScore, fitLabel(decision.FitScore)))

	// Locality
	if local {
		decision.Reasoning = append(decision.Reasoning, "local node (no SSH hop)")
	} else {
		decision.Reasoning = append(decision.Reasoning, "remote node (via SSH)")
	}

	// Resource details
	if best.Resources != nil {
		allocatable := freeRAMWithState(best)
		reservable := reservableRAM(best)
		if best.RAMReservedMB > 0 || best.RAMAllocatableMB > 0 {
			decision.Reasoning = append(decision.Reasoning,
				fmt.Sprintf("%dMB allocatable (%dMB reserved of %dMB reservable pool; %dMB total)",
					allocatable, reservedRAM(best), reservable, best.Resources.RAMTotalMB))
		} else {
			decision.Reasoning = append(decision.Reasoning,
				fmt.Sprintf("%dMB free RAM (of %dMB total)", best.Resources.RAMFreeMB, best.Resources.RAMTotalMB))
		}
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("pressure: %s", best.Resources.Pressure))
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("%d CPU cores", best.Resources.CPUCores))
		if len(best.Resources.GPUs) > 0 {
			decision.Reasoning = append(decision.Reasoning,
				fmt.Sprintf("GPU: %s", strings.Join(models.GPUNames(best.Resources.GPUs), ", ")))
		}
	}

	// Tool match
	if len(reqs.RequiredTools) > 0 {
		matched := make([]string, 0, len(reqs.RequiredTools))
		for _, required := range reqs.RequiredTools {
			for _, t := range best.Tools {
				if strings.EqualFold(t.Name, required) {
					if decision.Tool == "" {
						decision.Tool = t.Name
					}
					ver := t.Version
					if ver == "" {
						ver = "detected"
					}
					matched = append(matched, fmt.Sprintf("%s (%s)", t.Name, ver))
					break
				}
			}
			if strings.EqualFold(required, "ollama") && decision.Tool == "" {
				decision.Tool = "ollama"
			}
		}
		if len(matched) == 1 {
			decision.Reasoning = append(decision.Reasoning,
				fmt.Sprintf("required tool %s available", matched[0]))
		} else if len(matched) > 1 {
			decision.Reasoning = append(decision.Reasoning,
				fmt.Sprintf("required tools available: %s", strings.Join(matched, ", ")))
		}
	}

	// Runner-up comparison under the same headline metric, never FitScore.
	if len(ranked) > 1 {
		runnerUp := ranked[1]
		obj, _ := ResolveObjective(reqs)
		var ruMetric models.RankingMetric
		if obj == models.PlacementObjectiveSpread && len(rankedKeys) > 1 {
			ruMetric = models.RankingMetric{
				Name:       RankingMetricReservationSkew,
				Value:      rankedKeys[1].reservationSkew,
				Unit:       RankingMetricUnitStddev,
				Provenance: models.MetricProvenanceDerived,
			}
		} else if obj == models.PlacementObjectiveSpread {
			ruMetric = SpreadMetric(runnerUp, ranked, reqs)
		} else {
			ruMetric = CapacityMetric(runnerUp)
		}
		bestShare := clusterReservationShare(best, ranked)
		runnerShare := clusterReservationShare(runnerUp, ranked)
		runnerObservation := empiricalObservation(runnerUp, reqs, st)
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("selected from %d eligible nodes", len(ranked)))
		if compareObservationPreference(bestObservation, runnerObservation) > 0 && bestObservation != nil {
			decision.Reasoning = append(decision.Reasoning,
				fmt.Sprintf("empirical history favored %q over runner-up %q", best.Name, runnerUp.Name))
		}
		if residentModelRank(best, reqs) > residentModelRank(runnerUp, reqs) && residentModelRank(best, reqs) > 0 {
			decision.Reasoning = append(decision.Reasoning,
				fmt.Sprintf("resident model locality favored %q over runner-up %q", best.Name, runnerUp.Name))
		}
		if bestShare < runnerShare {
			decision.Reasoning = append(decision.Reasoning,
				fmt.Sprintf("lower cluster reservation share favored: %.0f%% vs runner-up %.0f%%", bestShare*100, runnerShare*100))
		}
		decision.Reasoning = append(decision.Reasoning,
			fmt.Sprintf("runner-up %q ranking metric: %s = %s (%s)", runnerUp.Name, ruMetric.Name, FormatRankingMetricValue(ruMetric), ruMetric.Provenance))
	}

	// Soft failure memory penalty reasoning
	if st != nil && st.Failures != nil {
		rec, ok := st.Failures.NarrowestMatch(models.FailureScope{
			Node:     best.Name,
			Workload: reqs.Workload.Class,
		})
		if ok && !isBlockingFailure(rec.Class) {
			decision.Reasoning = append(decision.Reasoning,
				fmt.Sprintf("penalized: %s recorded %d time(s) for this scope", rec.Class, rec.Count))
		}
	}

	return decision
}

// buildFailureDecision explains why every node was excluded.
func buildFailureDecision(reqs models.TaskRequirements, nodes []models.NodeFacts, st *state.ClusterState) models.PlacementDecision {
	d := models.PlacementDecision{
		OK:       false,
		Workload: reqs.Workload,
	}

	if len(nodes) == 0 {
		d.Reasoning = []string{"no nodes in cluster"}
		return d
	}

	d.Reasoning = []string{fmt.Sprintf("0 of %d nodes qualify", len(nodes))}

	if reqs.Workload.Class != "" && reqs.Workload.Class != models.ClassUnknown {
		d.Reasoning = append(d.Reasoning,
			fmt.Sprintf("workload class: %s", reqs.Workload.Class))
	}
	for _, note := range reqs.Workload.Notes {
		d.Reasoning = append(d.Reasoning, fmt.Sprintf("workload note: %s", note))
	}

	for _, eval := range evaluateCandidates(reqs, nodes, st) {
		for _, reason := range eval.ExclusionReasons {
			d.Reasoning = append(d.Reasoning, fmt.Sprintf("  %s: %s", eval.Node.Name, reason))
		}
	}

	if requiresTool(reqs.RequiredTools, "ollama") {
		d.Reasoning = append(d.Reasoning,
			"note: AXIS can bootstrap Ollama on partial nodes when tool is ollama")
		if reqs.PrefersTurboQuant {
			d.Reasoning = append(d.Reasoning,
				"note: long-context tasks prefer TurboQuant-capable backends (mlx, llama.cpp) when available")
		}
	} else if reqs.MinFreeRAMMB >= 4096 {
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
