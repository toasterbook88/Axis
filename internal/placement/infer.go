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
	case containsAny(lower, "inference", "ollama", "llm", "gpu"):
		reqs.RequiredTool = "ollama"
	case containsAny(lower, "repo", "analyze", "code", "clone", "commit"):
		reqs.RequiredTool = "git"
	case containsAny(lower, "build", "compile"):
		reqs.RequiredTool = "go"
	case containsAny(lower, "docker", "container"):
		reqs.RequiredTool = "docker"
	}

	// RAM inference
	switch {
	case containsAny(lower, "70b", "13b", "heavy"):
		reqs.MinFreeRAMMB = 4096
	case containsAny(lower, "model", "7b", "inference"):
		reqs.MinFreeRAMMB = 1536
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
