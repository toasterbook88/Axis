package safety

import "testing"

// Full autonomy asks before commands a rule marked AskAlways flags.
// See docs/superpowers/specs/2026-11-05-full-autonomy-named-ask-design.md.

func TestCheckScoresAskAlwaysPrompts(t *testing.T) {
	for _, tc := range []struct {
		cmd   string
		score int
	}{
		{"git push origin main", ScoreAskAlways},
		{"git push origin main > /dev/null", ScoreAskAlways},
		{"foo --bar", 70},
		{"ls > /dev/null", 70},
		{"rm -rf /tmp/x", 100},
		{"ls", 0},
	} {
		if got := Check(nil, tc.cmd, nil).Score; got != tc.score {
			t.Errorf("%q scored %d, want %d", tc.cmd, got, tc.score)
		}
	}
}

func TestEvaluatePrefersAskAlwaysPrompt(t *testing.T) {
	e := NewEvaluator(DefaultRuleSet())
	for _, tc := range []struct {
		cmd     string
		verdict Verdict
		rule    string
	}{
		// Across segments, an AskAlways prompt beats the unknown fallback.
		{"cd /tmp && git push", VerdictPrompt, "git-push"},
		// Within a segment, a higher-priority prompt does not hide it.
		{"git push origin main > /dev/null", VerdictPrompt, "git-push"},
		{"curl -X POST http://example.com > /dev/null", VerdictPrompt, "curl-post"},
		{"ssh -o StrictHostKeyChecking=no h true > /dev/null", VerdictPrompt, "ssh-host-key-off"},
		// Regression guards.
		{"cd /tmp && foo", VerdictPrompt, "unknown-fallback"},
		{"git push && rm -rf /", VerdictDeny, ""},
		{"git push > /dev/null && rm -rf /", VerdictDeny, ""},
	} {
		d := e.Evaluate(tc.cmd, "agent-run-shell")
		if d.Verdict != tc.verdict || (tc.rule != "" && d.MatchedRule != tc.rule) {
			t.Errorf("%q: %s/%s, want %s/%s", tc.cmd, d.Verdict, d.MatchedRule, tc.verdict, tc.rule)
		}
		if tc.verdict == VerdictPrompt && d.AskAlways != (tc.rule != "unknown-fallback") {
			t.Errorf("%q: AskAlways %v", tc.cmd, d.AskAlways)
		}
	}
}

func TestNewAskAlwaysRules(t *testing.T) {
	e := NewEvaluator(DefaultRuleSet())
	match := map[string]string{
		`ssh -o StrictHostKeyChecking=no h true`:              "ssh-host-key-off",
		`ssh -o "StrictHostKeyChecking no" h true`:            "ssh-host-key-off",
		`ssh -o StrictHostKeyChecking='no' h true`:            "ssh-host-key-off",
		`ssh -o StrictHostKeyChecking="no" h true`:            "ssh-host-key-off",
		`ssh -o 'StrictHostKeyChecking  no' h true`:           "ssh-host-key-off",
		`ssh -o StrictHostKeyChecking = no h true`:            "ssh-host-key-off",
		`ssh -o StrictHostKeyChecking=off h true`:             "ssh-host-key-off",
		`rsync -e "ssh -o UserKnownHostsFile=/dev/null" a b:`: "ssh-host-key-off",
		`cd node_modules/x && git checkout v1`:                "dependency-tree",
		`cd -- node_modules/x`:                                "dependency-tree",
		`cd node_modules && git -C x checkout v1`:             "dependency-tree",
		`git -C node_modules/x checkout v1`:                   "dependency-tree-git",
		`FOO=1 git -C node_modules/x checkout v1`:             "dependency-tree-git",
	}
	for cmd, rule := range match {
		d := e.Evaluate(cmd, "agent-run-shell")
		if d.Verdict != VerdictPrompt || d.MatchedRule != rule || !d.AskAlways {
			t.Errorf("%q: %s/%s ask_always=%v, want prompt/%s", cmd, d.Verdict, d.MatchedRule, d.AskAlways, rule)
		}
	}
}

