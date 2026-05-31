package safety

import "testing"

// Package-level variables to prevent the compiler from optimizing away
// the benchmarked function calls (a common Go benchmark gotcha).
var (
	benchDecision  Decision
	benchEvaluator *Evaluator
)

// Benchmarks for the structured safety evaluator.
//
// These provide a stable baseline for measuring the cost of safety checks,
// which run on the hot path for task execution and agent tool use.
// They cover common cases: safe commands, early-reject dangerous commands,
// argument-pattern matching, complex commands, and evaluator construction.
func BenchmarkEvaluate_SafeCommand(b *testing.B) {
	eval := NewEvaluator(DefaultRuleSet())
	b.ReportAllocs()
	b.ResetTimer()
	var d Decision
	for i := 0; i < b.N; i++ {
		d = eval.Evaluate("ls -la /tmp", "agent-run-shell")
	}
	benchDecision = d
}

func BenchmarkEvaluate_DangerousCommand(b *testing.B) {
	eval := NewEvaluator(DefaultRuleSet())
	b.ReportAllocs()
	b.ResetTimer()
	var d Decision
	for i := 0; i < b.N; i++ {
		d = eval.Evaluate("rm -rf /", "agent-run-shell")
	}
	benchDecision = d
}

func BenchmarkEvaluate_OllamaRun(b *testing.B) {
	eval := NewEvaluator(DefaultRuleSet())
	b.ReportAllocs()
	b.ResetTimer()
	var d Decision
	for i := 0; i < b.N; i++ {
		d = eval.Evaluate("ollama run llama3", "agent-run-shell")
	}
	benchDecision = d
}

func BenchmarkEvaluate_ComplexGit(b *testing.B) {
	eval := NewEvaluator(DefaultRuleSet())
	b.ReportAllocs()
	b.ResetTimer()
	var d Decision
	for i := 0; i < b.N; i++ {
		d = eval.Evaluate("git push --force-with-lease origin main", "agent-run-shell")
	}
	benchDecision = d
}

func BenchmarkNewEvaluator_DefaultRuleSet(b *testing.B) {
	var e *Evaluator
	for i := 0; i < b.N; i++ {
		e = NewEvaluator(DefaultRuleSet())
	}
	benchEvaluator = e
}
