// Package safety extension: structured.go adds structured rule-evaluation
// scaffolding alongside the existing substring-based blocker.
//
// The current safety system uses substring matching which risks:
//   - Over-blocking: "rm -rf" blocks "echo 'rm -rf' in quotes"
//   - Under-blocking: "r""m -rf /" bypasses substring check
//   - No context awareness: same rules for local vs remote, admin vs user
//
// This groundwork provides:
//   - Parsed command analysis (split into program + args)
//   - Category-based rules (destructive, network, privilege-escalation, etc.)
//   - Surface-aware rule matching
//   - Explicit safety reasoning per decision
//
// Learned approvals are intentionally disabled in this branch because
// program-name-only overrides are too broad to be treated as operator-safe.
package safety

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Category classifies the risk level of a command.
type Category string

const (
	CategorySafe              Category = "safe"
	CategoryReadOnly          Category = "read-only"
	CategoryModify            Category = "modify"
	CategoryDestructive       Category = "destructive"
	CategoryNetworkMutating   Category = "network-mutating"
	CategoryPrivilegeEscalate Category = "privilege-escalate"
	CategorySystemCritical    Category = "system-critical"
	CategoryUnknown           Category = "unknown"
)

// Verdict is the outcome of a safety evaluation.
type Verdict string

const (
	VerdictAllow  Verdict = "allow"
	VerdictDeny   Verdict = "deny"
	VerdictPrompt Verdict = "prompt" // ask the operator
)

// Decision captures the full reasoning for a safety evaluation.
type Decision struct {
	Verdict     Verdict   `json:"verdict"`
	Category    Category  `json:"category"`
	Program     string    `json:"program"`
	Args        []string  `json:"args"`
	RawCmd      string    `json:"raw_cmd"`
	Reasons     []string  `json:"reasons"`
	MatchedRule string    `json:"matched_rule,omitempty"`
	EvalAt      time.Time `json:"evaluated_at"`
}

// Rule defines a single safety rule.
type Rule struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description" json:"description"`
	Programs    []string `yaml:"programs" json:"programs"`         // glob patterns for program name
	ArgPatterns []string `yaml:"arg_patterns" json:"arg_patterns"` // glob patterns matched against joined args
	Category    Category `yaml:"category" json:"category"`
	Verdict     Verdict  `yaml:"verdict" json:"verdict"`
	Priority    int      `yaml:"priority" json:"priority"`           // higher = evaluated first
	Surfaces    []string `yaml:"surfaces" json:"surfaces,omitempty"` // restrict to surfaces (empty = all)
}

// RuleSet is an ordered collection of safety rules.
type RuleSet struct {
	Rules   []Rule `yaml:"rules" json:"rules"`
	Version string `yaml:"version" json:"version"`
}

