package workload

import "github.com/toasterbook88/axis/internal/models"

// Match returns the best-fitting workload profile for a task description.
func Match(desc string) models.WorkloadProfileMatch {
	return matchFromSignals(analyzeDescription(desc))
}

func matchFromSignals(signals workloadSignals) models.WorkloadProfileMatch {
	var matchedClasses []models.WorkloadClass

	if signals.apple {
		matchedClasses = appendUniqueClass(matchedClasses, models.ClassAppleIntelligence)
	}
	if signals.llama {
		matchedClasses = appendUniqueClass(matchedClasses, models.ClassLlamaServer)
	}
	if signals.localLLM {
		matchedClasses = appendUniqueClass(matchedClasses, models.ClassLocalLLMInference)
	}
	if signals.repo {
		matchedClasses = appendUniqueClass(matchedClasses, models.ClassRepoAnalysis)
	}
	if signals.goBuild {
		matchedClasses = appendUniqueClass(matchedClasses, models.ClassGoBuild)
	}
	if signals.dockerBuild {
		matchedClasses = appendUniqueClass(matchedClasses, models.ClassDockerBuild)
	}
	if signals.indexingIO {
		matchedClasses = appendUniqueClass(matchedClasses, models.ClassIndexingIO)
	}
	if signals.batchScript {
		matchedClasses = appendUniqueClass(matchedClasses, models.ClassBatchScript)
	}
	if signals.longContext {
		matchedClasses = appendUniqueClass(matchedClasses, models.ClassLongContextInference)
	}

	if len(matchedClasses) == 0 {
		return models.WorkloadProfileMatch{
			Class: models.ClassUnknown,
			Notes: []string{"no structured profile matched description"},
		}
	}

	primary := matchedClasses[0]
	if signals.longContext && primary == models.ClassLocalLLMInference {
		primary = models.ClassLongContextInference
	} else if primary == models.ClassUnknown && signals.longContext {
		primary = models.ClassLongContextInference
	}

	var notes []string
	for _, class := range matchedClasses {
		if class == primary {
			continue
		}
		notes = append(notes, "also matched class: "+string(class))
	}

	return models.WorkloadProfileMatch{
		Class: primary,
		Notes: notes,
	}
}

func appendUniqueClass(classes []models.WorkloadClass, class models.WorkloadClass) []models.WorkloadClass {
	for _, existing := range classes {
		if existing == class {
			return classes
		}
	}
	return append(classes, class)
}
