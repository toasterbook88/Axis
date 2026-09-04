package modelplan

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

// CandidateFit describes the quality of a placement fit.
type CandidateFit string

const (
	FitExcellent CandidateFit = "excellent"
	FitGood      CandidateFit = "good"
	FitMarginal  CandidateFit = "marginal"
)

// ModelCandidateScore is a scored placement candidate node.
type ModelCandidateScore struct {
	Node            string       `json:"node" yaml:"node"`
	Score           int          `json:"score" yaml:"score"`
	Fit             CandidateFit `json:"fit" yaml:"fit"`
	Accelerator     string       `json:"accelerator" yaml:"accelerator"`
	VRAMTotalMB     int64        `json:"vram_total_mb" yaml:"vram_total_mb"`
	VRAMFreeMB      int64        `json:"vram_free_mb" yaml:"vram_free_mb"`
	RAMFreeMB       int64        `json:"ram_free_mb" yaml:"ram_free_mb"`
	RAMTotalMB      int64        `json:"ram_total_mb" yaml:"ram_total_mb"`
	PortAvailable   bool         `json:"port_available" yaml:"port_available"`
	HasLocalWeights bool         `json:"has_local_weights" yaml:"has_local_weights"`
	Reasoning       []string     `json:"reasoning" yaml:"reasoning"`
}

// ModelExcludedCandidate describes a node excluded from placement with reasons.
type ModelExcludedCandidate struct {
	Node    string   `json:"node" yaml:"node"`
	Reasons []string `json:"reasons" yaml:"reasons"`
}

// ModelPlacementPlan is a dry-run advisory placement proposal.
type ModelPlacementPlan struct {
	Schema         string                   `json:"schema" yaml:"schema"`
	Spec           models.ModelSpec         `json:"spec" yaml:"spec"`
	TargetPort     int                      `json:"target_port" yaml:"target_port"`
	PlannedAt      time.Time                `json:"planned_at" yaml:"planned_at"`
	SnapshotSource string                   `json:"snapshot_source,omitempty" yaml:"snapshot_source,omitempty"`
	PublicationID  string                   `json:"publication_id,omitempty" yaml:"publication_id,omitempty"`
	Candidates     []ModelCandidateScore    `json:"candidates" yaml:"candidates"`
	Excluded       []ModelExcludedCandidate `json:"excluded" yaml:"excluded"`
	BestCandidate  string                   `json:"best_candidate,omitempty" yaml:"best_candidate,omitempty"`
}

// PlanSingleNode performs a dry-run evaluation of cluster nodes against available
// RAM, VRAM, and port availability to output scored placement candidates without mutating services.
func PlanSingleNode(snapshot *models.ClusterSnapshot, spec models.ModelSpec, targetPort int) (ModelPlacementPlan, error) {
	if snapshot == nil {
		return ModelPlacementPlan{}, fmt.Errorf("no cluster snapshot available")
	}
	if err := spec.Validate(); err != nil {
		return ModelPlacementPlan{}, fmt.Errorf("invalid model spec: %w", err)
	}
	if targetPort < 1 || targetPort > 65535 {
		return ModelPlacementPlan{}, fmt.Errorf("target port must be between 1 and 65535")
	}

	plan := ModelPlacementPlan{
		Schema:     "axis.model-plan/v1",
		Spec:       spec,
		TargetPort: targetPort,
		PlannedAt:  time.Now().UTC(),
		Candidates: make([]ModelCandidateScore, 0),
		Excluded:   make([]ModelExcludedCandidate, 0),
	}
	if snapshot.Publication != nil {
		plan.PublicationID = snapshot.Publication.ID
	}

	reqMemory := spec.Memory.TotalMemoryMB()

	for _, node := range snapshot.Nodes {
		var exclusions []string

		// 1. Node status check
		if node.Status != models.StatusComplete {
			exclusions = append(exclusions, fmt.Sprintf("node status %s is not complete", node.Status))
		}

		// 2. Port availability check
		portOccupied := false
		var occupiedBy string
		for _, res := range node.ResidentModels {
			if res.Port == targetPort {
				portOccupied = true
				occupiedBy = fmt.Sprintf("%s (%s)", res.Name, res.Runtime)
				break
			}
		}
		if portOccupied {
			exclusions = append(exclusions, fmt.Sprintf("target port %d occupied by resident model %s", targetPort, occupiedBy))
		}

		// 3. Memory & Accelerator analysis
		var ramFreeMB, ramTotalMB int64
		if node.Resources != nil {
			ramFreeMB = node.Resources.RAMFreeMB
			ramTotalMB = node.Resources.RAMTotalMB
		}

		vramTotalMB, vramFreeMB, accName, hasAcc := evaluateNodeAccelerator(node, spec.Accelerators)

		// Check if required memory can be satisfied by VRAM, RAM, or a combination
		canFitVRAM := hasAcc && (vramTotalMB >= reqMemory || vramFreeMB >= reqMemory)
		canFitRAM := ramFreeMB >= reqMemory

		if !canFitVRAM && !canFitRAM && (vramFreeMB+ramFreeMB < reqMemory) {
			exclusions = append(exclusions, fmt.Sprintf("insufficient memory: required %d MiB, but node has %d MiB free RAM and %d MiB free VRAM",
				reqMemory, ramFreeMB, vramFreeMB))
		}

		// Check minimum VRAM requirement if declared
		if spec.Memory.MinVRAMMB > 0 && vramFreeMB < spec.Memory.MinVRAMMB {
			exclusions = append(exclusions, fmt.Sprintf("insufficient VRAM for minimum offload: requires %d MiB, node has %d MiB",
				spec.Memory.MinVRAMMB, vramFreeMB))
		}

		// 4. Pressure / thermal guardrails
		if node.Resources != nil {
			if node.Resources.MemoryPSIFullAvg10 > 70.0 {
				exclusions = append(exclusions, fmt.Sprintf("severe memory pressure (PSI full avg10: %.1f%%)", node.Resources.MemoryPSIFullAvg10))
			}
			if node.Resources.ThermalState == "critical" {
				exclusions = append(exclusions, "critical thermal throttling")
			}
		}

		if len(exclusions) > 0 {
			plan.Excluded = append(plan.Excluded, ModelExcludedCandidate{
				Node:    node.Name,
				Reasons: exclusions,
			})
			continue
		}

		// Eligible candidate - compute fit score
		candidate := scoreEligibleNode(node, spec, targetPort, ramFreeMB, ramTotalMB, vramTotalMB, vramFreeMB, accName, hasAcc)
		plan.Candidates = append(plan.Candidates, candidate)
	}

	// Sort candidates descending by score, breaking ties by VRAM then RAM headroom then node name
	sort.Slice(plan.Candidates, func(i, j int) bool {
		ci, cj := plan.Candidates[i], plan.Candidates[j]
		if ci.Score != cj.Score {
			return ci.Score > cj.Score
		}
		if ci.VRAMFreeMB != cj.VRAMFreeMB {
			return ci.VRAMFreeMB > cj.VRAMFreeMB
		}
		if ci.RAMFreeMB != cj.RAMFreeMB {
			return ci.RAMFreeMB > cj.RAMFreeMB
		}
		return ci.Node < cj.Node
	})

	// Sort excluded nodes alphabetically by name
	sort.Slice(plan.Excluded, func(i, j int) bool {
		return plan.Excluded[i].Node < plan.Excluded[j].Node
	})

	if len(plan.Candidates) > 0 {
		plan.BestCandidate = plan.Candidates[0].Node
	}

	return plan, nil
}

