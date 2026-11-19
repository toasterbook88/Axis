package capability

import (
	"strings"
	"testing"
)

func TestKnownChecker(t *testing.T) {
	for name, want := range map[string]bool{
		CheckerFactsHardware:                     true,
		CheckerFactsHardware + NodeCheckerSuffix: true,
		CheckerFactsGPU:                          true,
		CheckerFactsGPU + NodeCheckerSuffix:      true,
		"not-a-real-checker":                     false,
		"axis capability check facts.other":      false,
		CheckerFactsGPU + " --elsewhere":         false,
		"":                                       false,
	} {
		if got := KnownChecker(name); got != want {
			t.Errorf("KnownChecker(%q) = %v, want %v", name, got, want)
		}
	}
}

// An entry appended by hand that claims checker origin under a name no Axis
// checker uses is rejected, so it can neither count nor pass verify.
func TestJournalRejectsAnInventedChecker(t *testing.T) {
	obs := obsOf('p')
	obs.Checker = "not-a-real-checker"
	obs, err := obs.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	obs.Counted = Counted(obs)
	entry := JournalEntry{Seq: 1, Kind: EntryObservation, Observation: &obs, Thresholds: &Thresholds{PromoteAfter: 20, Window: 50, MinRate: 0.98}}
	err = entry.validate()
	if err == nil || !strings.Contains(err.Error(), "not one of Axis's checkers") {
		t.Fatalf("validate() = %v, want an unknown-checker error", err)
	}
	obs.Checker = CheckerFactsHardware + NodeCheckerSuffix
	if err := entry.validate(); err != nil {
		t.Fatalf("a real checker name was rejected: %v", err)
	}
}

func TestRecordCheckRefusesAnInventedChecker(t *testing.T) {
	s := newStore(t)
	obs := checked(OutcomePass)
	obs.Checker = "not-a-real-checker"
	if _, _, _, err := s.RecordCheck(obs, nil); err == nil || !strings.Contains(err.Error(), "not one of Axis's checkers") {
		t.Fatalf("RecordCheck with an invented checker: err=%v", err)
	}
}
