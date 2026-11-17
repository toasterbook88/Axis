package capability

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

// obsOf builds one observation:
//
//	p  a pass from Axis's checker with a live probe
//	x  the same pass reported from outside Axis
//	f  a failure from the checker
//	c  a grounded critical failure
//	u  a critical failure with only a peer's say-so
//	i  inconclusive
func obsOf(kind byte) Observation {
	o := Observation{Capability: "facts.hardware", Actor: DefaultActor, Origin: OriginChecker, Outcome: OutcomePass,
		At: t0, Checker: "test", Evidence: Evidence{Kind: EvidenceLiveProbe, Ref: "uname(2)"}}
	switch kind {
	case 'x':
		o.Origin = OriginExternal
	case 'f':
		o.Outcome = OutcomeFail
	case 'c':
		o.Outcome, o.Severity = OutcomeFail, SeverityCritical
	case 'u':
		o.Outcome, o.Severity, o.Origin = OutcomeFail, SeverityCritical, OriginExternal
		o.Evidence = Evidence{Kind: EvidencePeer, Ref: "an agent said so"}
	case 'i':
		o.Outcome = OutcomeInconclusive
	}
	return o
}

func th(after, window int, rate float64) Thresholds {
	return Thresholds{PromoteAfter: after, Window: window, MinRate: rate}
}

func p(n int) string { return strings.Repeat("p", n) }

// feed applies the outcomes in order, numbering them after *seq and spacing
// them an hour apart.
func feed(rec Record, t Thresholds, seq *uint64, outcomes string) (Record, []Transition) {
	var all []Transition
	for i := 0; i < len(outcomes); i++ {
		*seq++
		o := obsOf(outcomes[i])
		o.At = t0.Add(time.Duration(*seq) * time.Hour)
		var tr *Transition
		rec, tr = Apply(rec, t, o, *seq)
		if tr != nil {
			all = append(all, *tr)
		}
	}
	return rec, all
}

func TestCheckerPassesPromoteOneLevelAndStopAtTheCeiling(t *testing.T) {
	var seq uint64
	rec, trs := feed(NewRecord("facts.hardware", DefaultActor, t0), th(10, 50, 0.9), &seq, p(30))
	if rec.Level != LevelAdvisory {
		t.Fatalf("level = %s, want advisory: the default ceiling must hold", rec.Level)
	}
	if len(trs) != 1 || trs[0].Kind != TransitionPromoted || trs[0].From != LevelUnproven || trs[0].To != LevelAdvisory || trs[0].Seq != 10 {
		t.Fatalf("transitions = %+v, want one promotion to advisory at entry 10", trs)
	}
	if trs[0].Thresholds == nil || *trs[0].Thresholds != (Thresholds{10, 50, 0.9, 60}) {
		t.Fatalf("the promotion must record its thresholds, got %+v", trs[0].Thresholds)
	}
	if rec.Totals != (Totals{Pass: 30}) {
		t.Fatalf("totals = %+v, want 30 passes", rec.Totals)
	}
}

func TestEachLevelIsEarnedSeparately(t *testing.T) {
	var seq uint64
	rec := NewRecord("facts.hardware", DefaultActor, t0)
	rec.Ceiling = LevelTrusted
	var all []Transition
	step := func(outcomes string, want Level) {
		t.Helper()
		var trs []Transition
		rec, trs = feed(rec, th(10, 50, 0.9), &seq, outcomes)
		all = append(all, trs...)
		if rec.Level != want {
			t.Fatalf("after %d entries: level %s, want %s", seq, rec.Level, want)
		}
	}
	step(p(10), LevelAdvisory)
	if rec.Streak != 0 {
		t.Fatalf("a promotion must reset the streak, got %d", rec.Streak)
	}
	step(p(9), LevelAdvisory)
	step(p(1), LevelSupervised)
	step(p(10), LevelTrusted)
	want := []struct {
		to  Level
		seq uint64
	}{{LevelAdvisory, 10}, {LevelSupervised, 20}, {LevelTrusted, 30}}
	if len(all) != len(want) {
		t.Fatalf("transitions = %+v", all)
	}
	for i, w := range want {
		if all[i].To != w.to || all[i].Seq != w.seq {
			t.Fatalf("transition %d = %s at entry %d, want %s at %d", i, all[i].To, all[i].Seq, w.to, w.seq)
		}
	}
}

