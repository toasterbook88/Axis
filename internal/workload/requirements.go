package workload

import (
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// InferRequirements derives TaskRequirements from a task description string.
// It uses structured workload profiles to determine hardware and tool needs.
func InferRequirements(desc string) models.TaskRequirements {
	reqs := models.TaskRequirements{
		Description: desc,
	}

	match := Match(desc)
	Apply(match, &reqs)

	lower := strings.ToLower(desc)

	// Context window inference (additive to profile)
	reqs.ContextWindowTokens = InferContextWindowTokens(lower)
	if reqs.ContextWindowTokens > 0 {
		reqs.PrefersTurboQuant = true
		if minForContext := LongContextMinRAM(reqs.ContextWindowTokens); minForContext > reqs.MinFreeRAMMB {
			reqs.MinFreeRAMMB = minForContext
		}
	}

	// Backend inference (preferences based on description keywords)
	InferBackends(desc, &reqs)

	return reqs
}

// Apply populates TaskRequirements based on the matched workload class.
// It aggregates resource signals (RAM, TurboQuant) and required tools/backends
// from all matching profiles found in the description.
func Apply(match models.WorkloadProfileMatch, reqs *models.TaskRequirements) {
	reqs.Workload = match
	lower := strings.ToLower(reqs.Description)
	registry := DefaultRegistry()

	appleMatched := false
	// 1. Aggregate resources and tools from all matching profiles
	for _, p := range registry {
		if containsAny(lower, p.Keywords...) {
			// Resource aggregation
			if p.MinFreeRAMMB == -1 {
				appleMatched = true
			} else if p.MinFreeRAMMB > reqs.MinFreeRAMMB {
				reqs.MinFreeRAMMB = p.MinFreeRAMMB
			}
			if p.PrefersTurboQuant {
				reqs.PrefersTurboQuant = true
			}

			// Tool aggregation
			for _, tool := range p.RequiredTools {
				if !contains(reqs.RequiredTools, tool) {
					reqs.RequiredTools = append(reqs.RequiredTools, tool)
				}
			}

			// Backend aggregation
			for _, backend := range p.PreferredBackends {
				if !contains(reqs.PreferredBackends, backend) {
					reqs.PreferredBackends = append(reqs.PreferredBackends, backend)
				}
			}
		}
	}

	if appleMatched {
		reqs.MinFreeRAMMB = 0
	}
}

func contains(slice []string, s string) bool {
	for _, item := range slice {
		if strings.EqualFold(item, s) {
			return true
		}
	}
	return false
}

// InferBackends adds backend preferences based on keywords in the description.
func InferBackends(desc string, reqs *models.TaskRequirements) {
	lower := strings.ToLower(desc)
	if containsAny(lower, "mlx", "mlx_lm", "apple silicon", "mac studio", "macbook pro", "mac mini") {
		if !contains(reqs.PreferredBackends, "mlx") {
			reqs.PreferredBackends = append(reqs.PreferredBackends, "mlx")
		}
	}
	if containsAny(lower, "apple-intelligence", "apple intelligence", "apple foundation models", "apple-foundation-models", "language model session") {
		if !contains(reqs.PreferredBackends, "apple-foundation-models") {
			reqs.PreferredBackends = append(reqs.PreferredBackends, "apple-foundation-models")
		}
	}
	if containsAny(lower, "llama.cpp", "llama-cli", "llama server", "llama-server") {
		if !contains(reqs.PreferredBackends, "llama.cpp") {
			reqs.PreferredBackends = append(reqs.PreferredBackends, "llama.cpp")
		}
	}
}

// InferContextWindowTokens derives a heuristic token count from description keywords.
func InferContextWindowTokens(lower string) int {
	switch {
	case containsAny(lower, "million-token", "million token", "1m context", "1m tokens"):
		return 1000000
	case containsAny(lower, "512k"):
		return 512000
	case containsAny(lower, "256k", "200k"):
		return 256000
	case containsAny(lower, "128k", "long-context", "long context", "book-length", "book length", "needle-in-a-haystack", "needle in a haystack"):
		return 128000
	default:
		return 0
	}
}

// LongContextMinRAM returns the minimum RAM floor for a given token count.
func LongContextMinRAM(tokens int) int64 {
	switch {
	case tokens >= 1000000:
		return 12288
	case tokens >= 512000:
		return 8192
	case tokens >= 256000:
		return 6144
	case tokens >= 128000:
		return 6144 // Unified with profile floor
	default:
		return 0
	}
}

func containsAny(s string, keywords ...string) bool {
	for _, kw := range keywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}
