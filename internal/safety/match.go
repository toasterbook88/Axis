package safety

import (
	"regexp"
	"strings"
)

// nullRedirect matches a redirect whose whole target is /dev/null. It never
// writes a device, so it is not evidence for any deny rule ("> /dev" would
// otherwise match it). The target must end there: "/dev/nullb0" is a block
// device and stays a raw device write.
var nullRedirect = regexp.MustCompile(`> /dev/null(\s|$)`)

// ruleMatches reports whether rule applies to one command segment. lowerCmd
// is the lowercased raw segment. A deny rule's raw patterns are matched with
// null redirects removed: "echo x > /dev/null" is not a raw device write,
// while "echo x > /dev/null > /dev/sda" still is.
func ruleMatches(rule Rule, surface, program, joinedArgs, lowerCmd string) bool {
	if len(rule.Surfaces) > 0 && !containsString(rule.Surfaces, surface) {
		return false
	}
	if len(rule.RawPatterns) > 0 {
		raw := lowerCmd
		if rule.Verdict == VerdictDeny {
			raw = nullRedirect.ReplaceAllString(raw, " ")
		}
		if rule.AskAlways {
			raw = optionText(raw)
		}
		if !containsAnyFold(raw, rule.RawPatterns) {
			return false
		}
	}
	if len(rule.Programs) > 0 && !matchesProgram(program, rule.Programs) {
		return false
	}
	if len(rule.ArgPatterns) > 0 {
		for _, pattern := range rule.ArgPatterns {
			if globMatch(joinedArgs, pattern) {
				return true
			}
		}
		return false
	}
	return true
}

// firstDeny returns the first deny rule in rules that matches the segment.
// A higher-priority allow or prompt rule must not hide a deny: before this
// check, "rm -rf x > /dev/null" matched the /dev/null prompt rule first and
// was never denied.
func firstDeny(rules []Rule, surface, program, joinedArgs, lowerCmd string) (Rule, bool) {
	for _, rule := range rules {
		if rule.Verdict == VerdictDeny && ruleMatches(rule, surface, program, joinedArgs, lowerCmd) {
			return rule, true
		}
	}
	return Rule{}, false
}

// firstAskAlways returns the first AskAlways prompt rule in rules that
// matches the segment. A higher-priority prompt that flags no specific risk
// (the null-redirect rule) must not hide one: "git push > /dev/null" would
// otherwise auto-run under full autonomy.
func firstAskAlways(rules []Rule, surface, program, joinedArgs, lowerCmd string) (Rule, bool) {
	for _, rule := range rules {
		if rule.Verdict == VerdictPrompt && rule.AskAlways && ruleMatches(rule, surface, program, joinedArgs, lowerCmd) {
			return rule, true
		}
	}
	return Rule{}, false
}

var (
	optionSpaces = regexp.MustCompile(`\s+`)
	optionEquals = regexp.MustCompile(`\s*=\s*`)
)

// optionText normalizes a lowercased segment for AskAlways raw patterns:
// quotes removed, whitespace runs collapsed, and spaces around "=" dropped,
// so `StrictHostKeyChecking='no'`, `"StrictHostKeyChecking  no"` and
// `StrictHostKeyChecking = no` read the same as the plain spellings.
func optionText(lower string) string {
	s := strings.NewReplacer(`'`, "", `"`, "").Replace(lower)
	s = optionSpaces.ReplaceAllString(s, " ")
	return optionEquals.ReplaceAllString(s, "=")
}

// wildcardMatch reports whether s matches pattern, where "*" matches any run
// of characters (including spaces and "/") and everything else is literal.
func wildcardMatch(s, pattern string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return s == pattern
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

func containsAnyFold(lower string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.Contains(lower, strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}
