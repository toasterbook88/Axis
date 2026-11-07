package safety

import "testing"

// A shell splits words on tabs as well as spaces, and a case-insensitive
// filesystem (macOS) runs "Rm" as rm. Before this, a tab or an uppercase
// program name made every program and argument rule miss, deny rules
// included (diff review B F5 of the full-autonomy spec).
func TestRulesMatchTabsAndProgramCase(t *testing.T) {
	e := NewEvaluator(DefaultRuleSet())
	for cmd, want := range map[string]Verdict{
		"rm\t-rf\tsomedir":      VerdictDeny,
		"rm -rf\tsomedir":       VerdictDeny,
		"Rm -rf somedir":        VerdictDeny,
		"RM -rf somedir":        VerdictDeny,
		"sudo\tls":              VerdictDeny,
		"git\tpush origin main": VerdictPrompt,
		"Git push origin main":  VerdictPrompt,
		"ls\t-la":               VerdictAllow,
		"echo\thello":           VerdictAllow,
		"rm -rf somedir":        VerdictDeny, // a non-breaking space parses as a separator too: stricter, never looser
	} {
		d := e.Evaluate(cmd, "agent-run-shell")
		if d.Verdict != want {
			t.Errorf("%q: %s/%s, want %s", cmd, d.Verdict, d.MatchedRule, want)
		}
	}
	for _, cmd := range []string{"git\tpush origin main", "Git push origin main"} {
		if d := e.Evaluate(cmd, "agent-run-shell"); d.MatchedRule != "git-push" || !d.AskAlways {
			t.Errorf("%q: %s, want git-push (AskAlways)", cmd, d.MatchedRule)
		}
	}
}
