package agent

import (
	"fmt"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// summarizeSnapshot returns a compact human-readable summary of cluster state
// suitable for feeding back to an LLM. Keeps output under ~600 chars.
func summarizeSnapshot(snap *models.ClusterSnapshot) string {
	if snap == nil {
		return "No cluster snapshot available — cluster may not be configured."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Cluster: %d nodes (%d reachable), status: %s\n",
		snap.Summary.TotalNodes, snap.Summary.ReachableNodes, snap.Status)
	if snap.Summary.TotalRAMMB > 0 {
		fmt.Fprintf(&b, "Total RAM: %d MB total, %d MB free\n",
			snap.Summary.TotalRAMMB, snap.Summary.TotalFreeRAMMB)
	}

	for i, n := range snap.Nodes {
		if i >= 6 {
			fmt.Fprintf(&b, "... and %d more nodes\n", len(snap.Nodes)-i)
			break
		}
		status := string(n.Status)
		if n.Error != "" {
			status += " — " + truncate(n.Error, 40)
		}
		line := fmt.Sprintf("- %s (%s): %s", n.Name, n.Hostname, status)
		if n.Resources != nil {
			line += fmt.Sprintf(", %d MB free RAM, %d cores", n.Resources.RAMFreeMB, n.Resources.CPUCores)
			if len(n.Resources.GPUs) > 0 {
				gpuNames := make([]string, 0, len(n.Resources.GPUs))
				for _, g := range n.Resources.GPUs {
					gpuNames = append(gpuNames, g.GPUName())
				}
				line += fmt.Sprintf(", GPUs: %s", strings.Join(gpuNames, ", "))
			}
		}
		b.WriteString(line + "\n")
	}

	if len(snap.Warnings) > 0 {
		b.WriteString("Warnings:\n")
		for i, w := range snap.Warnings {
			if i >= 3 {
				fmt.Fprintf(&b, "... and %d more warnings\n", len(snap.Warnings)-i)
				break
			}
			fmt.Fprintf(&b, "- %s: %s\n", w.Node, truncate(w.Message, 60))
		}
	}

	return b.String()
}

// summarizeNodeFacts returns a compact human-readable summary of a single node.
func summarizeNodeFacts(n models.NodeFacts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Node: %s (%s/%s, %s)\n", n.Name, n.OS, n.Arch, n.Hostname)
	if n.Resources != nil {
		r := n.Resources
		fmt.Fprintf(&b, "CPU: %d cores (%s)\n", r.CPUCores, truncate(r.CPUModel, 40))
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
	if len(n.Tools) > 0 {
		toolNames := make([]string, 0, len(n.Tools))
		for _, t := range n.Tools {
			toolNames = append(toolNames, t.Name)
		}
		fmt.Fprintf(&b, "Tools: %s\n", strings.Join(toolNames, ", "))
	}
	if n.Ollama != nil && n.Ollama.Installed {
		fmt.Fprintf(&b, "Ollama: %s (%d models)\n", n.Ollama.Version, len(n.Ollama.Models))
	}
	fmt.Fprintf(&b, "Status: %s\n", n.Status)
	if n.Error != "" {
		fmt.Fprintf(&b, "Error: %s\n", truncate(n.Error, 100))
	}
	return b.String()
}

// summarizePlacementDecision returns a compact human-readable summary.
func summarizePlacementDecision(dec models.PlacementDecision) string {
	if !dec.OK {
		return "Placement: no suitable node found for this task."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Placement: %s (fit score: %d/100", dec.Node, dec.FitScore)
	if dec.IsLocal {
		b.WriteString(", local")
	}
	b.WriteString(")\n")
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

// truncate truncates a string to maxLen runes, appending "..." if truncated.
// Safe for UTF-8 — operates on runes, not bytes.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}