// Alternate spellings of the specific risks still ask (diff reviews A F1, B
// F1-F3): git global options and env, curl/wget upload options anywhere,
// service options, brew services, backslash escapes.
func TestAskAlwaysRulesMatchAlternateSpellings(t *testing.T) {
	e := NewEvaluator(DefaultRuleSet())
	match := map[string]string{
		`git -C /repo push origin main`:               "git-push",
		`git -C /repo push --force`:                   "git-force-push",
		`git -c user.name=x push`:                     "git-push",
		`git --no-pager push`:                         "git-push",
		`git push --force`:                            "git-force-push",
		`curl -XPOST http://example.com`:              "curl-post",
		`curl --request POST http://example.com`:      "curl-post",
		`curl http://example.com -X POST`:             "curl-post",
		`curl -F f=@x http://example.com`:             "curl-post",
		`curl -T file http://example.com`:             "curl-post",
		`wget --post-data=x http://example.com`:       "curl-post",
		`systemctl restart foo`:                       "service-mgmt",
		`systemctl --user restart foo`:                "service-mgmt",
		`brew services start foo`:                     "service-mgmt",
		`ollama run m`:                                "ollama-run",
		`ssh -o StrictHostKeyChecking\=no h true`:     "ssh-host-key-off",
		`cd node\_modules/x`:                          "dependency-tree",
		`GIT_DIR=node_modules/x/.git git checkout v1`: "dependency-tree-git",
		`GIT_WORK_TREE=node_modules/x git checkout .`: "dependency-tree-git",
		`git --work-tree=node_modules/x checkout v1`:  "dependency-tree-git",
		`git --git-dir node_modules/x/.git log`:       "dependency-tree-git",
		`git -Cnode_modules/x status`:                 "dependency-tree-git",
	}
	for cmd, rule := range match {
		if d := e.Evaluate(cmd, "agent-run-shell"); d.MatchedRule != rule || !d.AskAlways {
			t.Errorf("%q: %s/%s ask_always=%v, want %s", cmd, d.Verdict, d.MatchedRule, d.AskAlways, rule)
		}
	}
}

// A git command that only names node_modules as a pathspec, a message or a
// copy-detection flag keeps its git rule (diff reviews A F1, B F2).
func TestDependencyTreeGitIgnoresPathspecs(t *testing.T) {
	e := NewEvaluator(DefaultRuleSet())
	keep := map[string]string{
		`git -C /repo diff -- . ':!node_modules'`:       "git-safe",
		`git -C /repo status -- node_modules`:           "unknown-fallback", // git-safe matches bare "status" only, as at Base
		`git log -C -- node_modules/x`:                  "git-safe",
		`git log --grep='-C node_modules'`:              "git-safe",
		`git -C ../app commit -m "ignore node_modules"`: "git-modify",
		`curl --request=GET http://example.com`:         "curl-get",
		`wget --method=GET http://example.com`:          "curl-get",
		`systemctl --user status foo`:                   "unknown-fallback",
	}
	for cmd, rule := range keep {
		if d := e.Evaluate(cmd, "agent-run-shell"); d.MatchedRule != rule || d.AskAlways {
			t.Errorf("%q: %s/%s ask_always=%v, want %s", cmd, d.Verdict, d.MatchedRule, d.AskAlways, rule)
		}
	}
}

// Commands near the new rules keep the verdict and rule they have today.
func TestNewRulesLeaveNeighboursAlone(t *testing.T) {
	e := NewEvaluator(DefaultRuleSet())
	keep := map[string]string{
		`ls node_modules/x`:              "list-commands",
		`cat vendor/a.go`:                "list-commands",
		`grep -rn foo node_modules/`:     "unknown-fallback",
		`./node_modules/.bin/eslint .`:   "unknown-fallback",
		`.venv/bin/python -m pytest`:     "unknown-fallback",
		`git add vendor/ go.mod`:         "git-modify",
		`git diff -- . ':!node_modules'`: "git-safe",
		`ls > /dev/null`:                 "legacy-hardblock-dev-null",
	}
	for cmd, rule := range keep {
		if d := e.Evaluate(cmd, "agent-run-shell"); d.MatchedRule != rule || d.AskAlways {
			t.Errorf("%q: %s/%s ask_always=%v, want %s", cmd, d.Verdict, d.MatchedRule, d.AskAlways, rule)
		}
	}
}
