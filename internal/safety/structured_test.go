package safety

import (
	"testing"
)

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

func TestEvaluate_AllowsSafePipeline(t *testing.T) {
	eval := NewEvaluator(DefaultRuleSet())
	d := eval.Evaluate("cat /etc/hosts | head -5", "agent-run-shell")
	if d.Verdict != VerdictAllow {
		t.Fatalf("expected safe pipeline to be allowed, got %s: %v", d.Verdict, d.Reasons)
	}
}

func TestEvaluate_AllowsSafeTrailingShellSeparators(t *testing.T) {
	eval := NewEvaluator(DefaultRuleSet())
	for _, cmd := range []string{"hostname;", "hostname\n", "hostname &"} {
		d := eval.Evaluate(cmd, "agent-run-shell")
		if d.Verdict != VerdictAllow {
			t.Errorf("expected allow for %q, got %s: %v", cmd, d.Verdict, d.Reasons)
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

func TestGlobMatch_PreservesPrefixFallbackForJoinedArgs(t *testing.T) {
	tests := []struct {
		pattern string
		value   string
		want    bool
	}{
		{pattern: "push*", value: "push --force origin main", want: true},
		{pattern: "show *", value: "show llama3", want: true},
		{pattern: "status", value: "status", want: true},
		{pattern: "status", value: "status --short", want: false},
	}

	for _, tt := range tests {
		if got := globMatch(tt.value, tt.pattern); got != tt.want {
			t.Fatalf("globMatch(%q, %q) = %v, want %v", tt.value, tt.pattern, got, tt.want)
		}
	}
}