// DefaultRuleSet returns the built-in safety rules.
func DefaultRuleSet() RuleSet {
	return RuleSet{
		Version: "1.0.0",
		Rules: []Rule{
			// Always-safe: read-only info commands
			{Name: "info-commands", Programs: []string{"uname", "hostname", "whoami", "id", "date", "uptime", "df", "free", "lsb_release", "sw_vers", "sysctl"}, Category: CategorySafe, Verdict: VerdictAllow, Priority: 100},
			{Name: "list-commands", Programs: []string{"ls", "find", "which", "where", "type", "file", "wc", "head", "tail", "cat", "less", "more"}, Category: CategoryReadOnly, Verdict: VerdictAllow, Priority: 100},
			{Name: "process-info", Programs: []string{"ps", "top", "htop", "pgrep", "lsof"}, Category: CategoryReadOnly, Verdict: VerdictAllow, Priority: 100},
			{Name: "network-info", Programs: []string{"ifconfig", "ip", "netstat", "ss", "dig", "nslookup", "ping", "traceroute"}, Category: CategoryReadOnly, Verdict: VerdictAllow, Priority: 90},
			{Name: "gpu-info", Programs: []string{"nvidia-smi", "rocm-smi", "metal", "system_profiler"}, Category: CategoryReadOnly, Verdict: VerdictAllow, Priority: 90},
			{Name: "version-commands", Programs: []string{"git", "go", "python*", "node", "npm", "cargo", "rustc", "swift", "xcodebuild", "ollama"}, ArgPatterns: []string{"version", "--version", "-v", "-V"}, Category: CategorySafe, Verdict: VerdictAllow, Priority: 95},

			// AI/ML runtime commands
			{Name: "ollama-safe", Programs: []string{"ollama"}, ArgPatterns: []string{"list", "show *", "ps"}, Category: CategoryReadOnly, Verdict: VerdictAllow, Priority: 85},
			{Name: "ollama-run", Programs: []string{"ollama"}, ArgPatterns: []string{"run *", "pull *", "serve"}, Category: CategoryModify, Verdict: VerdictPrompt, Priority: 80},

			// Build/dev commands — generally safe
			{Name: "build-commands", Programs: []string{"make", "go", "cargo", "npm", "yarn", "swift", "xcodebuild"}, ArgPatterns: []string{"build*", "test*", "check*", "fmt*", "lint*", "vet*"}, Category: CategoryModify, Verdict: VerdictAllow, Priority: 80},

			// Destructive commands — always block without prompt
			{Name: "recursive-delete", Programs: []string{"rm"}, ArgPatterns: []string{"-rf *", "-fr *", "-rf/*"}, Category: CategoryDestructive, Verdict: VerdictDeny, Priority: 200},
			{Name: "format-disk", Programs: []string{"mkfs*", "dd", "fdisk", "diskutil"}, Category: CategorySystemCritical, Verdict: VerdictDeny, Priority: 200},
			{Name: "wipe-commands", Programs: []string{"shred", "srm", "wipefs"}, Category: CategoryDestructive, Verdict: VerdictDeny, Priority: 200},

			// Privilege escalation — deny by default
			{Name: "privilege-escalate", Programs: []string{"sudo", "su", "doas", "pkexec"}, Category: CategoryPrivilegeEscalate, Verdict: VerdictDeny, Priority: 190},
			{Name: "chmod-dangerous", Programs: []string{"chmod"}, ArgPatterns: []string{"777 *", "+s *", "u+s *"}, Category: CategoryPrivilegeEscalate, Verdict: VerdictDeny, Priority: 185},

			// Network-mutating — prompt
			{Name: "curl-post", Programs: []string{"curl", "wget"}, ArgPatterns: []string{"-X POST*", "-X PUT*", "-X DELETE*", "--data*", "-d *"}, Category: CategoryNetworkMutating, Verdict: VerdictPrompt, Priority: 70},
			{Name: "curl-get", Programs: []string{"curl", "wget"}, Category: CategoryReadOnly, Verdict: VerdictAllow, Priority: 60},

			// Service management — prompt
			{Name: "service-mgmt", Programs: []string{"systemctl", "launchctl", "service", "brew"}, ArgPatterns: []string{"start*", "stop*", "restart*", "enable*", "disable*", "install*", "uninstall*"}, Category: CategoryModify, Verdict: VerdictPrompt, Priority: 75},

			// Git — mostly safe except force push
			{Name: "git-safe", Programs: []string{"git"}, ArgPatterns: []string{"status", "log*", "diff*", "branch*", "show*", "remote*", "fetch*", "stash*"}, Category: CategoryReadOnly, Verdict: VerdictAllow, Priority: 85},
			{Name: "git-modify", Programs: []string{"git"}, ArgPatterns: []string{"add*", "commit*", "pull*", "merge*", "rebase*", "checkout*", "switch*"}, Category: CategoryModify, Verdict: VerdictAllow, Priority: 80},
			{Name: "git-force-push", Programs: []string{"git"}, ArgPatterns: []string{"push --force*", "push -f*"}, Category: CategoryDestructive, Verdict: VerdictPrompt, Priority: 150},
			{Name: "git-push", Programs: []string{"git"}, ArgPatterns: []string{"push*"}, Category: CategoryModify, Verdict: VerdictPrompt, Priority: 75},

			// Catch-all for unknown commands
			{Name: "unknown-fallback", Programs: []string{"*"}, Category: CategoryUnknown, Verdict: VerdictPrompt, Priority: 0},
		},
	}
}

