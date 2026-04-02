package placement

import (
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// InferRequirements derives TaskRequirements from a task description string.
// Simple keyword matching — no ML, no parsing.
// Phase-level matches to avoid false positives (e.g. "large codebase" ≠ heavy RAM).
func InferRequirements(desc string) models.TaskRequirements {
	reqs := models.TaskRequirements{
		Description: desc,
	}

	lower := strings.ToLower(desc)

	// Tool inference — order matters (first match wins)
	switch {
	case containsAny(lower, "apple-intelligence", "apple intelligence", "apple foundation models", "apple-foundation-models", "language model session"):
		reqs.RequiredTools = []string{"apple-foundation-models"}
	case containsAny(lower, "llama.cpp", "llama-cli", "llama server", "llama-server"):
		reqs.RequiredTools = []string{"llama-server"}
	case containsAny(lower, "inference", "ollama", "llm", "gpu"):
		reqs.RequiredTools = []string{"ollama"}
	case containsAny(lower, "repo", "analyze", "code", "clone", "commit"):
		reqs.RequiredTools = []string{"git"}
	case containsAny(lower, "build", "compile"):
		reqs.RequiredTools = []string{"go"}
	case containsAny(lower, "docker", "container"):
		reqs.RequiredTools = []string{"docker"}
	}

	// RAM inference
	switch {
	case containsAny(lower, "70b"):
		reqs.MinFreeRAMMB = 12288
	case containsAny(lower, "13b", "heavy"):
		reqs.MinFreeRAMMB = 8192
	case containsAny(lower, "7b"):
		reqs.MinFreeRAMMB = 4096
	case containsAny(lower, "apple-intelligence", "apple intelligence", "apple foundation models", "apple-foundation-models", "language model session"):
		reqs.MinFreeRAMMB = 0
	case containsAny(lower, "llama.cpp", "llama-cli", "llama server", "llama-server"):
		reqs.MinFreeRAMMB = 6144
	case containsAny(lower, "model", "inference", "ollama", "llm", "gpu"):
		reqs.MinFreeRAMMB = 6144
	}

	reqs.ContextWindowTokens = inferContextWindowTokens(lower)
	if reqs.ContextWindowTokens > 0 {
		reqs.PrefersTurboQuant = true
		if minForContext := longContextMinRAM(reqs.ContextWindowTokens); minForContext > reqs.MinFreeRAMMB {
			reqs.MinFreeRAMMB = minForContext
		}
	}

	if containsAny(lower, "mlx", "mlx_lm", "apple silicon", "mac studio", "macbook pro", "mac mini") {
		reqs.PreferredBackends = append(reqs.PreferredBackends, "mlx")
	}
	if containsAny(lower, "apple-intelligence", "apple intelligence", "apple foundation models", "apple-foundation-models", "language model session") {
		reqs.PreferredBackends = append(reqs.PreferredBackends, "apple-foundation-models")
	}
	if containsAny(lower, "llama.cpp", "llama-cli", "llama server", "llama-server") {
		reqs.PreferredBackends = append(reqs.PreferredBackends, "llama.cpp")
	}

	return reqs
}

func containsAny(s string, keywords ...string) bool {
	for _, kw := range keywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

func inferContextWindowTokens(lower string) int {
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

func longContextMinRAM(tokens int) int64 {
	switch {
	case tokens >= 1000000:
		return 12288
	case tokens >= 512000:
		return 8192
	case tokens >= 256000:
		return 6144
	case tokens >= 128000:
		return 4096
	default:
		return 0
	}
}
