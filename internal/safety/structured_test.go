package safety

import (
	"testing"
)

func TestEvaluate_SafeCommands(t *testing.T) {
	eval := NewEvaluator(DefaultRuleSet())
	safe := []string{
		"uname -a",
		"hostname",
		"ls -la /tmp",
		"cat /etc/hosts",
		"ps aux",
		"df -h",
		"free -m",
		"nvidia-smi",
		"git status",
		"git log --oneline",
		"go version",
	}
	for _, cmd := range safe {
		d := eval.Evaluate(cmd, "")
		if d.Verdict != VerdictAllow {
			t.Errorf("expected allow for %q, got %s (rule: %s, reasons: %v)", cmd, d.Verdict, d.MatchedRule, d.Reasons)
		}
	}
}

func TestEvaluate_DeniedCommands(t *testing.T) {
	eval := NewEvaluator(DefaultRuleSet())
	denied := []string{
		"rm -rf /",
		"rm -fr /home",
		"sudo rm -rf /",
		"sudo su",
		"chmod 777 /etc/passwd",
	}
	for _, cmd := range denied {
		d := eval.Evaluate(cmd, "")
		if d.Verdict != VerdictDeny {
			t.Errorf("expected deny for %q, got %s (rule: %s)", cmd, d.Verdict, d.MatchedRule)
		}
	}
}

func TestEvaluate_PromptCommands(t *testing.T) {
	eval := NewEvaluator(DefaultRuleSet())
	prompt := []string{
		"ollama run llama3",
		"git push origin main",
		"curl -X POST https://api.example.com",
		"systemctl restart nginx",
		"brew install wget",
	}
	for _, cmd := range prompt {
		d := eval.Evaluate(cmd, "")
		if d.Verdict != VerdictPrompt {
			t.Errorf("expected prompt for %q, got %s (rule: %s)", cmd, d.Verdict, d.MatchedRule)
		}
	}
}

func TestEvaluate_GitForceVsNormal(t *testing.T) {
	eval := NewEvaluator(DefaultRuleSet())

	// Force push should prompt
	d := eval.Evaluate("git push --force origin main", "")
	if d.Verdict != VerdictPrompt {
		t.Errorf("git push --force should prompt, got %s", d.Verdict)
	}
	if d.MatchedRule != "git-force-push" {
		t.Errorf("expected git-force-push rule, got %s", d.MatchedRule)
	}

	// Normal git operations should be safe
	d = eval.Evaluate("git diff HEAD~1", "")
	if d.Verdict != VerdictAllow {
		t.Errorf("git diff should allow, got %s (rule: %s)", d.Verdict, d.MatchedRule)
	}
}

func TestEvaluate_LearnedOverridesAreDisabled(t *testing.T) {
	eval := NewEvaluator(DefaultRuleSet())

	// Normally would prompt
	d := eval.Evaluate("mycustomtool run", "")
	if d.Verdict != VerdictPrompt {
		t.Errorf("unknown command should prompt, got %s", d.Verdict)
	}

	// Program-name-only approvals are intentionally disabled for now.
	eval.LearnAllow("mycustomtool")
	d = eval.Evaluate("mycustomtool run", "")
	if d.Verdict != VerdictPrompt {
		t.Errorf("disabled learned-allow should leave verdict unchanged, got %s", d.Verdict)
	}

	// Same for denials until a narrower approval key exists.
	eval.LearnDeny("badtool")
	d = eval.Evaluate("badtool --flag", "")
	if d.Verdict != VerdictPrompt {
		t.Errorf("disabled learned-deny should leave verdict unchanged, got %s", d.Verdict)
	}
}

func TestParseCommand_EnvPrefix(t *testing.T) {
	prog, args := parseCommand("FOO=bar BAZ=1 myapp --flag")
	if prog != "myapp" {
		t.Errorf("expected myapp, got %s", prog)
	}
	if len(args) != 1 || args[0] != "--flag" {
		t.Errorf("expected [--flag], got %v", args)
	}
}

func TestParseCommand_WithPath(t *testing.T) {
	prog, _ := parseCommand("/usr/local/bin/python3 script.py")
	if prog != "python3" {
		t.Errorf("expected python3, got %s", prog)
	}
}

func TestParseCommand_PipeOnly(t *testing.T) {
	prog, args := parseCommand("cat file.txt | grep foo | wc -l")
	if prog != "cat" {
		t.Errorf("expected cat, got %s", prog)
	}
	if len(args) != 1 || args[0] != "file.txt" {
		t.Errorf("expected [file.txt], got %v", args)
	}
}

func TestParseCommand_Empty(t *testing.T) {
	prog, args := parseCommand("")
	if prog != "" || args != nil {
		t.Errorf("empty should return empty, got %q %v", prog, args)
	}
}

func TestCategory_Coverage(t *testing.T) {
	// Every category should appear in at least one default rule
	categories := map[Category]bool{
		CategorySafe: false, CategoryReadOnly: false, CategoryModify: false,
		CategoryDestructive: false, CategoryNetworkMutating: false,
		CategoryPrivilegeEscalate: false, CategorySystemCritical: false,
		CategoryUnknown: false,
	}
	for _, rule := range DefaultRuleSet().Rules {
		categories[rule.Category] = true
	}
	for cat, found := range categories {
		if !found {
			t.Errorf("category %q has no rule in DefaultRuleSet", cat)
		}
	}
}