func evaluateNodeAccelerator(node models.NodeFacts, requested []models.AcceleratorType) (int64, int64, string, bool) {
	if node.Resources == nil || len(node.Resources.GPUs) == 0 {
		return 0, 0, "cpu", false
	}

	var totalVRAM, freeVRAM int64
	var bestAcc string
	hasCompatible := false

	for _, gpu := range node.Resources.GPUs {
		vram := int64(gpu.VRAMMB)
		totalVRAM += vram
		// If explicit free VRAM isn't available, treat total as potential capacity
		freeVRAM += vram

		gpuAcc := "unknown"
		if gpu.HasCapability("cuda") || strings.EqualFold(gpu.Vendor, "nvidia") {
			gpuAcc = "cuda"
		} else if gpu.HasCapability("metal") || strings.EqualFold(gpu.Vendor, "apple") {
			gpuAcc = "metal"
		} else if gpu.HasCapability("rocm") || strings.EqualFold(gpu.Vendor, "amd") {
			gpuAcc = "rocm"
		}

		if matchesAccelerator(gpuAcc, requested) {
			hasCompatible = true
			if bestAcc == "" {
				bestAcc = fmt.Sprintf("%s (%s)", gpu.Model, gpuAcc)
			}
		}
	}

	if !hasCompatible {
		return 0, 0, "cpu", false
	}
	return totalVRAM, freeVRAM, bestAcc, true
}

func matchesAccelerator(acc string, requested []models.AcceleratorType) bool {
	if len(requested) == 0 {
		return true
	}
	for _, req := range requested {
		if strings.EqualFold(acc, string(req)) {
			return true
		}
	}
	return false
}

func hasLocalWeights(node models.NodeFacts, spec models.ModelSpec) bool {
	specName := strings.ToLower(strings.TrimSpace(spec.Name))
	for _, dw := range node.DiskWeights {
		if strings.EqualFold(dw.Name, spec.Name) || strings.EqualFold(dw.Path, spec.WeightsPath) {
			return true
		}
		if specName != "" && strings.Contains(strings.ToLower(dw.Path), specName) {
			return true
		}
	}
	return false
}

