package placement

import (
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/workload"
)

// InferRequirements derives TaskRequirements from a task description string.
// It delegates to the internal/workload package for structured profile matching.
func InferRequirements(desc string) models.TaskRequirements {
	return workload.InferRequirements(desc)
}
