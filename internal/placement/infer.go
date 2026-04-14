package placement

import (
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/workload"
)

// InferRequirements derives TaskRequirements from a task description string.
// It delegates to the internal/workload package for structured profile matching.
//
// An optional InferRequirementsOptions may be provided to inject a semantic
// Classifier (e.g. llmrouter.Engine). All existing call-sites that pass no
// options continue to use the legacy string-matcher path unchanged.
func InferRequirements(desc string, opts ...workload.InferRequirementsOptions) models.TaskRequirements {
	return workload.InferRequirements(desc, opts...)
}
