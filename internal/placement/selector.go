package placement

import (
	"fmt"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

// SelectBestNode runs the full placement pipeline: filter → rank → select.
// Reasoning is diagnostic: on failure it explains why each node was excluded;
// on success it explains fit score, locality, and runner-up comparison.
func SelectBestNode(reqs models.TaskRequirements, nodes []models.NodeFacts, st *state.ClusterState) models.PlacementDecision {
	candidates := FilterCandidates(reqs, nodes, st)
	if len(candidates) == 0 {
		return buildFailureDecision(reqs, nodes, st)
	}

	ranked := RankCandidates(candidates, reqs, st)
	best := ranked[0]
	local := models.IsLocalNode(best)

	decision := buildSuccessDecision(best, ranked, reqs, local, st)
	decision.Workload = reqs.Workload
	return decision
}

func buildSuccessDecision(best models.NodeFacts, ranked []models.NodeFacts, reqs models.TaskRequirements, local bool, st *state.ClusterState) models.PlacementDecision {
	decision := models.PlacementDecision{
		Node:     best.Name,
		OK:       true,
		FitScore: ComputeTaskFitScore(best, local, st, reqs),
		IsLocal:  local,
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
	if requiresAppleFoundationModels(reqs) && models.IsLocalNode(best) {
		decision.Reasoning = append(decision.Reasoning, "local Apple Foundation Models path verified")
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

	// Fit score summary
	fitLabel := fitLabel(decision.FitScore)
	decision.Reasoning = append(decision.Reasoning,
		fmt.Sprintf("LLM fit: %d/100 (%s)", decision.FitScore, fitLabel))

	// Locality
	if local {
		decision.Reasoning = append(decision.Reasoning, "local node (no SSH hop)")
	} else {
		decision.Reasoning = append(decision.Reasoning, "remote node (via SSH)")
	}

	// Resource details
	if best.Resources != nil {
		allocatable := freeRAMWithState(best, st)
		if best.Resources.RAMReservedMB > 0 || best.Resources.RAMAllocatableMB > 0 {
			decision.Reasoning = append(decision.Reasoning,
				fmt.Sprintf("%dMB allocatable (%dMB reserved) of %dMB total",
					allocatable, reservedRAM(best, st), best.Resources.RAMTotalMB))
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

	// Runner-up comparison
	if len(ranked) > 1 {
		runnerUp := ranked[1]
		ruLocal := models.IsLocalNode(runnerUp)
		ruScore := ComputeTaskFitScore(runnerUp, ruLocal, st, reqs)
		bestShare := clusterReservationShare(best, st)
		runnerShare := clusterReservationShare(runnerUp, st)
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
			fmt.Sprintf("runner-up %q scored %d/100", runnerUp.Name, ruScore))
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

	for _, n := range nodes {
		if requiresAppleFoundationModels(reqs) {
			switch {
			case !models.IsLocalNode(n):
				d.Reasoning = append(d.Reasoning,
					fmt.Sprintf("  %s: excluded (apple foundation models are local-only)", n.Name))
				continue
			case !appleFoundationModelsReady(n):
				d.Reasoning = append(d.Reasoning,
					fmt.Sprintf("  %s: excluded (apple foundation models not verified on local node)", n.Name))
				continue
			}
		}
		if n.Status != models.StatusComplete {
			d.Reasoning = append(d.Reasoning,
				fmt.Sprintf("  %s: excluded (status: %s)", n.Name, n.Status))
			continue
		}
		if blocksForRuntimePressure(reqs, n) {
			if n.Resources != nil && n.Resources.PressureSource == "linux-psi" {
				d.Reasoning = append(d.Reasoning,
					fmt.Sprintf("  %s: excluded (critical runtime memory pressure via linux-psi avg10=%.2f)", n.Name, n.Resources.PressureStall10))
			} else if n.Resources != nil && n.Resources.PressureSource == "darwin-vm-pressure" {
				d.Reasoning = append(d.Reasoning,
					fmt.Sprintf("  %s: excluded (critical runtime memory pressure via darwin vm pressure)", n.Name))
			} else {
				d.Reasoning = append(d.Reasoning,
					fmt.Sprintf("  %s: excluded (critical runtime memory pressure)", n.Name))
			}
			continue
		}
		if blocksForThermalOrBattery(reqs, n) {
			if n.Resources != nil && n.Resources.BatteryPercent != nil && *n.Resources.BatteryPercent < 20 {
				d.Reasoning = append(d.Reasoning, fmt.Sprintf("  %s: excluded (battery critically low: %d%%)", n.Name, *n.Resources.BatteryPercent))
			} else {
				d.Reasoning = append(d.Reasoning, fmt.Sprintf("  %s: excluded (thermal throttling state: %s)", n.Name, n.Resources.ThermalState))
			}
			continue
		}
		if st != nil && st.Failures != nil {
			if rec, ok := st.Failures.NarrowestMatch(models.FailureScope{Node: n.Name, Workload: reqs.Workload.Class}); ok && isBlockingFailure(rec.Class) {
				d.Reasoning = append(d.Reasoning,
					fmt.Sprintf("  %s: blocked (%s repeated %d time(s) for this scope)", n.Name, rec.Class, rec.Count))
				continue
			}
		}
		if reqs.MinFreeRAMMB > 0 {
			minNeeded := effectiveMinFreeRAM(reqs, n)
			actual := int64(0)
			effective := int64(0)
			if n.Resources != nil {
				actual = n.Resources.RAMFreeMB
				effective = freeRAMWithState(n, st)
			}
			if effective < minNeeded {
				d.Reasoning = append(d.Reasoning,
					fmt.Sprintf("  %s: need %dMB free RAM, have %dMB effective (base %dMB, short %dMB)",
						n.Name, minNeeded, effective, actual, minNeeded-effective))
				continue
			}
		}
		if len(reqs.RequiredTools) > 0 && !satisfiesRequiredTools(n, reqs.RequiredTools) {
			missing := make([]string, 0, len(reqs.RequiredTools))
			for _, required := range reqs.RequiredTools {
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
			d.Reasoning = append(d.Reasoning,
				fmt.Sprintf("  %s: missing required tools %v (has: %v)",
					n.Name, missing, available))
			continue
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
