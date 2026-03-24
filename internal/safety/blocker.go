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

// Check is the single gatekeeper. Call this first, always.
func Check(k *knowledge.ClusterKnowledge, desc string, isKnownBad func(string) bool) BlockResult {
	lower := strings.ToLower(desc)
	r := BlockResult{Score: 0}

	// === Learned bad commands (fast path - highest priority) ===
	if isKnownBad != nil && isKnownBad(desc) {
		return BlockResult{Blocked: true, Reason: "this exact command failed before", Score: 92}
	}

	// === Hard zero-tolerance patterns (expand forever) ===
	hardBlocks := []struct {
		pattern string
		reason  string
		score   int
	}{
		{"> /dev/null", "redirecting output to null (likely dangerous)", 70},
		{"rm -rf /", "trying to nuke root filesystem", 100},
		{"rm -rf *", "dangerous recursive delete", 95},
		{"sudo rm -rf", "sudo + rm -rf = instant regret", 98},
		{"> /dev", "redirecting to raw device", 92},
		{"dd if", "low-level disk destruction", 90},
		{"while true", "unbounded infinite loop", 85},
		{"fork bomb", "resource exhaustion attack", 88},
		{":(){ :|:& };:}", "classic fork bomb", 100},
		{"70b", "70B model on tiny cluster", 82},
		{"format", "formatting drives without confirmation", 80},
		{"mkfs", "formatting drives without confirmation", 85},
	}

	for _, b := range hardBlocks {
		if strings.Contains(lower, b.pattern) {
			return BlockResult{Blocked: b.score >= 80, Reason: b.reason, Score: b.score}
		}
	}

	// === Explicit safe list (prevents false positives on common safe patterns) ===
	safePatterns := []string{
		"echo ", "printf ", "ls ", "cat ", "git status", "git log", "go version",
		"ollama list", "docker ps", "ps aux", "top", "df -h",
	}
	for _, safe := range safePatterns {
		if strings.Contains(lower, safe) {
			return r
		}
	}

	// === Live cluster-aware checks ===
	if k != nil {
		if (strings.Contains(lower, "model") || strings.Contains(lower, "inference") || strings.Contains(lower, "large")) &&
			k.Snapshot.Summary.TotalFreeRAMMB < 4096 {
			return BlockResult{
				Blocked: true,
				Reason:  fmt.Sprintf("cluster only has %d MB free RAM total — too small for heavy model", k.Snapshot.Summary.TotalFreeRAMMB),
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