// Evaluator applies static rules to commands. Program-wide learned overrides are
// intentionally disabled until they can be scoped more narrowly than a binary
// name alone.
type Evaluator struct {
	rules []Rule
}

// NewEvaluator creates a safety evaluator from a rule set.
func NewEvaluator(rs RuleSet) *Evaluator {
	// Sort rules by priority (highest first) so specific high-risk matches win.
	sorted := make([]Rule, len(rs.Rules))
	copy(sorted, rs.Rules)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Priority > sorted[j].Priority
	})
	return &Evaluator{
		rules: sorted,
	}
}

// Evaluate assesses a command string and returns a safety decision.
func (e *Evaluator) Evaluate(rawCmd string, surface string) Decision {
	program, args := parseCommand(rawCmd)
	d := Decision{
		Program: program,
		Args:    args,
		RawCmd:  rawCmd,
		EvalAt:  time.Now(),
	}

	joinedArgs := strings.Join(args, " ")

	for _, rule := range e.rules {
		if !matchesProgram(program, rule.Programs) {
			continue
		}

		// If rule has surface restriction, check it
		if len(rule.Surfaces) > 0 && !containsString(rule.Surfaces, surface) {
			continue
		}

		// If rule has arg patterns, at least one must match
		if len(rule.ArgPatterns) > 0 {
			matched := false
			for _, pattern := range rule.ArgPatterns {
				if globMatch(joinedArgs, pattern) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}

		d.Verdict = rule.Verdict
		d.Category = rule.Category
		d.MatchedRule = rule.Name
		d.Reasons = append(d.Reasons, fmt.Sprintf("matched rule %q (priority %d): %s", rule.Name, rule.Priority, rule.Description))
		return d
	}

	// No rule matched — default deny
	d.Verdict = VerdictDeny
	d.Category = CategoryUnknown
	d.Reasons = append(d.Reasons, "no matching safety rule found")
	return d
}

// LearnAllow is intentionally disabled until approvals can be scoped more
// narrowly than a program name alone.
func (e *Evaluator) LearnAllow(_ string) {
}

// LearnDeny is intentionally disabled until approvals can be scoped more
// narrowly than a program name alone.
func (e *Evaluator) LearnDeny(_ string) {
}

// --- Parsing helpers ---

// parseCommand splits a shell command into program and arguments.
// Handles common shell patterns:
//   - Pipes: only evaluates the first command
//   - Env prefixes: FOO=bar cmd → cmd
//   - Path prefixes: /usr/bin/cmd → cmd
func parseCommand(raw string) (string, []string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}

	// Strip leading env assignments (KEY=val)
	parts := shellSplit(raw)
	for len(parts) > 0 && strings.Contains(parts[0], "=") && !strings.HasPrefix(parts[0], "-") {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return "", nil
	}

	// Extract program name (strip path)
	program := filepath.Base(parts[0])
	args := parts[1:]

	// If piped, only evaluate first command
	for i, arg := range args {
		if arg == "|" || arg == "||" || arg == "&&" || arg == ";" {
			args = args[:i]
			break
		}
	}

	return program, args
}

// shellSplit does basic shell-style word splitting.
// Handles single and double quotes but not full POSIX parsing.
func shellSplit(s string) []string {
	var parts []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escaped := false

	for _, r := range s {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		switch {
		case r == '\\' && !inSingle:
			escaped = true
		case r == '\'' && !inDouble:
			inSingle = !inSingle
		case r == '"' && !inSingle:
			inDouble = !inDouble
		case r == ' ' && !inSingle && !inDouble:
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// matchesProgram checks if a program name matches any glob pattern.
func matchesProgram(program string, patterns []string) bool {
	for _, pattern := range patterns {
		if matched, _ := filepath.Match(pattern, program); matched {
			return true
		}
	}
	return false
}

// globMatch does a simple glob match on a string.
func globMatch(s, pattern string) bool {
	matched, _ := filepath.Match(pattern, s)
	if matched {
		return true
	}
	// Joined argument strings can contain spaces, so keep an explicit prefix
	// fallback for the common "foo*" rule style used by the evaluator.
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(s, prefix)
	}
	return false
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