func scoreEligibleNode(
	node models.NodeFacts,
	spec models.ModelSpec,
	targetPort int,
	ramFreeMB, ramTotalMB, vramTotalMB, vramFreeMB int64,
	accName string,
	hasAcc bool,
) ModelCandidateScore {
	score := 50
	var reasoning []string

	reqMemory := spec.Memory.TotalMemoryMB()

	// 1. Accelerator offload capability
	if hasAcc && vramFreeMB >= reqMemory {
		score += 30
		reasoning = append(reasoning, fmt.Sprintf("full accelerator offload possible (%d MiB VRAM free >= %d MiB required)", vramFreeMB, reqMemory))
	} else if hasAcc && vramFreeMB >= spec.Memory.WeightSizeMB {
		score += 20
		reasoning = append(reasoning, fmt.Sprintf("weights fit in VRAM (%d MiB free >= %d MiB weights)", vramFreeMB, spec.Memory.WeightSizeMB))
	} else if hasAcc && vramFreeMB > 0 {
		score += 10
		reasoning = append(reasoning, fmt.Sprintf("partial VRAM offload possible (%d MiB free)", vramFreeMB))
	} else {
		reasoning = append(reasoning, "CPU inference execution")
	}

	// 2. RAM Headroom
	if ramFreeMB >= reqMemory*2 {
		score += 15
		reasoning = append(reasoning, fmt.Sprintf("ample RAM headroom (%d MiB free > 2x required)", ramFreeMB))
	} else if ramFreeMB >= reqMemory {
		score += 10
		reasoning = append(reasoning, fmt.Sprintf("sufficient RAM headroom (%d MiB free)", ramFreeMB))
	}

	// 3. Local weights presence
	localWeights := hasLocalWeights(node, spec)
	if localWeights {
		score += 15
		reasoning = append(reasoning, "model weights present on local volume (zero download/transfer overhead)")
	}

	// 4. Memory pressure penalties
	if node.Resources != nil && node.Resources.MemoryPSISomeAvg10 > 20.0 {
		score -= 10
		reasoning = append(reasoning, fmt.Sprintf("moderate memory pressure (PSI some: %.1f%%)", node.Resources.MemoryPSISomeAvg10))
	}

	// Normalize score to 1..100
	if score > 100 {
		score = 100
	}
	if score < 1 {
		score = 1
	}

	fit := FitGood
	if score >= 80 {
		fit = FitExcellent
	} else if score < 50 {
		fit = FitMarginal
	}

	return ModelCandidateScore{
		Node:            node.Name,
		Score:           score,
		Fit:             fit,
		Accelerator:     accName,
		VRAMTotalMB:     vramTotalMB,
		VRAMFreeMB:      vramFreeMB,
		RAMFreeMB:       ramFreeMB,
		RAMTotalMB:      ramTotalMB,
		PortAvailable:   true,
		HasLocalWeights: localWeights,
		Reasoning:       reasoning,
	}
}

// FormatModelPlacementPlanText formats a placement plan for human terminal display.
func FormatModelPlacementPlanText(plan ModelPlacementPlan) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("MODEL PLACEMENT PLAN: %s\n", plan.Spec.Name))
	sb.WriteString(fmt.Sprintf("Spec ID: %s | Format: %s\n", plan.Spec.ID, plan.Spec.Format))
	sb.WriteString(fmt.Sprintf("Required Memory: %d MiB (Weights: %d MiB, Context: %d MiB, Runtime: %d MiB)\n",
		plan.Spec.Memory.TotalMemoryMB(), plan.Spec.Memory.WeightSizeMB, plan.Spec.Memory.ContextOverheadMB, plan.Spec.Memory.RuntimeOverheadMB))
	sb.WriteString(fmt.Sprintf("Target Port: %d\n", plan.TargetPort))
	if plan.PublicationID != "" {
		sb.WriteString(fmt.Sprintf("Snapshot Publication: %s\n", plan.PublicationID))
	}
	sb.WriteString("\n")

	if len(plan.Candidates) > 0 {
		sb.WriteString(fmt.Sprintf("ELIGIBLE CANDIDATES (%d):\n", len(plan.Candidates)))
		for i, c := range plan.Candidates {
			localNote := ""
			if c.HasLocalWeights {
				localNote = " [local weights present]"
			}
			accText := c.Accelerator
			if c.VRAMFreeMB > 0 {
				accText = fmt.Sprintf("%s (VRAM: %d MiB)", c.Accelerator, c.VRAMFreeMB)
			}
			sb.WriteString(fmt.Sprintf("  %d. %-12s Score: %2d/100 (%-9s)  RAM: %6d MiB free  Acc: %-25s Port %d: free%s\n",
				i+1, c.Node, c.Score, c.Fit, c.RAMFreeMB, accText, plan.TargetPort, localNote))
			for _, r := range c.Reasoning {
				sb.WriteString(fmt.Sprintf("       • %s\n", r))
			}
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("NO ELIGIBLE CANDIDATES FOUND\n\n")
	}

	if len(plan.Excluded) > 0 {
		sb.WriteString(fmt.Sprintf("EXCLUDED NODES (%d):\n", len(plan.Excluded)))
		for _, ex := range plan.Excluded {
			sb.WriteString(fmt.Sprintf("  • %-12s %s\n", ex.Node, strings.Join(ex.Reasons, "; ")))
		}
		sb.WriteString("\n")
	}

	if plan.BestCandidate != "" {
		sb.WriteString(fmt.Sprintf("Recommended Placement: %s\n", plan.BestCandidate))
	} else {
		sb.WriteString("Recommended Placement: none (no eligible nodes meet requirements)\n")
	}

	return sb.String()
}