func TestPassesReportedFromOutsideNeverCount(t *testing.T) {
	var seq uint64
	rec, trs := feed(NewRecord("facts.hardware", DefaultActor, t0), th(10, 50, 0.9), &seq, strings.Repeat("x", 30))
	if rec.Level != LevelUnproven || len(trs) != 0 || rec.Streak != 0 || rec.Recent != "" {
		t.Fatalf("outside passes moved the record: %+v %+v", rec, trs)
	}
	if rec.Totals != (Totals{Inconclusive: 30}) {
		t.Fatalf("totals = %+v, want 30 inconclusive", rec.Totals)
	}
}

func TestCheckerPassesNeedGroundedEvidence(t *testing.T) {
	for _, ev := range []Evidence{
		{Kind: EvidencePeer, Ref: "another agent said so"},
		{Kind: EvidenceNone},
		{Kind: EvidenceLiveProbe},                // names nothing probed
		{Kind: EvidenceFile, Ref: "findings.md"}, // no content hash
	} {
		rec := NewRecord("facts.hardware", DefaultActor, t0)
		for i := 0; i < 30; i++ {
			o := obsOf('p')
			o.Evidence = ev
			rec, _ = Apply(rec, th(10, 50, 0.9), o, uint64(i+1))
		}
		if rec.Level != LevelUnproven || rec.Totals != (Totals{Inconclusive: 30}) {
			t.Fatalf("evidence %+v counted: %+v", ev, rec)
		}
	}
}

func TestCheckerPassesWithAHashedFileCount(t *testing.T) {
	rec := NewRecord("facts.hardware", DefaultActor, t0)
	var trs []*Transition
	for i := 0; i < 10; i++ {
		o := obsOf('p')
		o.Evidence = Evidence{Kind: EvidenceFile, Ref: "findings/run.md", SHA256: "ff58ba6a88f1"}
		o.At = t0.Add(time.Duration(i) * time.Hour)
		var tr *Transition
		rec, tr = Apply(rec, th(10, 50, 0.9), o, uint64(i+1))
		if tr != nil {
			trs = append(trs, tr)
		}
	}
	if rec.Level != LevelAdvisory || len(trs) != 1 || rec.Totals.Pass != 10 {
		t.Fatalf("hashed-file passes: %+v %+v", rec, trs)
	}
}

func TestEveryFailureCounts(t *testing.T) {
	var seq uint64
	rec := NewRecord("facts.hardware", DefaultActor, t0)
	rec.Level, rec.Ceiling, rec.Streak = LevelTrusted, LevelTrusted, 7

	outside := obsOf('f')
	outside.Origin, outside.Evidence = OriginExternal, Evidence{Kind: EvidencePeer, Ref: "an agent saw it fail"}
	seq++
	rec, tr := Apply(rec, th(10, 50, 0.9), outside, seq)
	if rec.Level != LevelSupervised || rec.Streak != 0 || tr == nil || tr.Kind != TransitionDemoted {
		t.Fatalf("an outside, ungrounded failure must demote one level: %+v %+v", rec, tr)
	}

	rec, trs := feed(rec, th(10, 50, 0.9), &seq, "u")
	if rec.Level != LevelAdvisory || len(trs) != 1 || !strings.Contains(trs[0].Reason, "from outside Axis, so one level") {
		t.Fatalf("a critical failure from outside must demote one level, not to unproven: %+v %+v", rec, trs)
	}
	hashed := obsOf('u')
	hashed.Evidence = Evidence{Kind: EvidenceFile, Ref: "findings/x.md", SHA256: "made-up"}
	rec.Level = LevelAdvisory
	seq++
	if r, tr := Apply(rec, th(10, 50, 0.9), hashed, seq); r.Level != LevelUnproven || tr == nil || tr.From != LevelAdvisory || strings.Contains(tr.Reason, "grounded critical") {
		t.Fatalf("a made-up hash must not make an outside failure critical: %+v %+v", r, tr)
	}
	rec.Level = LevelSupervised
	rec, trs = feed(rec, th(10, 50, 0.9), &seq, "f")
	if rec.Level != LevelAdvisory || len(trs) != 1 {
		t.Fatalf("a checker failure demotes one level: %+v %+v", rec, trs)
	}
	rec, trs = feed(rec, th(10, 50, 0.9), &seq, "c")
	if rec.Level != LevelUnproven || len(trs) != 1 || !strings.Contains(trs[0].Reason, "grounded critical") {
		t.Fatalf("a grounded critical failure must demote to unproven: %+v %+v", rec, trs)
	}
	rec, trs = feed(rec, th(10, 50, 0.9), &seq, "f")
	if len(trs) != 0 || rec.Totals.Fail != 5 {
		t.Fatalf("a failure at unproven is counted and reports no transition: %+v %+v", rec, trs)
	}
}

