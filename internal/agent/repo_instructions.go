package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxRepoInstructionsBytes caps injected AGENTS.md content so a huge file
// cannot blow the system prompt / context budget.
const maxRepoInstructionsBytes = 32 * 1024

// loadRepoInstructions looks for AGENTS.md starting at startDir and walking
// toward the filesystem root. The nearest file wins (project overrides parent).
// Returns path, content, true when a usable file is found.
func loadRepoInstructions(startDir string) (path, content string, ok bool) {
	dir, err := filepath.Abs(startDir)
	if err != nil || dir == "" {
		return "", "", false
	}
	for {
		candidate := filepath.Join(dir, "AGENTS.md")
		data, err := os.ReadFile(candidate)
		if err == nil {
			text := strings.TrimSpace(string(data))
			if text == "" {
				// Empty file is not useful instruction; keep walking.
			} else {
				if len(data) > maxRepoInstructionsBytes {
					text = string(data[:maxRepoInstructionsBytes]) +
						fmt.Sprintf("\n\n… [truncated: AGENTS.md exceeds %d bytes]", maxRepoInstructionsBytes)
				}
				return candidate, text, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", "", false
}

// formatRepoInstructionsBlock builds the system-prompt section for repo rules.
func formatRepoInstructionsBlock(path, content string) string {
	if strings.TrimSpace(content) == "" {
		return ""
	}
	// Prefer a path relative to CWD when possible for shorter prompts.
	display := path
	if rel, err := filepath.Rel(".", path); err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
		display = rel
	}
	var b strings.Builder
	b.WriteString("\n\nRepository instructions (from ")
	b.WriteString(display)
	b.WriteString("):\n")
	b.WriteString("These are operator/project rules for this workspace. Follow them when they apply. ")
	b.WriteString("They do not override the Truth Rule: never invent cluster facts.\n\n")
	b.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}
