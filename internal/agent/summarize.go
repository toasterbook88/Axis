package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

// summarizeSnapshot returns a comprehensive human-readable summary of cluster state
// suitable for feeding back to an LLM with full fleet visibility.
func summarizeSnapshot(snap *models.ClusterSnapshot) string {
	if snap == nil {
		return "No cluster snapshot available — cluster may not be configured."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Cluster: %d nodes (%d reachable), status: %s",
		snap.Summary.TotalNodes, snap.Summary.ReachableNodes, snap.Status)
	if !snap.Timestamp.IsZero() {
		age := time.Since(snap.Timestamp).Round(time.Second)
		if age >= 0 {
			fmt.Fprintf(&b, " (age: %s)", age)
		}
	}
	b.WriteByte('\n')
	if snap.Summary.TotalRAMMB > 0 {
		fmt.Fprintf(&b, "Total RAM: %d MB total, %d MB free\n",
			snap.Summary.TotalRAMMB, snap.Summary.TotalFreeRAMMB)
	}

	for _, n := range snap.Nodes {
		status := string(n.Status)
		if n.Error != "" {
			status += " — " + truncateRunes(n.Error, 50)
		}
		line := fmt.Sprintf("- %s (%s, %s): %s", n.Name, n.OS+"/"+n.Arch, n.Hostname, status)
		if n.Resources != nil {
			line += fmt.Sprintf(", %d/%d MB free RAM, %d cores", n.Resources.RAMFreeMB, n.Resources.RAMTotalMB, n.Resources.CPUCores)
			if len(n.Resources.GPUs) > 0 {
				gpuNames := make([]string, 0, len(n.Resources.GPUs))
				for _, g := range n.Resources.GPUs {
					gpuNames = append(gpuNames, g.GPUName())
				}
				line += fmt.Sprintf(", GPUs: %s", strings.Join(gpuNames, ", "))
			}
		}
		if len(n.ResidentModels) > 0 {
			modelParts := make([]string, 0, len(n.ResidentModels))
			for _, rm := range n.ResidentModels {
				p := rm.Name
				if rm.SizeVRAMMB > 0 {
					p += fmt.Sprintf(" (%dMB VRAM)", rm.SizeVRAMMB)
				} else if rm.SizeRAMMB > 0 {
					p += fmt.Sprintf(" (%dMB RAM)", rm.SizeRAMMB)
				}
				modelParts = append(modelParts, p)
			}
			line += fmt.Sprintf(", resident: %s", strings.Join(modelParts, ", "))
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}

	if len(snap.Warnings) > 0 {
		b.WriteString("Warnings:\n")
		for _, w := range snap.Warnings {
			fmt.Fprintf(&b, "- %s: %s\n", w.Node, truncateRunes(w.Message, 80))
		}
	}

	return b.String()
}

// summarizeNodeFacts returns a comprehensive human-readable summary of a single node.
func summarizeNodeFacts(n models.NodeFacts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Node: %s (%s/%s, %s)\n", n.Name, n.OS, n.Arch, n.Hostname)
	if n.Role != "" {
		fmt.Fprintf(&b, "Role: %s\n", n.Role)
	}
	if n.Resources != nil {
		r := n.Resources
		fmt.Fprintf(&b, "CPU: %d cores (%s)\n", r.CPUCores, truncateRunes(r.CPUModel, 50))
		fmt.Fprintf(&b, "RAM: %d MB total, %d MB free\n", r.RAMTotalMB, r.RAMFreeMB)
		fmt.Fprintf(&b, "Disk: %d GB total, %d GB free\n", r.DiskTotalGB, r.DiskFreeGB)
		if r.Load1M > 0 {
			fmt.Fprintf(&b, "Load: %.2f (1m)\n", r.Load1M)
		}
		if len(r.GPUs) > 0 {
			for _, g := range r.GPUs {
				fmt.Fprintf(&b, "GPU: %s (%s, %d MB VRAM)\n", g.GPUName(), g.Vendor, g.VRAMMB)
			}
		}
		if r.Pressure != "" && r.Pressure != "none" {
			fmt.Fprintf(&b, "Pressure: %s\n", r.Pressure)
		}
		if r.ThermalState != "" && r.ThermalState != "nominal" {
			fmt.Fprintf(&b, "Thermal: %s\n", r.ThermalState)
		}
	}
	if len(n.ResidentModels) > 0 {
		fmt.Fprintf(&b, "Resident models (%d):\n", len(n.ResidentModels))
		for _, rm := range n.ResidentModels {
			mem := ""
			if rm.SizeVRAMMB > 0 {
				mem = fmt.Sprintf(", %d MB VRAM", rm.SizeVRAMMB)
			} else if rm.SizeRAMMB > 0 {
				mem = fmt.Sprintf(", %d MB RAM", rm.SizeRAMMB)
			}
			fmt.Fprintf(&b, "- %s (%s, port %d%s)\n", rm.Name, rm.Runtime, rm.Port, mem)
		}
	}
	if n.Ollama != nil && n.Ollama.Installed {
		modelNames := n.Ollama.Models
		if len(modelNames) > 0 {
			fmt.Fprintf(&b, "Ollama: %s (%d models: %s)\n", n.Ollama.Version, len(modelNames), strings.Join(modelNames, ", "))
		} else {
			fmt.Fprintf(&b, "Ollama: %s (0 models)\n", n.Ollama.Version)
		}
	}
	if len(n.Tools) > 0 {
		toolNames := make([]string, 0, len(n.Tools))
		for _, t := range n.Tools {
			toolNames = append(toolNames, t.Name)
		}
		fmt.Fprintf(&b, "Tools: %s\n", strings.Join(toolNames, ", "))
	}
	fmt.Fprintf(&b, "Status: %s\n", n.Status)
	if n.Error != "" {
		fmt.Fprintf(&b, "Error: %s\n", n.Error)
	}
	return b.String()
}

// summarizePlacementDecision returns a compact human-readable summary.
func summarizePlacementDecision(dec models.PlacementDecision) string {
	if !dec.OK {
		return "Placement: no suitable node found for this task."
	}
	var b strings.Builder
	if dec.Ranking != nil {
		fmt.Fprintf(&b, "Placement: %s (%s = %.0f%s, provenance: %s", dec.Node, dec.Ranking.Metric.Name, dec.Ranking.Metric.Value, dec.Ranking.Metric.Unit, dec.Ranking.Metric.Provenance)
	} else {
		fmt.Fprintf(&b, "Placement: %s (diagnostic suitability: %d/100", dec.Node, dec.FitScore)
	}
	if dec.IsLocal {
		b.WriteString(", local")
	}
	b.WriteString(")\n")
	if dec.Ranking != nil {
		fmt.Fprintf(&b, "Ranking objective: %s (%s); decisive criterion: %s\n", dec.Ranking.Objective, dec.Ranking.Source, dec.Ranking.DecisiveBy)
		if tie := dec.Ranking.TieBreak; tie != nil {
			fmt.Fprintf(&b, "Tie-break: %s (%s vs %s)\n", tie.Criterion, tie.WinnerValue, tie.RunnerValue)
		}
	}
	if dec.Workload.Class != "" {
		fmt.Fprintf(&b, "Workload class: %s\n", dec.Workload.Class)
	}
	if len(dec.Reasoning) > 0 {
		b.WriteString("Reasoning:\n")
		for _, r := range dec.Reasoning {
			fmt.Fprintf(&b, "- %s\n", r)
		}
	}
	return b.String()
}

// summarizePlacementExplanation returns a comprehensive human-readable summary
// of placement decision, runner-up candidates, and exclusion reasons without arbitrary caps.
func summarizePlacementExplanation(exp models.PlacementExplanation) string {
	var b strings.Builder
	b.WriteString(summarizePlacementDecision(exp.Decision))
	if exp.Decision.OK {
		var runnerUps []models.PlacementCandidateExplanation
		for _, cand := range exp.Eligible {
			if cand.Node != exp.Decision.Node {
				runnerUps = append(runnerUps, cand)
			}
		}

		if len(runnerUps) > 0 {
			b.WriteString("\nRunner-up candidates:\n")
			for _, r := range runnerUps {
				line := fmt.Sprintf("- %s: %s = %.0f%s (suitability: %d/100", r.Node, r.Metric.Name, r.Metric.Value, r.Metric.Unit, r.FitScore)
				if r.IsLocal {
					line += ", local"
				}
				if r.HeadroomMB > 0 {
					line += fmt.Sprintf(", headroom: %dMB", r.HeadroomMB)
				}
				line += ")"
				b.WriteString(line)
				b.WriteByte('\n')
			}
		}
	}

	if len(exp.Excluded) > 0 {
		b.WriteString("\nExcluded nodes:\n")
		for _, ex := range exp.Excluded {
			b.WriteString(fmt.Sprintf("- %s: %s\n", ex.Node, strings.Join(ex.Reasons, ", ")))
		}
	}

	return b.String()
}

// summarizeClusterModels returns a structured overview of resident models,
// local Ollama models on disk, and configured AI roles/backends.
func summarizeClusterModels(snap *models.ClusterSnapshot, aiCfg *config.AIConfig, nodeFilter string) string {
	var b strings.Builder
	filter := strings.ToLower(strings.TrimSpace(nodeFilter))

	// 1. Active Resident Models (in VRAM/RAM)
	b.WriteString("Active Resident Models (in VRAM/RAM):\n")
	residentCount := 0
	if snap != nil {
		for _, n := range snap.Nodes {
			if filter != "" && !strings.EqualFold(n.Name, filter) && !strings.EqualFold(n.Hostname, filter) {
				continue
			}
			for _, rm := range n.ResidentModels {
				residentCount++
				mem := ""
				if rm.SizeVRAMMB > 0 {
					mem = fmt.Sprintf("%d MB VRAM", rm.SizeVRAMMB)
				} else if rm.SizeRAMMB > 0 {
					mem = fmt.Sprintf("%d MB RAM", rm.SizeRAMMB)
				}
				portStr := ""
				if rm.Port > 0 {
					portStr = fmt.Sprintf(", port %d", rm.Port)
				}
				warmthStr := ""
				if rm.WarmthScore > 0 {
					warmthStr = fmt.Sprintf(", warmth: %.0f/100", rm.WarmthScore)
				}
				fmt.Fprintf(&b, "- %s on %s (%s%s, %s%s)\n",
					rm.Name, n.Name, rm.Runtime, portStr, mem, warmthStr)
			}
		}
	}
	if residentCount == 0 {
		b.WriteString("- None currently resident (no warm models active)\n")
	}

	// 2. Local Ollama Models (on disk)
	b.WriteString("\nLocal Ollama Models (on disk):\n")
	ollamaCount := 0
	if snap != nil {
		for _, n := range snap.Nodes {
			if filter != "" && !strings.EqualFold(n.Name, filter) && !strings.EqualFold(n.Hostname, filter) {
				continue
			}
			if n.Ollama != nil && n.Ollama.Installed && len(n.Ollama.Models) > 0 {
				modelNames := n.Ollama.Models
				ollamaCount += len(modelNames)
				fmt.Fprintf(&b, "- %s (%d models): %s\n", n.Name, len(modelNames), strings.Join(modelNames, ", "))
			}
		}
	}
	if ollamaCount == 0 {
		b.WriteString("- No local Ollama models found\n")
	}

	// 3. Configured AI Roles & Backends (from ai.yaml)
	if aiCfg != nil {
		if len(aiCfg.Roles) > 0 {
			b.WriteString("\nConfigured AI Roles (routing):\n")
			for roleName, r := range aiCfg.Roles {
				pref := strings.Join(r.Prefer, " -> ")
				fmt.Fprintf(&b, "- role %q -> model %q (prefer: %s)\n", roleName, r.Model, pref)
			}
		}
		if len(aiCfg.Backends) > 0 {
			b.WriteString("\nAI Backends:\n")
			for _, be := range aiCfg.Backends {
				if filter != "" && !strings.EqualFold(be.Node, filter) {
					continue
				}
				status := "enabled"
				if !be.IsEnabled() {
					status = "disabled"
				}
				nodeStr := be.Node
				if nodeStr == "" {
					nodeStr = "unspecified"
				}
				endpoint := be.BaseURL
				if be.AdvertiseURL != "" {
					endpoint += " (" + be.AdvertiseURL + ")"
				}
				fmt.Fprintf(&b, "- %s (%s, node: %s, %s): %s\n", be.Name, be.Kind, nodeStr, status, endpoint)
			}
		}
	}

	return b.String()
}
