package workload

import (
	"context"

	"github.com/toasterbook88/axis/internal/models"
)

// Classifier is the interface for semantic workload classification.
//
// It is satisfied by llmrouter.Engine (via its ClassifyWorkload method) and by
// test doubles. A nil Classifier passed inside InferRequirementsOptions causes
// InferRequirements to use the legacy string-matcher (analyzeDescription)
// instead.
//
// Implementations must:
//   - be safe for concurrent use
//   - enforce their own latency budget (the llmrouter.Engine default is 150 ms)
//   - never contact cloud endpoints (local inference only, per AXIS doctrine)
type Classifier interface {
	// ClassifyWorkload maps a task description to a WorkloadProfileMatch.
	// extraContext carries supplemental information (e.g. available node names,
	// current cluster RAM state) that the classifier may use to improve
	// accuracy. It may be empty.
	//
	// On error the caller falls back to the legacy reflex path; errors should
	// only be returned for permanent failures — transient latency overruns are
	// expected to be absorbed by the implementation.
	ClassifyWorkload(ctx context.Context, prompt, extraContext string) (models.WorkloadProfileMatch, error)
}

// InferRequirementsOptions controls optional behaviour of InferRequirements.
// All fields are optional; the zero value produces legacy behaviour.
type InferRequirementsOptions struct {
	// Match, if non-nil, is used as the primary classification result.
	// This takes precedence over Classifier and avoids redundant inference calls
	// when the caller already has a semantic match.
	Match *models.WorkloadProfileMatch

	// Classifier, if non-nil, is invoked to determine the primary WorkloadClass.
	// A nil Classifier is equivalent to calling InferRequirements with no options.
	Classifier Classifier

	// ExtraContext is forwarded to Classifier alongside the raw prompt.
	// Useful for injecting live cluster context (node names, free RAM, etc.)
	// without modifying the task description itself.
	// Ignored when Classifier is nil.
	ExtraContext string
}
