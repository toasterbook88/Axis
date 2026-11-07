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
	} {
		d := e.Evaluate(cmd, "agent-run-shell")
		if d.Verdict != want {
			t.Errorf("%q: %s/%s, want %s", cmd, d.Verdict, d.MatchedRule, want)
		}
	}
}

// The program-specific dispatch (git global options, git directory variables,
// service-manager options) must see an uppercase program name as the program
// it names, or a specific risk falls through to the unknown rule.
func TestUppercaseProgramKeepsSpecificRules(t *testing.T) {
	e := NewEvaluator(DefaultRuleSet())
	for cmd, want := range map[string]struct {
		rule string
		ask  bool
	}{
		"git\tpush origin main":                         {"git-push", true},
		"Git push origin main":                          {"git-push", true},
		"git -C /repo push":                             {"git-push", true},
		"Git -C /repo push":                             {"git-push", true},
		"GIT_WORK_TREE=node_modules/pkg Git checkout .": {"dependency-tree-git", true},
		"GIT_DIR=node_modules/pkg/.git Git status":      {"dependency-tree-git", true},
		"Systemctl --user restart nginx":                {"service-mgmt", true},
		"Service nginx start":                           {"service-mgmt", true},
		// A non-breaking space stays inside the -C value, exactly as the
		// shell passes it, so push remains the subcommand and still asks.
		"git -C /tmp/repo\u00a0log push origin main": {"git-push", true},
	} {
		d := e.Evaluate(cmd, "agent-run-shell")
		if d.MatchedRule != want.rule || d.AskAlways != want.ask {
			t.Errorf("%q: %s (ask=%v), want %s (ask=%v)", cmd, d.MatchedRule, d.AskAlways, want.rule, want.ask)
		}
		if d.Verdict != VerdictPrompt {
			t.Errorf("%q: verdict %s, want prompt", cmd, d.Verdict)
		}
	}
}

// A non-breaking space is an ordinary character in a shell, so this names one
// unknown program rather than "rm -rf": the shell finds no such binary. The
// parser must keep it as one word and fall to the unknown fallback instead of
// recognising a command the shell would not run.
func TestNonBreakingSpaceStaysInWord(t *testing.T) {
	d := NewEvaluator(DefaultRuleSet()).Evaluate("rm\u00a0-rf\u00a0somedir", "agent-run-shell")
	if d.Program != "rm\u00a0-rf\u00a0somedir" || d.MatchedRule != "unknown-fallback" {
		t.Fatalf("program %q rule %q, want one NBSP word matched by unknown-fallback", d.Program, d.MatchedRule)
	}
}
