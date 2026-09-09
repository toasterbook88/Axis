package console

import (
	"testing"
)

func TestExtractThought(t *testing.T) {
	// No thought tags
	thought, answer := extractThought("regular response")
	if thought != "" || answer != "regular response" {
		t.Fatalf("plain: thought=%q, answer=%q", thought, answer)
	}

	// Clean think block
	raw := "<think>\nchecking cluster health\n</think>\nAll nodes healthy."
	thought, answer = extractThought(raw)
	if thought != "checking cluster health" || answer != "All nodes healthy." {
		t.Fatalf("think block: thought=%q, answer=%q", thought, answer)
	}

	// Unclosed think block
	rawUnclosed := "<think>still thinking"
	thought, answer = extractThought(rawUnclosed)
	if thought != "still thinking" || answer != "" {
		t.Fatalf("unclosed: thought=%q, answer=%q", thought, answer)
	}
}

func TestParseStreamThought(t *testing.T) {
	// Not in thought
	thought, answer, inThought := parseStreamThought("streaming answer")
	if inThought || thought != "" || answer != "streaming answer" {
		t.Fatalf("plain stream: inThought=%t, thought=%q, answer=%q", inThought, thought, answer)
	}

	// Actively in thought
	thought, answer, inThought = parseStreamThought("<think>working on plan")
	if !inThought || thought != "working on plan" || answer != "" {
		t.Fatalf("in thought: inThought=%t, thought=%q, answer=%q", inThought, thought, answer)
	}

	// Thought finished, answer streaming
	thought, answer, inThought = parseStreamThought("<think>done planning</think>Here is the plan")
	if inThought || thought != "done planning" || answer != "Here is the plan" {
		t.Fatalf("completed thought: inThought=%t, thought=%q, answer=%q", inThought, thought, answer)
	}
}
