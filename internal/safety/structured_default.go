//go:build !safety_scaffolded

package safety

import "time"

// Default stubs for structured safety scaffolding when the safety_scaffolded
// build tag is not present. The stable operator path uses blocker.go (Check)
// instead.

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
	VerdictPrompt Verdict = "prompt"
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
	Programs    []string `yaml:"programs" json:"programs"`
	ArgPatterns []string `yaml:"arg_patterns" json:"arg_patterns"`
	Category    Category `yaml:"category" json:"category"`
	Verdict     Verdict  `yaml:"verdict" json:"verdict"`
	Priority    int      `yaml:"priority" json:"priority"`
	Surfaces    []string `yaml:"surfaces" json:"surfaces,omitempty"`
}

// RuleSet is an ordered collection of safety rules.
type RuleSet struct {
	Rules   []Rule `yaml:"rules" json:"rules"`
	Version string `yaml:"version" json:"version"`
}

// DefaultRuleSet returns an empty rule set stub.
func DefaultRuleSet() RuleSet {
	return RuleSet{Version: "0.0.0", Rules: nil}
}

// Evaluator is a no-op stub when safety_scaffolded is not set.
type Evaluator struct{}

// NewEvaluator creates a no-op safety evaluator stub.
func NewEvaluator(_ RuleSet) *Evaluator {
	return &Evaluator{}
}

// Evaluate returns a permissive decision (always allow).
func (e *Evaluator) Evaluate(rawCmd string, _ string) Decision {
	return Decision{
		Verdict:  VerdictAllow,
		Category: CategorySafe,
		RawCmd:   rawCmd,
		Reasons:  []string{"structured safety evaluator is compile-gated"},
		EvalAt:   time.Now(),
	}
}

// LearnAllow is a no-op stub.
func (e *Evaluator) LearnAllow(_ string) {}

// LearnDeny is a no-op stub.
func (e *Evaluator) LearnDeny(_ string) {}