func TestTheRateGateHoldsAfterTheStreakIsMet(t *testing.T) {
	var seq uint64
	rec, trs := feed(NewRecord("facts.hardware", DefaultActor, t0), th(10, 20, 0.95), &seq, "f"+p(10))
	if len(trs) != 0 || rec.Streak != 10 {
		t.Fatalf("streak met at 10/11 = 91%%, below 95%%: must not promote: %+v %+v", rec, trs)
	}
	rec, trs = feed(rec, th(10, 20, 0.95), &seq, p(8))
	if len(trs) != 0 {
		t.Fatalf("18/19 = 94.7%% must not promote: %+v", trs)
	}
	_, trs = feed(rec, th(10, 20, 0.95), &seq, p(1))
	if len(trs) != 1 || trs[0].Seq != 20 {
		t.Fatalf("19/20 = 95%% must promote at entry 20: %+v", trs)
	}
}

func TestTheStreakGateHoldsWhileTheRateIsMet(t *testing.T) {
	var seq uint64
	rec, trs := feed(NewRecord("facts.hardware", DefaultActor, t0), th(10, 50, 0.9), &seq, p(9))
	if len(trs) != 0 {
		t.Fatalf("9 passes at 100%% must not promote: %+v", trs)
	}
	if _, trs = feed(rec, th(10, 50, 0.9), &seq, p(1)); len(trs) != 1 {
		t.Fatalf("the 10th pass must promote: %+v", trs)
	}
}

func TestThresholdsBelowTheFloorsAreRaised(t *testing.T) {
	var seq uint64
	rec, trs := feed(NewRecord("facts.hardware", DefaultActor, t0), th(1, 1, 0.01), &seq, p(9))
	if len(trs) != 0 || rec.Level != LevelUnproven {
		t.Fatalf("a one-pass policy must not promote below the floor: %+v %+v", rec, trs)
	}
	_, trs = feed(rec, th(1, 1, 0.01), &seq, p(1))
	if len(trs) != 1 || *trs[0].Thresholds != (Thresholds{MinPromoteAfter, MinPromoteAfter, MinRateFloor, 60}) {
		t.Fatalf("promotion at the floor with floored thresholds recorded, got %+v", trs)
	}
}

func TestAStreakMustCoverTheMinimumSpan(t *testing.T) {
	rec := NewRecord("facts.hardware", DefaultActor, t0)
	var trs []*Transition
	for i := 0; i < 30; i++ { // thirty passes a minute apart: a burst
		o := obsOf('p')
		o.At = t0.Add(time.Duration(i) * time.Minute)
		var tr *Transition
		if rec, tr = Apply(rec, Thresholds{10, 50, 0.9, 120}, o, uint64(i+1)); tr != nil {
			trs = append(trs, tr)
		}
	}
	if len(trs) != 0 || rec.Streak != 30 || !rec.StreakSince.Equal(t0) {
		t.Fatalf("a burst inside two hours must not promote: %+v %+v", rec, trs)
	}
	o := obsOf('p')
	o.At = t0.Add(2 * time.Hour)
	if _, tr := Apply(rec, Thresholds{10, 50, 0.9, 120}, o, 31); tr == nil || !strings.Contains(tr.Reason, "over 2h0m0s") {
		t.Fatalf("the pass that completes the span must promote: %+v", tr)
	}
	if _, tr := Apply(NewRecord("facts.hardware", DefaultActor, t0), Thresholds{10, 50, 0.9, 1}, o, 1); tr != nil {
		t.Fatal("a one-pass record must not promote")
	}
}

