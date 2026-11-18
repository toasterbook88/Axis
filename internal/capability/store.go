package capability

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/persist"
)

// File names under the store directory.
const (
	stateFile       = "state.json"
	journalFile     = "journal.jsonl"
	transitionsFile = "transitions.jsonl"
)

// Dir is the default store directory, ~/.axis/capability.
func Dir() string { return persist.AxisPath("capability") }

// PolicyPath is the operator's policy file, ~/.axis/capabilities.yaml.
func PolicyPath() string { return persist.AxisPath("capabilities.yaml") }

// Store reads and writes the capability record in one directory:
//
//	journal.jsonl      the record itself: every observation and ceiling change, hash-chained
//	state.json         a cache of replaying the journal
//	transitions.jsonl  a derived log of level and ceiling changes, for readers
type Store struct {
	dir string
	now func() time.Time
}

// Clock stamps every observation and ceiling change. Only tests replace it.
var Clock = func() time.Time { return time.Now().UTC() }

// Open returns a store rooted at dir. Nothing is created until the first write.
func Open(dir string) *Store {
	return &Store{dir: dir, now: func() time.Time { return Clock() }}
}

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

// Load reads the state file. A missing file is an empty record. A file that
// cannot be parsed is an error and is left in place: silently starting over
// would discard every demotion the record holds.
func (s *Store) Load() (State, error) {
	data, err := os.ReadFile(s.path(stateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return emptyState(), nil
	}
	if err != nil {
		return State{}, fmt.Errorf("reading capability record: %w", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("capability record %s is not valid JSON; fix or move it aside: %w", s.path(stateFile), err)
	}
	if st.Schema != Schema {
		return State{}, fmt.Errorf("capability record %s has schema %q; this build reads %q", s.path(stateFile), st.Schema, Schema)
	}
	if st.NextSeq == 0 || st.JournalBytes < 0 {
		return State{}, fmt.Errorf("capability record %s has an impossible journal position", s.path(stateFile))
	}
	if st.Capabilities == nil {
		st.Capabilities = map[string]*Record{}
	}
	return st, nil
}

func emptyState() State {
	return State{Schema: Schema, NextSeq: 1, Capabilities: map[string]*Record{}}
}

// Record adds an observation reported from outside Axis. Axis did not run its
// probe, so a pass is kept and counted as inconclusive; a failure counts.
func (s *Store) Record(obs Observation, policies Policies) (Record, *Transition, error) {
	obs.Origin = OriginExternal
	return s.commitObservation(obs, policies)
}

// RecordCheck adds an observation produced by one of Axis's own checkers.
func (s *Store) RecordCheck(obs Observation, policies Policies) (Record, *Transition, error) {
	obs.Origin = OriginChecker
	if strings.TrimSpace(obs.Checker) == "" {
		return Record{}, nil, errors.New("a checker observation must name its checker")
	}
	return s.commitObservation(obs, policies)
}

func (s *Store) commitObservation(obs Observation, policies Policies) (Record, *Transition, error) {
	obs, err := obs.Normalize()
	if err != nil {
		return Record{}, nil, err
	}
	release, err := persist.LockFile(s.path(stateFile))
	if err != nil {
		return Record{}, nil, err
	}
	defer release()

	st, caught, err := s.loadForWrite()
	if err != nil {
		return Record{}, nil, err
	}
	// The store, not the caller, says when an observation happened: a caller
	// that could backdate observations could fake the span a promotion needs.
	now := s.now()
	obs.At = now
	obs.Counted = Counted(obs)
	th := policies.For(obs.Capability).Thresholds()
	trs, err := s.commit(&st, JournalEntry{Kind: EntryObservation, Observation: &obs, Thresholds: &th}, caught, now)
	if err != nil {
		return Record{}, nil, err
	}
	var tr *Transition
	if len(trs) > 0 {
		tr = &trs[0]
	}
	return *st.Capabilities[Key(obs.Capability, obs.Actor)], tr, nil
}

// SetCeiling sets the highest level a capability may reach.
func (s *Store) SetCeiling(capability string, ceiling Level, reason string) (Record, []Transition, error) {
	if err := ValidateCapabilityID(capability); err != nil {
		return Record{}, nil, err
	}
	if ceiling.Rank() < 0 {
		return Record{}, nil, fmt.Errorf("unknown level %q", ceiling)
	}
	reason, err := oneLine("reason", reason)
	if err != nil {
		return Record{}, nil, err
	}
	if reason == "" {
		reason = "set by the operator"
	}
	release, err := persist.LockFile(s.path(stateFile))
	if err != nil {
		return Record{}, nil, err
	}
	defer release()

	st, caught, err := s.loadForWrite()
	if err != nil {
		return Record{}, nil, err
	}
	key := Key(capability, DefaultActor)
	now := s.now()
	if cur, ok := st.Capabilities[key]; ok && cur.Ceiling == ceiling {
		if err := s.saveCaught(st, caught, now); err != nil {
			return Record{}, nil, err
		}
		return *cur, nil, nil
	}
	change := &CeilingChange{At: now, Capability: capability, Actor: DefaultActor, Ceiling: ceiling, Reason: reason}
	trs, err := s.commit(&st, JournalEntry{Kind: EntryCeiling, Ceiling: change}, caught, now)
	if err != nil {
		return Record{}, nil, err
	}
	return *st.Capabilities[key], trs, nil
}

// Resolve closes one subject's open failure on a capability: the operator's way
// out when a machine that failed is retired, renamed or will not be checked
// again. Only a subject that is open can be resolved, and the reason is kept in
// the journal. It does not raise a level.
func (s *Store) Resolve(capability, subject, reason string) (Record, []Transition, error) {
	if err := ValidateCapabilityID(capability); err != nil {
		return Record{}, nil, err
	}
	subject, err := oneLine("subject", subject)
	if err != nil {
		return Record{}, nil, err
	}
	if reason, err = oneLine("reason", reason); err != nil {
		return Record{}, nil, err
	}
	if subject == "" || reason == "" {
		return Record{}, nil, errors.New("resolve needs a subject and a reason")
	}
	release, err := persist.LockFile(s.path(stateFile))
	if err != nil {
		return Record{}, nil, err
	}
	defer release()

	st, caught, err := s.loadForWrite()
	if err != nil {
		return Record{}, nil, err
	}
	now := s.now()
	key := Key(capability, DefaultActor)
	cur, ok := st.Capabilities[key]
	open := false
	if ok {
		for _, have := range cur.OpenFailures {
			open = open || have == subject
		}
	}
	if !open {
		if err := s.saveCaught(st, caught, now); err != nil {
			return Record{}, nil, err
		}
		return Record{}, nil, fmt.Errorf("%s has no open failure on %q", capability, subject)
	}
	change := &Resolution{At: now, Capability: capability, Actor: DefaultActor, Subject: subject, Reason: reason}
	trs, err := s.commit(&st, JournalEntry{Kind: EntryResolve, Resolve: change}, caught, now)
	if err != nil {
		return Record{}, nil, err
	}
	return *st.Capabilities[key], trs, nil
}

// commit writes one entry to the journal, folds it into the state and saves
// the state. The journal line is synced to disk before the state that counts
// it is written, so the state never counts an entry the journal lacks.
func (s *Store) commit(st *State, entry JournalEntry, caught []Transition, now time.Time) ([]Transition, error) {
	entry.Seq, entry.Prev = st.NextSeq, st.Head
	if err := entry.validate(); err != nil {
		return nil, err
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	if err := s.appendJournal(line); err != nil {
		return nil, err
	}
	advance(st, entry.Seq, line)
	trs := applyEntry(st, entry)
	if err := s.save(*st, now); err != nil {
		return nil, fmt.Errorf("journal entry %d is written but saving the state failed; the next write counts it: %w", entry.Seq, err)
	}
	if err := s.appendTransitions(append(caught, trs...)); err != nil {
		return nil, fmt.Errorf("journal entry %d is recorded, but writing the derived %s failed (the journal holds the change): %w", entry.Seq, transitionsFile, err)
	}
	return trs, nil
}

// loadForWrite loads the state and counts any journal entries it has not yet
// counted (a write that stopped after the journal and before the state). It
// returns the level changes those entries caused.
func (s *Store) loadForWrite() (State, []Transition, error) {
	st, err := s.Load()
	if err != nil {
		return State{}, nil, err
	}
	f, err := os.Open(s.path(journalFile))
	if errors.Is(err, fs.ErrNotExist) {
		if st.NextSeq != 1 || st.JournalBytes != 0 {
			return State{}, nil, fmt.Errorf("the state counts %d journal entries but %s is missing; run axis capability verify", st.NextSeq-1, s.path(journalFile))
		}
		return st, nil, nil
	}
	if err != nil {
		return State{}, nil, fmt.Errorf("opening the journal: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return State{}, nil, err
	}
	switch {
	case info.Size() < st.JournalBytes:
		return State{}, nil, fmt.Errorf("the journal is %d bytes but the state counts %d; run axis capability verify", info.Size(), st.JournalBytes)
	case info.Size() == st.JournalBytes:
		return st, nil, nil
	}
	if _, err := f.Seek(st.JournalBytes, io.SeekStart); err != nil {
		return State{}, nil, err
	}
	var caught []Transition
	err = scanJournal(f, st.JournalBytes, func(e JournalEntry, line []byte) error {
		if e.Seq != st.NextSeq || e.Prev != st.Head {
			return fmt.Errorf("journal entry %d does not follow the last entry the state counts (%d)", e.Seq, st.NextSeq-1)
		}
		advance(&st, e.Seq, line)
		caught = append(caught, applyEntry(&st, e)...)
		return nil
	})
	if err != nil {
		return State{}, nil, fmt.Errorf("catching up from the journal: %w; run axis capability verify", err)
	}
	return st, caught, nil
}

// saveCaught persists entries loadForWrite counted from the journal when the
// write that triggered it turns out to change nothing itself.
func (s *Store) saveCaught(st State, caught []Transition, now time.Time) error {
	saved, err := s.Load()
	if err != nil {
		return err
	}
	if saved.NextSeq == st.NextSeq {
		return nil
	}
	if err := s.save(st, now); err != nil {
		return err
	}
	return s.appendTransitions(caught)
}

func (s *Store) save(st State, now time.Time) error {
	st.UpdatedAt = now
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return persist.WritePrivateFileAtomic(s.path(stateFile), append(data, '\n'))
}

// appendJournal appends one line and syncs it, and the directory when the
// journal is new, before returning.
func (s *Store) appendJournal(line []byte) error {
	path := s.path(journalFile)
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, fs.ErrNotExist)
	f, err := persist.OpenPrivateFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return fmt.Errorf("opening the journal: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("writing the journal: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("syncing the journal: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing the journal: %w", err)
	}
	if created {
		return syncDir(s.dir)
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", dir, err)
	}
	return nil
}

func (s *Store) appendTransitions(trs []Transition) error {
	if len(trs) == 0 {
		return nil
	}
	f, err := persist.OpenPrivateFile(s.path(transitionsFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, t := range trs {
		line, err := json.Marshal(t)
		if err != nil {
			f.Close()
			return err
		}
		buf.Write(append(line, '\n'))
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Verification is the result of replaying the journal against the state.
type Verification struct {
	OK bool `json:"ok"`
	// Entries is how many valid entries the journal holds.
	Entries uint64 `json:"entries"`
	// Counted is how many entries the state says it has counted.
	Counted  uint64   `json:"counted"`
	Head     string   `json:"head,omitempty"`
	Problems []string `json:"problems,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

// Verify replays the journal from the start, checking every line's sequence
// number and chain link, and compares the result with the state. It writes
// nothing. A state that has not yet counted the newest entries is noted, not
// failed: the next write counts them.
func (s *Store) Verify() (Verification, error) {
	release, err := persist.LockFile(s.path(stateFile))
	if err != nil {
		return Verification{}, err
	}
	defer release()

	st, err := s.Load()
	if err != nil {
		return Verification{}, err
	}
	v := Verification{Counted: st.NextSeq - 1}
	replay, snap, trs, problem, err := s.replayJournal(v.Counted)
	if err != nil {
		return Verification{}, err
	}
	if problem != "" {
		v.Problems = append(v.Problems, problem)
	}
	v.Entries, v.Head = replay.NextSeq-1, replay.Head
	if snap == nil {
		v.Problems = append(v.Problems, fmt.Sprintf("the state counts %d journal entries but the journal holds %d valid ones", v.Counted, v.Entries))
	} else {
		v.Problems = append(v.Problems, compareStates(st, *snap)...)
		if problem == "" {
			if msg := s.checkTransitions(trs); msg != "" {
				v.Problems = append(v.Problems, msg)
			}
		}
		if behind := v.Entries - v.Counted; behind > 0 {
			v.Notes = append(v.Notes, fmt.Sprintf("the state has not yet counted the last %d journal entries; the next write counts them", behind))
		}
	}
	v.OK = len(v.Problems) == 0
	return v, nil
}

// replayJournal replays the whole journal. It returns the replayed state; the
// replay as it stood once counted entries were applied (nil if the journal
// never got that far); the transitions of those counted entries; and the first
// chain problem, if any.
func (s *Store) replayJournal(counted uint64) (State, *State, []Transition, string, error) {
	replay := emptyState()
	var snap *State
	var trs []Transition
	if counted == 0 {
		empty := emptyState()
		snap = &empty
	}
	f, err := os.Open(s.path(journalFile))
	if errors.Is(err, fs.ErrNotExist) {
		return replay, snap, trs, "", nil
	}
	if err != nil {
		return State{}, nil, nil, "", fmt.Errorf("opening the journal: %w", err)
	}
	defer f.Close()
	problem := ""
	err = scanJournal(f, 0, func(e JournalEntry, line []byte) error {
		if e.Seq != replay.NextSeq {
			return fmt.Errorf("journal entry %d is out of order: entry %d was expected", e.Seq, replay.NextSeq)
		}
		if e.Prev != replay.Head {
			return fmt.Errorf("journal entry %d does not chain to the entry before it: the journal was edited", e.Seq)
		}
		advance(&replay, e.Seq, line)
		got := applyEntry(&replay, e)
		if e.Seq <= counted {
			trs = append(trs, got...)
		}
		if replay.NextSeq-1 == counted {
			c := cloneState(replay)
			snap = &c
		}
		return nil
	})
	if err != nil {
		problem = err.Error()
	}
	return replay, snap, trs, problem, nil
}

// checkTransitions compares the derived transitions log with the transitions a
// replay of the journal produces. It returns "" when they match.
func (s *Store) checkTransitions(want []Transition) string {
	data, err := os.ReadFile(s.path(transitionsFile))
	if errors.Is(err, fs.ErrNotExist) {
		data, err = nil, nil
	}
	if err != nil {
		return fmt.Sprintf("%s cannot be read: %v", transitionsFile, err)
	}
	var got []Transition
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var t Transition
		if err := json.Unmarshal([]byte(line), &t); err != nil {
			return fmt.Sprintf("%s has a line that is not a transition; rebuild it with axis capability verify --repair-transitions", transitionsFile)
		}
		got = append(got, t)
	}
	for i := 0; i < len(got) && i < len(want); i++ {
		a, _ := json.Marshal(got[i])
		b, _ := json.Marshal(want[i])
		if !bytes.Equal(a, b) {
			return fmt.Sprintf("%s does not match the journal: its transition %d differs from the replay's; rebuild it with axis capability verify --repair-transitions", transitionsFile, i+1)
		}
	}
	if len(got) == len(want) {
		return ""
	}
	return fmt.Sprintf("%s does not match the journal (%d transitions there, %d in the replay); rebuild it with axis capability verify --repair-transitions", transitionsFile, len(got), len(want))
}

// RepairTransitions rewrites the derived transitions log from a replay of the
// journal, for when a write of it failed after the state was saved. It never
// touches the journal or the state, and it returns how many transitions it wrote.
func (s *Store) RepairTransitions() (int, error) {
	release, err := persist.LockFile(s.path(stateFile))
	if err != nil {
		return 0, err
	}
	defer release()
	st, err := s.Load()
	if err != nil {
		return 0, err
	}
	_, snap, trs, problem, err := s.replayJournal(st.NextSeq - 1)
	if err != nil {
		return 0, err
	}
	if problem != "" || snap == nil {
		return 0, errors.New("the journal does not replay cleanly; run axis capability verify")
	}
	var buf bytes.Buffer
	for _, t := range trs {
		line, err := json.Marshal(t)
		if err != nil {
			return 0, err
		}
		buf.Write(append(line, '\n'))
	}
	if err := persist.WritePrivateFileAtomic(s.path(transitionsFile), buf.Bytes()); err != nil {
		return 0, err
	}
	return len(trs), nil
}

func cloneState(st State) State {
	data, _ := json.Marshal(st)
	var out State
	_ = json.Unmarshal(data, &out)
	if out.Capabilities == nil {
		out.Capabilities = map[string]*Record{}
	}
	return out
}

// compareStates lists how the state differs from a replay of the journal.
func compareStates(st, replay State) []string {
	var out []string
	if st.Head != replay.Head || st.JournalBytes != replay.JournalBytes {
		out = append(out, "the state's journal position does not match the journal")
	}
	keys := map[string]bool{}
	for k := range st.Capabilities {
		keys[k] = true
	}
	for k := range replay.Capabilities {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		a, b := st.Capabilities[k], replay.Capabilities[k]
		switch {
		case a == nil:
			out = append(out, k+": the journal has a record the state lacks")
		case b == nil:
			out = append(out, k+": the state has a record the journal does not support")
		case a.Level != b.Level:
			out = append(out, fmt.Sprintf("%s: the state says %s, the journal gives %s", k, a.Level, b.Level))
		default:
			ja, _ := json.Marshal(a)
			jb, _ := json.Marshal(b)
			if !bytes.Equal(ja, jb) {
				out = append(out, k+": the state differs from the journal replay")
			}
		}
	}
	return out
}

// Entry is one record as shown to a reader, with its measured rate and the
// thresholds it is judged against.
type Entry struct {
	Record
	Rate       float64 `json:"rate"`
	RateOver   int     `json:"rate_over"`
	Policy     Policy  `json:"policy"`
	NextLevel  Level   `json:"next_level,omitempty"`
	PassesToGo int     `json:"passes_to_go,omitempty"`
	// EarliestPromotion is when the current streak will have covered the span
	// a promotion needs.
	EarliestPromotion time.Time `json:"earliest_promotion,omitzero"`
	AtCeiling         bool      `json:"at_ceiling"`
	Description       string    `json:"description,omitempty"`
}

// Report is the whole record as shown to a reader or exported for a page.
type Report struct {
	Schema         string    `json:"schema"`
	GeneratedAt    time.Time `json:"generated_at"`
	UpdatedAt      time.Time `json:"updated_at,omitzero"`
	JournalEntries uint64    `json:"journal_entries"`
	JournalHead    string    `json:"journal_head,omitempty"`
	Capabilities   []Entry   `json:"capabilities"`
}

// BuildReport turns a state into a sorted report. Capabilities the policy file
// names but nobody has observed are listed as unproven.
func BuildReport(st State, policies Policies, now time.Time) Report {
	rep := Report{Schema: Schema, GeneratedAt: now, UpdatedAt: st.UpdatedAt,
		JournalEntries: st.NextSeq - 1, JournalHead: st.Head, Capabilities: []Entry{}}
	seen := map[string]bool{}
	for _, rec := range st.Capabilities {
		seen[rec.Capability] = true
		rep.Capabilities = append(rep.Capabilities, entryFor(*rec, policies.For(rec.Capability)))
	}
	for id := range policies {
		if !seen[id] {
			rep.Capabilities = append(rep.Capabilities, entryFor(NewRecord(id, DefaultActor, time.Time{}), policies.For(id)))
		}
	}
	sort.Slice(rep.Capabilities, func(i, j int) bool {
		a, b := rep.Capabilities[i], rep.Capabilities[j]
		if a.Policy.Rung != b.Policy.Rung {
			return a.Policy.Rung < b.Policy.Rung
		}
		if a.Capability != b.Capability {
			return a.Capability < b.Capability
		}
		return a.Actor < b.Actor
	})
	return rep
}

func entryFor(rec Record, pol Policy) Entry {
	rate, n := rec.Rate()
	e := Entry{Record: rec, Rate: rate, RateOver: n, Policy: pol, Description: pol.Description}
	e.AtCeiling = rec.Level.Rank() >= rec.Ceiling.Rank()
	if !e.AtCeiling {
		e.NextLevel = levelAt(rec.Level.Rank() + 1)
		th := pol.Thresholds()
		if left := th.PromoteAfter - rec.Streak; left > 0 {
			e.PassesToGo = left
		}
		if rec.Streak > 0 {
			e.EarliestPromotion = rec.StreakSince.Add(th.MinSpan())
		}
	}
	return e
}
