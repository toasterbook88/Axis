package placement

import (
	"regexp"
	"strings"
)

// modelTagRe matches a valid Ollama-style model tag: word characters, dots,
// dashes, slashes, and an optional colon-tag suffix (e.g. "llama3.2:latest",
// "hf.co/org/model:q4_k_m"). The name must start with a word character and
// contain at least one dot, colon, or slash to avoid treating arbitrary words
// as model names.
var modelTagRe = regexp.MustCompile(
	`(?i)\b([\w][\w.\-/]+(?::[^\s]+)?)\b`,
)

// flagPrefixes are explicit CLI flags that introduce a model name argument.
var flagPrefixes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:--model|-m)=(\S+)`),     // --model=name or -m=name
	regexp.MustCompile(`(?i)(?:--model|-m)\s+(\S+)`),   // --model name or -m name
	regexp.MustCompile(`(?i)\bollama\s+run\s+(\S+)`),   // ollama run <model>
	regexp.MustCompile(`(?i)\bollama\s+pull\s+(\S+)`),  // ollama pull <model>
	regexp.MustCompile(`(?i)\bollama\s+serve\s+(\S+)`), // ollama serve <model> (rare but valid)
}

// knownNonModelWords are tokens that look like model names but aren't — common
// flags, path segments, and prose words that would otherwise produce false
// positives from the bare-tag heuristic.
var knownNonModelWords = map[string]struct{}{
	"true": {}, "false": {}, "null": {}, "none": {},
	"cpu": {}, "gpu": {}, "cuda": {}, "metal": {}, "vulkan": {},
	"localhost": {}, "127.0.0.1": {},
}

// ExtractModelName attempts to extract an inference model name from a task
// description or command string. Returns the first match found, or "" if no
// model name is identifiable.
//
// Priority order:
//  1. Explicit flag forms: --model=X, -m=X, --model X, -m X
//  2. Ollama subcommand forms: ollama run X, ollama pull X
//  3. Bare model-tag heuristic: first token matching word:tag form
//
// The function is intentionally conservative — it returns "" rather than
// guessing when the description is ambiguous prose without a recognisable
// model reference.
func ExtractModelName(description string) string {
	if strings.TrimSpace(description) == "" {
		return ""
	}

	// Priority 1 & 2: explicit flag / subcommand forms.
	for _, re := range flagPrefixes {
		if m := re.FindStringSubmatch(description); len(m) >= 2 {
			candidate := strings.TrimSpace(m[1])
			if isPlausibleModelName(candidate) {
				return candidate
			}
		}
	}

	// Priority 3: bare model-tag heuristic — must contain a colon (tag
	// separator) or slash (namespace separator) to distinguish from generic
	// words, and must not be a known non-model token.
	for _, m := range modelTagRe.FindAllStringSubmatch(description, -1) {
		candidate := strings.TrimSpace(m[1])
		if !isPlausibleModelName(candidate) {
			continue
		}
		// Require colon or slash to avoid treating every hyphenated word as a
		// model name. Flags like "--model X" are already handled above.
		if strings.ContainsAny(candidate, ":/") {
			return candidate
		}
	}

	return ""
}

// isPlausibleModelName returns false for tokens that are clearly not model
// names: empty strings, pure numbers, known non-model words, and file paths.
func isPlausibleModelName(s string) bool {
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	if _, bad := knownNonModelWords[lower]; bad {
		return false
	}
	// Reject bare file paths (absolute or relative).
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return false
	}
	// Reject multi-segment filesystem paths even without a leading slash
	// (e.g. "home/user/models/qwen.gguf" from "--model /home/user/models/qwen.gguf").
	// Allow up to 2 slashes for namespaced model refs like "hf.co/org/model:tag".
	if strings.Count(s, "/") >= 3 {
		return false
	}
	// Must contain at least one letter.
	hasLetter := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			hasLetter = true
			break
		}
	}
	return hasLetter
}