func TestAnOpenFailureBlocksPromotionUntilThatSubjectPasses(t *testing.T) {
	rec := NewRecord("facts.gpu", DefaultActor, t0)
	at := func(i int, kind byte, subject string) Observation {
		o := obsOf(kind)
		o.Capability, o.Subject, o.At = "facts.gpu", subject, t0.Add(time.Duration(i)*time.Hour)
		return o
	}
	rec, _ = Apply(rec, th(10, 50, 0.9), at(0, 'f', "node:bad"), 1)
	if len(rec.OpenFailures) != 1 || rec.OpenFailures[0] != "node:bad" {
		t.Fatalf("a checker failure must open its subject: %+v", rec.OpenFailures)
	}
	outside := at(1, 'f', "node:elsewhere")
	outside.Origin = OriginExternal
	rec, _ = Apply(rec, th(10, 50, 0.9), outside, 2)
	if len(rec.OpenFailures) != 1 {
		t.Fatalf("an outside failure must not open a subject: %+v", rec.OpenFailures)
	}
	for i := 2; i < 60; i++ { // only the good machine is checked
		var tr *Transition
		if rec, tr = Apply(rec, th(10, 50, 0.9), at(i, 'p', "node:good"), uint64(i+1)); tr != nil {
			t.Fatalf("checking only the good machine promoted at entry %d: %+v", i+1, tr)
		}
	}
	if _, tr := Apply(rec, th(10, 50, 0.9), at(60, 'i', "node:bad"), 61); tr != nil {
		t.Fatal("an inconclusive check of the failing machine must not close its failure")
	}
	rec, tr := Apply(rec, th(10, 50, 0.9), at(61, 'p', "node:bad"), 62)
	if tr == nil || tr.To != LevelAdvisory || len(rec.OpenFailures) != 0 {
		t.Fatalf("a pass on the failing machine must close it and allow the promotion: %+v %+v", rec, tr)
	}
}

func TestRecentIsTrimmedToTheWindow(t *testing.T) {
	var seq uint64
	rec, _ := feed(NewRecord("facts.hardware", DefaultActor, t0), th(10, 12, 0.9), &seq, "f"+p(12))
	if rec.Recent != p(12) {
		t.Fatalf("recent = %q, want the newest 12 outcomes", rec.Recent)
	}
}

func TestCeilingChanges(t *testing.T) {
	rec := NewRecord("facts.hardware", DefaultActor, t0)
	rec.Level, rec.Streak = LevelAdvisory, 4
	rec, trs := SetCeiling(rec, LevelSupervised, t0, "operator", 7)
	if rec.Level != LevelAdvisory || rec.Streak != 4 || len(trs) != 1 || trs[0].Kind != TransitionCeiling || trs[0].Seq != 7 {
		t.Fatalf("raising the ceiling must keep the level and streak: %+v %+v", rec, trs)
	}
	if _, trs = SetCeiling(rec, LevelSupervised, t0, "operator", 8); len(trs) != 0 {
		t.Fatalf("the same ceiling must report nothing, got %+v", trs)
	}
	rec, trs = SetCeiling(rec, LevelUnproven, t0, "operator", 9)
	if rec.Level != LevelUnproven || rec.Ceiling != LevelUnproven || rec.Streak != 0 {
		t.Fatalf("lowering the ceiling below the level must lower the level: %+v", rec)
	}
	if len(trs) != 2 || trs[0].Kind != TransitionCeiling || trs[1].Kind != TransitionDemoted || rec.LastTransition.Kind != TransitionDemoted {
		t.Fatalf("transitions = %+v, want a ceiling change then a demotion", trs)
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]Level{
		"unproven": LevelUnproven, " Advisory ": LevelAdvisory, "SUPERVISED": LevelSupervised, "Trusted": LevelTrusted,
	} {
		if got, err := ParseLevel(in); err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "root", "trusted!"} {
		if _, err := ParseLevel(bad); err == nil {
			t.Errorf("ParseLevel(%q) must fail", bad)
		}
	}
}
