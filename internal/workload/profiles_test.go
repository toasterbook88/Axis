package workload

import (
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestDefaultRegistryCoversAllClasses(t *testing.T) {
	// Every non-unknown WorkloadClass should have a corresponding profile.
	expected := []models.WorkloadClass{
		models.ClassAppleIntelligence,
		models.ClassLlamaServer,
		models.ClassLongContextInference,
		models.ClassLocalLLMInference,
		models.ClassRepoAnalysis,
		models.ClassGoBuild,
		models.ClassDockerBuild,
		models.ClassIndexingIO,
		models.ClassBatchScript,
	}

	registry := DefaultRegistry()
	covered := make(map[models.WorkloadClass]bool, len(registry))
	for _, p := range registry {
		covered[p.Class] = true
	}

	for _, class := range expected {
		if !covered[class] {
			t.Errorf("DefaultRegistry missing profile for class %q", class)
		}
	}
}

func TestProfileForClassLookup(t *testing.T) {
	classes := []models.WorkloadClass{
		models.ClassAppleIntelligence,
		models.ClassLlamaServer,
		models.ClassLongContextInference,
		models.ClassLocalLLMInference,
		models.ClassRepoAnalysis,
		models.ClassGoBuild,
		models.ClassDockerBuild,
		models.ClassIndexingIO,
		models.ClassBatchScript,
	}

	for _, class := range classes {
		p, ok := profileForClass(class)
		if !ok {
			t.Errorf("profileForClass(%q) returned false", class)
			continue
		}
		if p.Class != class {
			t.Errorf("profileForClass(%q).Class = %q", class, p.Class)
		}
	}
}

func TestProfileForClassUnknown(t *testing.T) {
	_, ok := profileForClass(models.ClassUnknown)
	if ok {
		t.Error("expected profileForClass(ClassUnknown) to return false")
	}
}

func TestProfileForClassNonexistent(t *testing.T) {
	_, ok := profileForClass("totally-fake-class")
	if ok {
		t.Error("expected profileForClass for nonexistent class to return false")
	}
}

func TestProfilesHaveConsistentRequirements(t *testing.T) {
	for _, p := range DefaultRegistry() {
		if p.MinFreeRAMMB < 0 {
			t.Errorf("profile %q has negative MinFreeRAMMB: %d", p.Class, p.MinFreeRAMMB)
		}

		if p.PeakRAMHintMB < 0 {
			t.Errorf("profile %q has negative PeakRAMHintMB: %d", p.Class, p.PeakRAMHintMB)
		}

		// PeakRAMHint should be >= MinFreeRAM when both are set
		if p.PeakRAMHintMB > 0 && p.MinFreeRAMMB > 0 && p.PeakRAMHintMB < p.MinFreeRAMMB {
			t.Errorf("profile %q has PeakRAMHintMB (%d) < MinFreeRAMMB (%d)",
				p.Class, p.PeakRAMHintMB, p.MinFreeRAMMB)
		}
	}
}

func TestNoNilBackendsInProfiles(t *testing.T) {
	for _, p := range DefaultRegistry() {
		// RequiredTools and PreferredBackends should be nil or valid; never contain empty strings
		for _, tool := range p.RequiredTools {
			if tool == "" {
				t.Errorf("profile %q has empty string in RequiredTools", p.Class)
			}
		}
		for _, backend := range p.PreferredBackends {
			if backend == "" {
				t.Errorf("profile %q has empty string in PreferredBackends", p.Class)
			}
		}
	}
}
