package safety

import "testing"

// A redirect to /dev/null must not hide a deny rule. The /dev/null prompt
// rule sits above every deny rule by priority; before this fix the first
// match won, so "rm -rf x > /dev/null" became a prompt that full autonomy
// approves instead of a deny.
func TestNullRedirectDoesNotHideDeny(t *testing.T) {
	e := NewEvaluator(DefaultRuleSet())
	for _, tc := range []struct {
		cmd     string
		verdict Verdict
	}{
		// Denied commands stay denied with a null redirect.
		{"rm -rf /tmp/x", VerdictDeny},
		{"rm -rf /tmp/x > /dev/null", VerdictDeny},
		{"rm -rf /tmp/x > /dev/null 2>&1", VerdictDeny},
		{"sudo rm -rf /var/lib/x > /dev/null 2>&1", VerdictDeny},
		{"sudo systemctl stop example > /dev/null", VerdictDeny},
		{"shred -u secret.txt > /dev/null", VerdictDeny},
		// A raw device write stays denied, even next to a null redirect.
		{"echo hi > /dev/sda", VerdictDeny},
		{"echo hi > /dev/null > /dev/sda", VerdictDeny},
		// Only a target of exactly /dev/null is exempt; /dev/nullb0 is a
		// Linux null block device and a raw device write.
		{"echo hi > /dev/nullb0", VerdictDeny},
		{"echo hi > /dev/null > /dev/nullb0", VerdictDeny},
		// Unchanged by this fix: the "> /dev" rules match only with a space,
		// so no-space redirects keep their program's verdict (a known gap).
		{"echo hi >/dev/nullb0", VerdictAllow},
		{"make build 2>/dev/null", VerdictAllow},
		// A null redirect on its own keeps today's verdict: it asks, not denies.
		{"make build > /dev/null", VerdictPrompt},
		{"ls > /dev/null", VerdictPrompt},
		{"ls -la > /dev/null 2>&1", VerdictPrompt},
	} {
		if d := e.Evaluate(tc.cmd, "agent-run-shell"); d.Verdict != tc.verdict {
			t.Errorf("%q: verdict %s (rule %q); want %s", tc.cmd, d.Verdict, d.MatchedRule, tc.verdict)
		}
	}
}

// Check is what the agent's shell gate uses: a hidden deny scored 70 and
// was not blocked, so full autonomy (threshold 80) ran it.
func TestCheckBlocksDeniedCommandWithNullRedirect(t *testing.T) {
	r := Check(nil, "rm -rf /tmp/x > /dev/null", nil)
	if !r.Blocked || r.Score != 100 {
		t.Fatalf("Check = %+v; want blocked with score 100", r)
	}
	if r := Check(nil, "make build > /dev/null", nil); r.Blocked || r.Score != 70 {
		t.Fatalf("plain null redirect: Check = %+v; want unblocked prompt score 70", r)
	}
}
