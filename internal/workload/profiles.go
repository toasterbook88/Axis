package workload

import "github.com/toasterbook88/axis/internal/models"

// Profile defines the hardware and tool requirements for a class of work.
type Profile struct {
	Class             models.WorkloadClass
	RequiredTools     []string
	MinFreeRAMMB      int64
	PrefersTurboQuant bool
	PreferredBackends []string
	// PeakRAMHintMB is a heuristic fallback for the empirical placement
	// system. When no runtime observation exists for a workload class,
	// placement uses this as a conservative peak-RAM estimate.
	PeakRAMHintMB int64
}

// DefaultRegistry returns the canonical set of workload profiles.
func DefaultRegistry() []Profile {
	return []Profile{
		{
			Class:             models.ClassAppleIntelligence,
			RequiredTools:     []string{"apple-foundation-models"},
			PreferredBackends: []string{"apple-foundation-models"},
			// PeakRAMHintMB: 0 — system-managed, no hint needed.
		},
		{
			Class:             models.ClassLlamaServer,
			RequiredTools:     []string{"llama-server"},
			PreferredBackends: []string{"llama.cpp"},
			MinFreeRAMMB:      6144,
			PeakRAMHintMB:     8192,
		},
		{
			Class:             models.ClassLongContextInference,
			MinFreeRAMMB:      6144,
			PrefersTurboQuant: true,
			PeakRAMHintMB:     10240,
		},
		{
			Class:         models.ClassLocalLLMInference,
			PeakRAMHintMB: 6144,
		},
		{
			Class:         models.ClassRepoAnalysis,
			RequiredTools: []string{"git"},
			PeakRAMHintMB: 1024,
		},
		{
			Class:         models.ClassGoBuild,
			RequiredTools: []string{"go", "git"},
			PeakRAMHintMB: 2048,
		},
		{
			Class:         models.ClassDockerBuild,
			RequiredTools: []string{"docker"},
			PeakRAMHintMB: 4096,
		},
		{
			Class:         models.ClassIndexingIO,
			MinFreeRAMMB:  2048,
			PeakRAMHintMB: 2048,
		},
		{
			Class:         models.ClassBatchScript,
			PeakRAMHintMB: 512,
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

// PeakRAMHint returns the heuristic peak RAM estimate for a workload class.
// Returns 0 if no profile exists or the class has no hint (e.g. system-managed).
// Exported for use by the placement ranker as a fallback when no empirical
// observation exists.
func PeakRAMHint(class models.WorkloadClass) int64 {
	if p, ok := profileForClass(class); ok {
		return p.PeakRAMHintMB
	}
	return 0
}
