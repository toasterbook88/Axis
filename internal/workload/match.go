package workload

import (
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// Match returns the best-fitting workload profile for a task description.
func Match(desc string) models.WorkloadProfileMatch {
	lower := strings.ToLower(desc)
	registry := DefaultRegistry()

	var matchedClasses []models.WorkloadClass
	var notes []string

	for _, p := range registry {
		if containsAny(lower, p.Keywords...) {
			matchedClasses = append(matchedClasses, p.Class)
		}
	}

	if len(matchedClasses) == 0 {
		return models.WorkloadProfileMatch{
			Class: models.ClassUnknown,
			Notes: []string{"no structured profile matched description"},
		}
	}

	// Selection and precedence logic
	primary := matchedClasses[0]
	for _, c := range matchedClasses {
		// LongContext overrides LocalLLM
		if c == models.ClassLongContextInference && primary == models.ClassLocalLLMInference {
			primary = c
			notes = append(notes, "upgraded to long-context-inference")
		}
		// AppleIntelligence / LlamaServer are very specific
		if c == models.ClassAppleIntelligence || c == models.ClassLlamaServer {
			primary = c
		}
	}

	if len(matchedClasses) > 1 {
		for _, c := range matchedClasses {
			if c != primary {
				notes = append(notes, "also matched class: "+string(c))
			}
		}
	}

	return models.WorkloadProfileMatch{
		Class: primary,
		Notes: notes,
	}
}

