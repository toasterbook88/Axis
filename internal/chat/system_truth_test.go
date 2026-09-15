package chat

import (
	"strings"
	"testing"
)

// The agent's system prompt must teach the agent about its own first-class
// tools (not just user-facing CLI commands) — otherwise small models burn
// turns guessing how to inspect the cluster.
func TestBuildSystemPromptNamesAgentTools(t *testing.T) {
	prompt := BuildSystemPrompt(nil, "")
	for _, want := range []string{
		"axis_place",
		"axis_status",
		"axis_reservations",
		"Placement is advisory",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, prompt)
		}
	}
}

// The prompt must present the true mutation contract: safety checks and
// operator confirmation gate mutating tools. This is the prompt-level
// expression of the Execution Subordination invariant.
func TestBuildSystemPromptStatesConfirmationGates(t *testing.T) {
	prompt := BuildSystemPrompt(nil, "")
	if !strings.Contains(prompt, "safety checks and operator confirmation") {
		t.Fatalf("system prompt missing confirmation-gate guidance:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Never assume approval") {
		t.Fatalf("system prompt missing explicit no-auto-approval rule:\n%s", prompt)
	}
}

// Model lifecycle guidance must match the live command surface: list, start,
// stop, and query exist; the prompt must not invent commands that do not.
func TestBuildSystemPromptModelLifecycleMatchesSurface(t *testing.T) {
	prompt := BuildSystemPrompt(nil, "")
	for _, want := range []string{
		"axis model list",
		"axis model start",
		"axis model query",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("system prompt missing model lifecycle command %q:\n%s", want, prompt)
		}
	}
}
