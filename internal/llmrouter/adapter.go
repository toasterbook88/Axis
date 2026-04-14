package llmrouter

import (
	"context"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/workload"
)

// Compile-time assertion: *Engine must satisfy workload.Classifier.
// This will produce a build error if the method signature drifts.
var _ workload.Classifier = (*Engine)(nil)

// ClassifyWorkload implements workload.Classifier.
//
// It wraps Engine.Classify and converts the result to a
// models.WorkloadProfileMatch so that the workload package can use it without
// importing llmrouter internals (which would create an import cycle).
//
// Because Engine.Classify absorbs all failures internally — falling back to
// the legacy reflex path on timeout, unreachable endpoint, or parse error —
// this method will only ever return a non-nil error if the supplied context is
// already cancelled before the call begins. In all other cases it returns a
// valid WorkloadProfileMatch and a nil error.
func (e *Engine) ClassifyWorkload(ctx context.Context, prompt, extraContext string) (models.WorkloadProfileMatch, error) {
	if err := ctx.Err(); err != nil {
		// Fast-path: caller's context is already done. Propagate so that
		// workload.resolveWorkloadMatch can fall back to the legacy path.
		return models.WorkloadProfileMatch{}, err
	}

	class, sig, _ := e.Classify(ctx, prompt, extraContext)
	// _ is intentionally discarded: Classify never returns a non-nil error
	// (all failure modes are absorbed and produce SourceReflex internally).

	return models.WorkloadProfileMatch{
		Class: class,
		Notes: append([]string(nil), sig.Notes...),
	}, nil
}
