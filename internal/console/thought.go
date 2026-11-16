package console

import (
	"strings"
)

// extractThought splits text into reasoning thought (from <think>...</think>
// spans) and final response answer. If no <think> block exists, thought is empty
// and answer is the original text. Multiple spans across tool rounds are joined
// in order while preserving visible text outside tags.
func extractThought(s string) (thought, answer string) {
	thoughts, ans, inThought, found := splitThoughtSpans(s)
	if !found && !inThought {
		return "", s
	}
	return strings.Join(thoughts, "\n\n"), strings.TrimSpace(ans)
}

// parseStreamThought inspects in-flight stream text across one or more
// <think>...</think> spans. inThought is true if a final unclosed <think>
// block is currently being streamed.
func parseStreamThought(s string) (thought, answer string, inThought bool) {
	thoughts, ans, inThought, found := splitThoughtSpans(s)
	if !found && !inThought {
		return "", s, false
	}
	if inThought {
		return strings.Join(thoughts, "\n\n"), "", true
	}
	return strings.Join(thoughts, "\n\n"), strings.TrimSpace(ans), false
}

func splitThoughtSpans(s string) (thoughts []string, answer string, inThought, found bool) {
	var answerBuf strings.Builder
	rest := s
	for {
		startIdx := strings.Index(rest, "<think>")
		if startIdx == -1 {
			answerBuf.WriteString(rest)
			break
		}
		found = true
		answerBuf.WriteString(rest[:startIdx])
		afterStart := rest[startIdx+len("<think>"):]
		endRel := strings.Index(afterStart, "</think>")
		if endRel == -1 {
			if t := strings.TrimSpace(afterStart); t != "" {
				thoughts = append(thoughts, t)
			}
			inThought = true
			break
		}
		if t := strings.TrimSpace(afterStart[:endRel]); t != "" {
			thoughts = append(thoughts, t)
		}
		rest = afterStart[endRel+len("</think>"):]
	}
	return thoughts, answerBuf.String(), inThought, found
}
