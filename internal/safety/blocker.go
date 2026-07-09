package safety

import (
	"fmt"
	"strings"

	"github.com/toasterbook88/axis/internal/knowledge"
)

// BlockResult is final verdict. Score >= 80 = instant block.
type BlockResult struct {
	Blocked bool   `json:"blocked"`
	Reason  string `json:"reason"`
	Score   int    `json:"dumb_score"` // 0-100
}

var defaultEvaluator = NewEvaluator(DefaultRuleSet())

// Check is the single gatekeeper. Call this first, always.
func Check(k *knowledge.ClusterKnowledge, desc string, isKnownBad func(string) bool) BlockResult {
	lower := strings.ToLower(desc)
	r := BlockResult{Score: 0}

	// === Learned bad commands (fast path - highest priority) ===
	if isKnownBad != nil && isKnownBad(desc) {
		return BlockResult{Blocked: true, Reason: "this exact command failed before", Score: 92}
	}

	// === Structured Safety Evaluator (delegation path) ===
	decision := defaultEvaluator.Evaluate(desc, "agent-run-shell")
	if decision.Verdict == VerdictDeny {
		return BlockResult{
			Blocked: true,
			Reason:  strings.Join(decision.Reasons, "; "),
			Score:   100,
		}
	} else if decision.Verdict == VerdictPrompt {
		r.Score = 70
		r.Reason = strings.Join(decision.Reasons, "; ")
	}

	// === Live cluster-aware checks ===
	if k != nil {
		appleFoundationHelper := strings.Contains(lower, "xcrun swift") &&
			strings.Contains(lower, "hack/apple-foundation-models.swift") &&
			(strings.Contains(lower, "--prompt") || strings.Contains(lower, "--self-test"))
		clusterAllocatable := k.Snapshot.Summary.TotalAllocatableMB
		if clusterAllocatable <= 0 {
			clusterAllocatable = k.Snapshot.Summary.TotalFreeRAMMB
		}
		if !appleFoundationHelper &&
			(strings.Contains(lower, "model") || strings.Contains(lower, "inference") || strings.Contains(lower, "large")) &&
			clusterAllocatable < 4096 {
			return BlockResult{
				Blocked: true,
				Reason:  fmt.Sprintf("cluster only has %d MB allocatable RAM total — too small for heavy model", clusterAllocatable),
				Score:   87,
			}
		}

		if len(k.Snapshot.Nodes) > 0 {
			best := k.Snapshot.Nodes[0]
			if best.Resources != nil && best.Resources.RAMFreeMB < 1024 && strings.Contains(lower, "large") {
				return BlockResult{
					Blocked: true,
					Reason:  fmt.Sprintf("best node '%s' only has %d MB free RAM", best.Name, best.Resources.RAMFreeMB),
					Score:   78,
				}
			}
		}

		if strings.Contains(lower, "gpu") && !hasGPU(k) {
			return BlockResult{Blocked: true, Reason: "no GPU node available", Score: 75}
		}
	}

	return r
}

func hasGPU(k *knowledge.ClusterKnowledge) bool {
	for _, n := range k.Snapshot.Nodes {
		if n.Resources != nil && len(n.Resources.GPUs) > 0 {
			return true
		}
	}
	return false
}
