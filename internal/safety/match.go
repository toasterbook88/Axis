package safety

import "strings"

// nullRedirect is a redirect that discards output. It never writes a device,
// so it is not evidence for any deny rule ("> /dev" would otherwise match it).
const nullRedirect = "> /dev/null"

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
			raw = strings.ReplaceAll(raw, nullRedirect, "")
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

func containsAnyFold(lower string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.Contains(lower, strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}
