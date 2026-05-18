// Package safety is EXPERIMENTAL — execution safety blocker with structured analysis.
// It is subordinate to observed state and emits warnings automatically.
//
// The structured safety evaluator in structured.go is compile-gated behind the
// "safety_scaffolded" build tag. It is not included in default builds.
// The stable operator path uses blocker.go (Check) instead.
package safety
