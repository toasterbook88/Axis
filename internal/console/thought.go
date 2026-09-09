package console

import (
	"strings"
)

// extractThought splits text into reasoning thought (from <think>...</think>)
// and final response answer. If no <think> block exists, thought is empty and
// answer is the original text.
func extractThought(s string) (thought, answer string) {
	startIdx := strings.Index(s, "<think>")
	if startIdx == -1 {
		return "", s
	}
	afterStart := s[startIdx+len("<think>"):]
	endRel := strings.Index(afterStart, "</think>")
	if endRel == -1 {
		// Unclosed <think>: treat everything after <think> as thought
		thought = strings.TrimSpace(afterStart)
		answer = strings.TrimSpace(s[:startIdx])
		return thought, answer
	}
	endIdx := startIdx + len("<think>") + endRel
	thought = strings.TrimSpace(s[startIdx+len("<think>") : endIdx])
	answer = strings.TrimSpace(s[:startIdx] + s[endIdx+len("</think>"):])
	return thought, answer
}

// parseStreamThought inspects in-flight stream text.
// inThought is true if an unclosed <think> block is currently being streamed.
func parseStreamThought(s string) (thought, answer string, inThought bool) {
	startIdx := strings.Index(s, "<think>")
	if startIdx == -1 {
		return "", s, false
	}
	afterStart := s[startIdx+len("<think>"):]
	endRel := strings.Index(afterStart, "</think>")
	if endRel == -1 {
		return strings.TrimSpace(afterStart), "", true
	}
	endIdx := startIdx + len("<think>") + endRel
	thought = strings.TrimSpace(s[startIdx+len("<think>") : endIdx])
	answer = strings.TrimSpace(s[:startIdx] + s[endIdx+len("</think>"):])
	return thought, answer, false
}
