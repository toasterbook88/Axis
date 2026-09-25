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
// The walk stops at the first repository sentinel (.git or go.mod) so a stray
// AGENTS.md in a parent of the workspace (e.g. the operator's home directory)
// cannot leak into the agent system prompt, and never ascends above the user's
// home directory unless startDir already is home.
// Returns path, content, true when a usable file is found.
func loadRepoInstructions(startDir string) (path, content string, ok bool) {
	dir, err := filepath.Abs(startDir)
	if err != nil || dir == "" {
		return "", "", false
	}
	home, _ := os.UserHomeDir()
	for {
		candidate := filepath.Join(dir, "AGENTS.md")
		data, err := os.ReadFile(candidate)
		if err == nil {
			text := strings.TrimSpace(string(data))
			if text != "" {
				if len(text) > maxRepoInstructionsBytes {
					text = truncateUTF8(text, maxRepoInstructionsBytes) +
						fmt.Sprintf("\n\n… [truncated: AGENTS.md exceeds %d bytes]", maxRepoInstructionsBytes)
				}
				return candidate, text, true
			}
			// Empty (or whitespace-only) file is not useful; keep walking.
		}
		// Repository sentinel: stop before leaving this tree.
		for _, marker := range []string{".git", "go.mod"} {
			if _, statErr := os.Stat(filepath.Join(dir, marker)); statErr == nil {
				return "", "", false
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		if home != "" && dir == home {
			break // never read above the user's home directory
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
