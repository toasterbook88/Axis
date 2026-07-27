// Package safety is EXPERIMENTAL — execution safety blocker with structured analysis.
// It is subordinate to observed state and emits warnings automatically.
//
// The structured evaluator is included in default builds. blocker.go uses it
// through Check, and guarded execution evaluates commands through the same
// default rule set. Learned approvals remain disabled.
package safety
