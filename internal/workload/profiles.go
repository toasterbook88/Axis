package workload

import "github.com/toasterbook88/axis/internal/models"

// Profile defines the hardware and tool requirements for a class of work.
type Profile struct {
	Class             models.WorkloadClass
	RequiredTools     []string
	MinFreeRAMMB      int64
	PrefersTurboQuant bool
	PreferredBackends []string
}

// DefaultRegistry returns the canonical set of workload profiles.
func DefaultRegistry() []Profile {
	return []Profile{
		{
			Class:             models.ClassAppleIntelligence,
			RequiredTools:     []string{"apple-foundation-models"},
			PreferredBackends: []string{"apple-foundation-models"},
		},
		{
			Class:             models.ClassLlamaServer,
			RequiredTools:     []string{"llama-server"},
			PreferredBackends: []string{"llama.cpp"},
			MinFreeRAMMB:      6144,
		},
		{
			Class:             models.ClassLongContextInference,
			MinFreeRAMMB:      6144,
			PrefersTurboQuant: true,
		},
		{
			Class: models.ClassLocalLLMInference,
		},
		{
			Class:         models.ClassRepoAnalysis,
			RequiredTools: []string{"git"},
		},
		{
			Class:         models.ClassGoBuild,
			RequiredTools: []string{"go", "git"},
		},
		{
			Class:         models.ClassDockerBuild,
			RequiredTools: []string{"docker"},
		},
		{
			Class:        models.ClassIndexingIO,
			MinFreeRAMMB: 2048,
		},
		{
			Class: models.ClassBatchScript,
		},
	}
}

func profileForClass(class models.WorkloadClass) (Profile, bool) {
	for _, profile := range DefaultRegistry() {
		if profile.Class == class {
			return profile, true
		}
	}
	return Profile{}, false
}
