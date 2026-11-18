package capability

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var fastPolicy = Policies{"facts.hardware": {ID: "facts.hardware", PromoteAfter: 10, Window: 20, MinRate: 0.9}}

func newStore(t *testing.T) *Store {
	t.Helper()
	s := Open(filepath.Join(t.TempDir(), "capability"))
	clock := t0
	s.now = func() time.Time { clock = clock.Add(time.Hour); return clock }
	return s
}

func checked(outcome Outcome) Observation {
	return Observation{Capability: "facts.hardware", Outcome: outcome, Checker: "test checker",
		Evidence: Evidence{Kind: EvidenceLiveProbe, Ref: "uname(2)"}}
}

func passes(t *testing.T, s *Store, n int) Record {
	t.Helper()
	var rec Record
	for i := 0; i < n; i++ {
		var err error
		if rec, _, err = s.RecordCheck(checked(OutcomePass), fastPolicy); err != nil {
			t.Fatalf("RecordCheck %d: %v", i+1, err)
		}
	}
	return rec
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func entries(t *testing.T, s *Store) []JournalEntry {
	t.Helper()
	var out []JournalEntry
	for _, line := range readLines(t, s.path(journalFile)) {
		var e JournalEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("journal line %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

func TestCheckerObservationsAreJournaledChainedAndCounted(t *testing.T) {
	s := newStore(t)
	rec := passes(t, s, 12)
	if rec.Level != LevelAdvisory || rec.Totals.Pass != 12 || rec.Streak != 2 {
		t.Fatalf("record = %+v, want advisory with 12 passes and a streak of 2", rec)
	}
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, s.path(journalFile))
	info, _ := os.Stat(s.path(journalFile))
	if st.NextSeq != 13 || st.Head != lineHash([]byte(lines[11])) || st.JournalBytes != info.Size() {
		t.Fatalf("state position: next %d head %s bytes %d (journal %d bytes)", st.NextSeq, st.Head, st.JournalBytes, info.Size())
	}
	for i, e := range entries(t, s) {
		if e.Seq != uint64(i+1) || e.Kind != EntryObservation || e.Observation.Origin != OriginChecker || e.Observation.Counted != OutcomePass {
			t.Fatalf("entry %d = %+v", i+1, e)
		}
		if *e.Thresholds != (Thresholds{10, 20, 0.9, 360}) {
			t.Fatalf("entry %d thresholds = %+v", i+1, e.Thresholds)
		}
		wantPrev := ""
		if i > 0 {
			wantPrev = lineHash([]byte(lines[i-1]))
		}
		if e.Prev != wantPrev {
			t.Fatalf("entry %d does not chain to the line before it", i+1)
		}
	}
	trs := readLines(t, s.path(transitionsFile))
	if len(trs) != 1 || !strings.Contains(trs[0], `"promoted"`) || !strings.Contains(trs[0], `"seq":10`) {
		t.Fatalf("transitions = %v, want one promotion at entry 10", trs)
	}
	for _, name := range []string{stateFile, journalFile, transitionsFile} {
		if info, err := os.Stat(s.path(name)); err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s must be private: %v %v", name, info, err)
		}
	}
}

func TestPassesReportedFromOutsideAreKeptButNeverCount(t *testing.T) {
	s := newStore(t)
	var rec Record
	for i := 0; i < 12; i++ {
		o := checked(OutcomePass)
		o.Origin = OriginChecker // the caller cannot choose its origin
		var err error
		if rec, _, err = s.Record(o, fastPolicy); err != nil {
			t.Fatal(err)
		}
	}
	if rec.Level != LevelUnproven || rec.Totals != (Totals{Inconclusive: 12}) {
		t.Fatalf("record = %+v", rec)
	}
	for _, e := range entries(t, s) {
		if e.Observation.Origin != OriginExternal || e.Observation.Counted != OutcomeInconclusive {
			t.Fatalf("entry %d = %+v", e.Seq, e.Observation)
		}
	}
}

func TestFailuresReportedFromOutsideCount(t *testing.T) {
	s := newStore(t)
	passes(t, s, 10)
	rec, tr, err := s.Record(Observation{Capability: "facts.hardware", Outcome: OutcomeFail,
		Evidence: Evidence{Kind: EvidencePeer, Ref: "an agent saw it fail"}}, fastPolicy)
	if err != nil || rec.Level != LevelUnproven || tr == nil || tr.Kind != TransitionDemoted {
		t.Fatalf("record = %+v, transition %+v, %v", rec, tr, err)
	}
}

func TestTheStoreStampsTheTime(t *testing.T) {
	s := newStore(t)
	o := checked(OutcomePass)
	o.At = t0.Add(-365 * 24 * time.Hour) // a caller trying to backdate
	rec, _, err := s.RecordCheck(o, fastPolicy)
	if err != nil || !rec.LastObserved.Equal(t0.Add(time.Hour)) {
		t.Fatalf("the store must stamp the time, got %v (%v)", rec.LastObserved, err)
	}
}

func TestOnlyTheAxisActorHoldsARecord(t *testing.T) {
	s := newStore(t)
	o := checked(OutcomePass)
	o.Actor = "agent:codex"
	if _, _, err := s.RecordCheck(o, fastPolicy); err == nil {
		t.Fatal("an agent actor must be refused until identities are verified")
	}
	if _, err := os.Stat(s.path(journalFile)); err == nil {
		t.Fatal("a refused observation reached the journal")
	}
	o = checked(OutcomePass)
	o.Checker = ""
	if _, _, err := s.RecordCheck(o, fastPolicy); err == nil {
		t.Fatal("a checker observation must name its checker")
	}
}

func TestSetCeilingIsJournaledAndKeepsTheStreak(t *testing.T) {
	s := newStore(t)
	passes(t, s, 5)
	rec, trs, err := s.SetCeiling("facts.hardware", LevelSupervised, "")
	if err != nil || rec.Ceiling != LevelSupervised || rec.Streak != 5 || len(trs) != 1 {
		t.Fatalf("SetCeiling = %+v %+v %v", rec, trs, err)
	}
	if last := entries(t, s)[5]; last.Kind != EntryCeiling || last.Ceiling.Reason != "set by the operator" {
		t.Fatalf("the ceiling change must be journaled: %+v", last)
	}
	if rec = passes(t, s, 5); rec.Level != LevelAdvisory {
		t.Fatalf("the streak kept across the ceiling change must promote: %+v", rec)
	}
	if rec = passes(t, s, 10); rec.Level != LevelSupervised {
		t.Fatalf("level = %s, want supervised under the raised ceiling", rec.Level)
	}
	before := len(entries(t, s))
	if _, trs, err = s.SetCeiling("facts.hardware", LevelSupervised, ""); err != nil || len(trs) != 0 || len(entries(t, s)) != before {
		t.Fatalf("setting the same ceiling must write nothing: %+v %v", trs, err)
	}
	if got := len(readLines(t, s.path(transitionsFile))); got != 3 {
		t.Fatalf("transitions has %d lines, want the ceiling change and two promotions", got)
	}
	if _, _, err := s.SetCeiling("facts.hardware", Level("root"), ""); err == nil {
		t.Fatal("an unknown ceiling must be refused")
	}
}

// appendWithoutState writes the next journal entry and stops, as a write would
// if it were interrupted before saving the state.
func appendWithoutState(t *testing.T, s *Store) {
	t.Helper()
	st, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	o := checked(OutcomePass)
	o.Origin, o.At = OriginChecker, t0
	if o, err = o.Normalize(); err != nil {
		t.Fatal(err)
	}
	o.Counted = Counted(o)
	th := fastPolicy.For(o.Capability).Thresholds()
	line, err := json.Marshal(JournalEntry{Seq: st.NextSeq, Prev: st.Head, Kind: EntryObservation, Observation: &o, Thresholds: &th})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.appendJournal(line); err != nil {
		t.Fatal(err)
	}
}

func TestTheStateCatchesUpAfterAnInterruptedWrite(t *testing.T) {
	s := newStore(t)
	passes(t, s, 2)
	appendWithoutState(t, s)
	v, err := s.Verify()
	if err != nil || !v.OK || v.Entries != 3 || v.Counted != 2 || len(v.Notes) != 1 || !strings.Contains(v.Notes[0], "last 1") {
		t.Fatalf("Verify = %+v, %v; want OK with one entry not yet counted", v, err)
	}
	rec := passes(t, s, 1)
	if rec.Totals.Pass != 4 {
		t.Fatalf("passes = %d, want 4: the interrupted entry must be counted once", rec.Totals.Pass)
	}
	for i, e := range entries(t, s) {
		if e.Seq != uint64(i+1) {
			t.Fatalf("entry %d has seq %d: a retry must not reuse a sequence number", i+1, e.Seq)
		}
	}
	if v, err = s.Verify(); err != nil || !v.OK || len(v.Notes) != 0 || v.Entries != 4 {
		t.Fatalf("Verify after catching up = %+v, %v", v, err)
	}
}

func TestReplayRefusesAnEntryNotInNormalForm(t *testing.T) {
	s := newStore(t)
	passes(t, s, 1)
	st, _ := s.Load()
	o := checked(OutcomePass)
	o.Origin, o.At = OriginChecker, t0
	o, _ = o.Normalize()
	o.Counted = Counted(o)
	o.Actor = "" // a hand-written line that leaves the actor out
	th := fastPolicy.For(o.Capability).Thresholds()
	line, _ := json.Marshal(JournalEntry{Seq: st.NextSeq, Prev: st.Head, Kind: EntryObservation, Observation: &o, Thresholds: &th})
	if err := s.appendJournal(line); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RecordCheck(checked(OutcomePass), fastPolicy); err == nil || !strings.Contains(err.Error(), "normal form") {
		t.Fatalf("catching up over an entry with no actor = %v", err)
	}
	if v, err := s.Verify(); err != nil || v.OK {
		t.Fatalf("Verify = %+v, %v; an entry with no actor must fail", v, err)
	}
	st, _ = s.Load()
	if len(st.Capabilities) != 1 {
		t.Fatalf("records = %v; nothing outside axis may hold one", st.Capabilities)
	}
}

func TestASameCeilingChangeStillSavesWhatItCaughtUp(t *testing.T) {
	s := newStore(t)
	passes(t, s, 2)
	appendWithoutState(t, s)
	rec, trs, err := s.SetCeiling("facts.hardware", DefaultCeiling, "")
	if err != nil || len(trs) != 0 || rec.Totals.Pass != 3 {
		t.Fatalf("SetCeiling = %+v %+v %v", rec, trs, err)
	}
	if st, _ := s.Load(); st.NextSeq != 4 {
		t.Fatalf("next seq = %d; the caught-up entry must be saved even when the ceiling does not change", st.NextSeq)
	}
	if v, err := s.Verify(); err != nil || !v.OK || len(v.Notes) != 0 {
		t.Fatalf("Verify = %+v, %v", v, err)
	}
	if got := len(entries(t, s)); got != 3 {
		t.Fatalf("journal has %d entries; a same-ceiling change must not add one", got)
	}
}

func TestVerifyCatchesEditsAndTruncation(t *testing.T) {
	s := newStore(t)
	passes(t, s, 3)
	if v, err := s.Verify(); err != nil || !v.OK {
		t.Fatalf("a clean record must verify: %+v %v", v, err)
	}
	statePath, journalPath := s.path(stateFile), s.path(journalFile)
	stateBytes, _ := os.ReadFile(statePath)
	journalBytes, _ := os.ReadFile(journalPath)
	restore := func() {
		_ = os.WriteFile(statePath, stateBytes, 0o600)
		_ = os.WriteFile(journalPath, journalBytes, 0o600)
	}
	expectProblem := func(name, want string) {
		t.Helper()
		v, err := s.Verify()
		if err != nil || v.OK || !strings.Contains(strings.Join(v.Problems, " | "), want) {
			t.Fatalf("%s: Verify = %+v, %v; want a problem containing %q", name, v, err, want)
		}
		restore()
	}

	_ = os.WriteFile(statePath, bytes.Replace(stateBytes, []byte(`"level": "unproven"`), []byte(`"level": "trusted"`), 1), 0o600)
	expectProblem("an edited state", "the state says trusted")

	lines := strings.SplitAfter(string(journalBytes), "\n")
	edited := lines[0] + strings.Replace(lines[1], "test checker", "test checker!", 1) + lines[2]
	_ = os.WriteFile(journalPath, []byte(edited), 0o600)
	expectProblem("an edited middle line", "entry 3 does not chain")

	edited = lines[0] + lines[1] + strings.Replace(lines[2], "test checker", "test checker!", 1)
	_ = os.WriteFile(journalPath, []byte(edited), 0o600)
	expectProblem("an edited last line", "journal position")

	_ = os.WriteFile(journalPath, []byte(lines[0]+lines[2]), 0o600)
	expectProblem("a removed line", "out of order")

	_ = os.WriteFile(journalPath, []byte(lines[0]), 0o600)
	if _, _, err := s.RecordCheck(checked(OutcomePass), fastPolicy); err == nil {
		t.Fatal("writing onto a truncated journal must fail")
	}
	expectProblem("a truncated journal", "the state counts 3 journal entries but the journal holds 1")
}

func TestAnIncompleteLastJournalLineIsAnError(t *testing.T) {
	s := newStore(t)
	passes(t, s, 1)
	f, _ := os.OpenFile(s.path(journalFile), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"seq":2`)
	_ = f.Close()
	if _, _, err := s.RecordCheck(checked(OutcomePass), fastPolicy); err == nil || !strings.Contains(err.Error(), "incomplete line") {
		t.Fatalf("RecordCheck onto a torn journal = %v", err)
	}
	if v, err := s.Verify(); err != nil || v.OK {
		t.Fatalf("Verify of a torn journal = %+v, %v", v, err)
	}
}

func TestAMissingJournalIsAnError(t *testing.T) {
	s := newStore(t)
	passes(t, s, 2)
	_ = os.Remove(s.path(journalFile))
	if _, _, err := s.RecordCheck(checked(OutcomePass), fastPolicy); err == nil || !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("RecordCheck with the journal gone = %v", err)
	}
	if v, err := s.Verify(); err != nil || v.OK {
		t.Fatalf("Verify with the journal gone = %+v, %v", v, err)
	}
}

func TestACorruptStateIsAnErrorAndIsLeftInPlace(t *testing.T) {
	s := newStore(t)
	passes(t, s, 1)
	if err := os.WriteFile(s.path(stateFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("a corrupt state must not load as empty")
	}
	if _, _, err := s.RecordCheck(checked(OutcomePass), fastPolicy); err == nil {
		t.Fatal("recording onto a corrupt state must fail")
	}
	if data, _ := os.ReadFile(s.path(stateFile)); string(data) != "{not json" {
		t.Fatalf("the corrupt state was rewritten: %q", data)
	}
}

func TestBadObservationsAreRefusedAndChangeNothing(t *testing.T) {
	s := newStore(t)
	passes(t, s, 1)
	stateBefore, _ := os.ReadFile(s.path(stateFile))
	journalBefore, _ := os.ReadFile(s.path(journalFile))
	for name, mutate := range map[string]func(*Observation){
		"capability id": func(o *Observation) { o.Capability = "Facts Hardware" },
		"actor":         func(o *Observation) { o.Actor = "Agent Codex" },
		"outcome":       func(o *Observation) { o.Outcome = "great" },
		"severity":      func(o *Observation) { o.Severity = "bad" },
		"evidence kind": func(o *Observation) { o.Evidence.Kind = "vibes" },
		"two-line":      func(o *Observation) { o.Claim = "one\ntwo" },
		"long claim":    func(o *Observation) { o.Claim = strings.Repeat("x", maxTextLen+1) },
	} {
		o := checked(OutcomePass)
		mutate(&o)
		if _, _, err := s.Record(o, fastPolicy); err == nil {
			t.Errorf("%s: a bad observation was recorded", name)
		}
	}
	stateAfter, _ := os.ReadFile(s.path(stateFile))
	journalAfter, _ := os.ReadFile(s.path(journalFile))
	if !bytes.Equal(stateBefore, stateAfter) || !bytes.Equal(journalBefore, journalAfter) {
		t.Fatal("a refused observation changed the record")
	}
}

func TestReportListsObservedAndUnobservedCapabilitiesInRungOrder(t *testing.T) {
	s := newStore(t)
	pols := Policies{
		"facts.hardware": {ID: "facts.hardware", Rung: 1, PromoteAfter: 10, Window: 20, MinRate: 0.9, Description: "Hardware facts"},
		"health.service": {ID: "health.service", Rung: 2},
	}
	for i := 0; i < 2; i++ {
		if _, _, err := s.RecordCheck(checked(OutcomePass), pols); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := s.Load()
	rep := BuildReport(st, pols, t0)
	if rep.JournalEntries != 2 || rep.JournalHead != st.Head || len(rep.Capabilities) != 2 {
		t.Fatalf("report = %+v", rep)
	}
	first, second := rep.Capabilities[0], rep.Capabilities[1]
	if first.Capability != "facts.hardware" || first.PassesToGo != 8 || first.NextLevel != LevelAdvisory || first.Rate != 1 || first.RateOver != 2 || first.AtCeiling || first.Description != "Hardware facts" {
		t.Fatalf("first entry = %+v", first)
	}
	if second.Capability != "health.service" || second.Level != LevelUnproven || second.Totals != (Totals{}) || second.PassesToGo != DefaultPromoteAfter {
		t.Fatalf("an unobserved capability must be listed as unproven: %+v", second)
	}
}

func TestResolveClosesOneOpenFailureForAMachineThatIsGone(t *testing.T) {
	s := newStore(t)
	failing := checked(OutcomeFail)
	failing.Subject = "node:gone"
	if _, _, err := s.RecordCheck(failing, fastPolicy); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ { // a long healthy streak elsewhere cannot promote past it
		o := checked(OutcomePass)
		o.Subject = "node:alpha"
		if rec, tr, err := s.RecordCheck(o, fastPolicy); err != nil || tr != nil || len(rec.OpenFailures) != 1 {
			t.Fatalf("pass %d: %+v %+v %v", i+1, rec, tr, err)
		}
	}
	if _, _, err := s.Resolve("facts.hardware", "node:alpha", "not open"); err == nil {
		t.Fatal("resolving a subject that is not open must be refused")
	}
	if _, _, err := s.Resolve("facts.hardware", "node:gone", ""); err == nil {
		t.Fatal("resolve must require a reason")
	}
	rec, trs, err := s.Resolve("facts.hardware", "node:gone", "machine retired")
	if err != nil || len(rec.OpenFailures) != 0 || rec.Level != LevelUnproven || len(trs) != 1 || trs[0].Kind != TransitionResolved {
		t.Fatalf("Resolve = %+v %+v %v; want the failure closed and the level unchanged", rec, trs, err)
	}
	if last := entries(t, s)[21]; last.Kind != EntryResolve || last.Resolve.Reason != "machine retired" {
		t.Fatalf("the resolution must be journaled with its reason: %+v", last)
	}
	o := checked(OutcomePass)
	o.Subject = "node:alpha"
	if rec, tr, err := s.RecordCheck(o, fastPolicy); err != nil || tr == nil || rec.Level != LevelAdvisory {
		t.Fatalf("with the failure closed the next pass must promote: %+v %+v %v", rec, tr, err)
	}
	if v, err := s.Verify(); err != nil || !v.OK {
		t.Fatalf("Verify after a resolution = %+v, %v", v, err)
	}
}

func TestReplayRefusesAMalformedResolution(t *testing.T) {
	for name, e := range map[string]JournalEntry{
		"no reason":       {Seq: 1, Kind: EntryResolve, Resolve: &Resolution{At: t0, Capability: "facts.gpu", Actor: DefaultActor, Subject: "node:a"}},
		"other actor":     {Seq: 1, Kind: EntryResolve, Resolve: &Resolution{At: t0, Capability: "facts.gpu", Actor: "agent:x", Subject: "node:a", Reason: "r"}},
		"extra payload":   {Seq: 1, Kind: EntryResolve, Resolve: &Resolution{At: t0, Capability: "facts.gpu", Actor: DefaultActor, Subject: "node:a", Reason: "r"}, Thresholds: &Thresholds{}},
		"untrimmed field": {Seq: 1, Kind: EntryResolve, Resolve: &Resolution{At: t0, Capability: "facts.gpu", Actor: DefaultActor, Subject: " node:a", Reason: "r"}},
	} {
		if err := e.validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestVerifyFindsAndRepairsALostTransition(t *testing.T) {
	s := newStore(t)
	passes(t, s, 9)
	// The derived log cannot be written: the promotion is saved in the journal
	// and the state, but its transition is lost from transitions.jsonl.
	if err := os.Mkdir(s.path(transitionsFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RecordCheck(checked(OutcomePass), fastPolicy); err == nil {
		t.Fatal("a failed transitions write must be reported")
	}
	if err := os.Remove(s.path(transitionsFile)); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Load(); st.Capabilities[Key("facts.hardware", DefaultActor)].Level != LevelAdvisory {
		t.Fatal("the promotion itself must be kept in the state")
	}
	v, err := s.Verify()
	if err != nil || v.OK || !strings.Contains(strings.Join(v.Problems, " | "), "transitions.jsonl does not match the journal") {
		t.Fatalf("Verify must find the lost transition: %+v %v", v, err)
	}
	if n, err := s.RepairTransitions(); err != nil || n != 1 {
		t.Fatalf("RepairTransitions = %d, %v; want the one promotion", n, err)
	}
	if v, err := s.Verify(); err != nil || !v.OK {
		t.Fatalf("Verify after the repair = %+v, %v", v, err)
	}
	if lines := readLines(t, s.path(transitionsFile)); len(lines) != 1 || !strings.Contains(lines[0], `"promoted"`) || !strings.Contains(lines[0], `"seq":10`) {
		t.Fatalf("rebuilt transitions = %v", lines)
	}
}

func TestConcurrentWritersNeverLoseOrRepeatAnEntry(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "capability")
	const writers, each = 4, 25
	errs := make(chan error, writers*each)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := Open(dir) // its own store, as separate processes would have
			for i := 0; i < each; i++ {
				if _, _, err := s.RecordCheck(checked(OutcomePass), fastPolicy); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("a concurrent write failed: %v", err)
	}
	s := Open(dir)
	st, err := s.Load()
	if err != nil || st.NextSeq != writers*each+1 || st.Capabilities[Key("facts.hardware", DefaultActor)].Totals.Pass != writers*each {
		t.Fatalf("state after %d writes = next %d, %v", writers*each, st.NextSeq, err)
	}
	for i, e := range entries(t, s) {
		if e.Seq != uint64(i+1) {
			t.Fatalf("entry %d has seq %d: concurrent writers lost or repeated one", i+1, e.Seq)
		}
	}
	if v, err := s.Verify(); err != nil || !v.OK || v.Entries != writers*each {
		t.Fatalf("Verify = %+v, %v", v, err)
	}
}
