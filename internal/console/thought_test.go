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
	// Reversed tags: </think> precedes <think> (must not panic)
	rawReversed := "Discussion about </think> tag: <think>actual thought</think>result"
	thought, answer = extractThought(rawReversed)
	if thought != "actual thought" || answer != "Discussion about </think> tag: result" {
		t.Fatalf("reversed tags: thought=%q, answer=%q", thought, answer)
	}

	// Multiple reasoning spans (e.g. across tool-calling rounds)
	rawMulti := "<think>Plan tool</think><think>Interpret result</think>Final answer"
	thought, answer = extractThought(rawMulti)
	if thought != "Plan tool\n\nInterpret result" || answer != "Final answer" {
		t.Fatalf("multi-span: thought=%q, answer=%q", thought, answer)
	}

	// Visible answer before/between multiple reasoning spans
	rawInterleaved := "<think>first</think>between<think>second</think>tail"
	thought, answer = extractThought(rawInterleaved)
	if thought != "first\n\nsecond" || answer != "betweentail" {
		t.Fatalf("interleaved: thought=%q, answer=%q", thought, answer)
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

	// Reversed tags: </think> precedes <think> (must not panic)
	thought, answer, inThought = parseStreamThought("Preceding </think> then <think>in progress")
	if !inThought || thought != "in progress" || answer != "" {
		t.Fatalf("reversed tags in stream: inThought=%t, thought=%q, answer=%q", inThought, thought, answer)
	}

	// Multiple spans during live streaming: second unclosed thought stays inThought
	thought, answer, inThought = parseStreamThought("<think>first</think><think>second working")
	if !inThought || thought != "first\n\nsecond working" || answer != "" {
		t.Fatalf("multi stream in thought: inThought=%t, thought=%q, answer=%q", inThought, thought, answer)
	}

	// Multiple completed spans followed by answer
	thought, answer, inThought = parseStreamThought("<think>first</think><think>second</think>Done")
	if inThought || thought != "first\n\nsecond" || answer != "Done" {
		t.Fatalf("multi completed stream: inThought=%t, thought=%q, answer=%q", inThought, thought, answer)
	}
}

func TestThoughtParsingEdgeCases(t *testing.T) {
	// Whitespace-only <think> span yields empty thought while preserving trimmed answer.
	thought, answer := extractThought("<think>   \n\t </think>Answer")
	if thought != "" || answer != "Answer" {
		t.Fatalf("whitespace-only extract: thought=%q, answer=%q", thought, answer)
	}

	// Empty span followed by populated span keeps only non-empty span.
	thought, answer, inThought := parseStreamThought("<think></think>  <think>Real</think>Done")
	if inThought || thought != "Real" || answer != "Done" {
		t.Fatalf("empty+real stream: inThought=%t, thought=%q, answer=%q", inThought, thought, answer)
	}

	// Trailing unclosed span after visible text marks inThought true and suppresses partial answer.
	thought, answer, inThought = parseStreamThought("Mid <think>still thinking")
	if !inThought || thought != "still thinking" || answer != "" {
		t.Fatalf("mid+unclosed stream: inThought=%t, thought=%q, answer=%q", inThought, thought, answer)
	}

	// joinNonEmptyThoughts trims parts and omits empty/blank entries.
	if got := joinNonEmptyThoughts("", "  \n ", " first ", "  ", "\tsecond\t", ""); got != "first\n\nsecond" {
		t.Fatalf("joinNonEmptyThoughts = %q, want %q", got, "first\n\nsecond")
	}
}
