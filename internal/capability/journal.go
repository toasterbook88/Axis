package capability

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// EntryKind says what a journal entry records.
type EntryKind string

const (
	EntryObservation EntryKind = "observation"
	EntryCeiling     EntryKind = "ceiling"
)

// CeilingChange is an operator moving a capability's ceiling.
type CeilingChange struct {
	At         time.Time `json:"at"`
	Capability string    `json:"capability"`
	Actor      string    `json:"actor"`
	Ceiling    Level     `json:"ceiling"`
	Reason     string    `json:"reason"`
}

// JournalEntry is one line of the journal. Prev is the SHA-256 of the line
// before it, so an edited, removed or reordered line breaks the chain.
type JournalEntry struct {
	Seq         uint64         `json:"seq"`
	Prev        string         `json:"prev"`
	Kind        EntryKind      `json:"kind"`
	Observation *Observation   `json:"observation,omitempty"`
	Thresholds  *Thresholds    `json:"thresholds,omitempty"`
	Ceiling     *CeilingChange `json:"ceiling,omitempty"`
}

func lineHash(line []byte) string {
	sum := sha256.Sum256(line)
	return hex.EncodeToString(sum[:])
}

func (e JournalEntry) validate() error {
	if e.Seq == 0 {
		return errors.New("journal entries are numbered from 1")
	}
	switch e.Kind {
	case EntryObservation:
		if e.Observation == nil || e.Thresholds == nil || e.Ceiling != nil {
			return errors.New("an observation entry carries an observation and its thresholds, and nothing else")
		}
		obs := *e.Observation
		if obs.At.IsZero() {
			return errors.New("an observation needs a time")
		}
		norm, err := obs.Normalize()
		if err != nil {
			return err
		}
		if norm != obs {
			return errors.New("the observation is not in normal form (a field is missing, defaulted or untrimmed); a normal write never stores that")
		}
		if obs.Counted != Counted(obs) {
			return fmt.Errorf("counted %q does not follow from the observation (%q)", obs.Counted, Counted(obs))
		}
	case EntryCeiling:
		if e.Ceiling == nil || e.Observation != nil || e.Thresholds != nil {
			return errors.New("a ceiling entry carries a ceiling change and nothing else")
		}
		c := *e.Ceiling
		if c.At.IsZero() {
			return errors.New("a ceiling change needs a time")
		}
		if err := ValidateCapabilityID(c.Capability); err != nil {
			return err
		}
		if err := validateActor(c.Actor); err != nil {
			return err
		}
		if c.Ceiling.Rank() < 0 {
			return fmt.Errorf("unknown ceiling %q", c.Ceiling)
		}
		if reason, err := oneLine("reason", c.Reason); err != nil || reason != c.Reason || reason == "" {
			return errors.New("a ceiling change needs a single-line, trimmed reason")
		}
	default:
		return fmt.Errorf("unknown entry kind %q", e.Kind)
	}
	return nil
}

// advance moves the state's journal position past one line.
func advance(st *State, seq uint64, line []byte) {
	st.NextSeq = seq + 1
	st.Head = lineHash(line)
	st.JournalBytes += int64(len(line)) + 1
}

// applyEntry folds one journal entry into the records. It is the only way a
// record changes, for new entries and for replay alike.
func applyEntry(st *State, e JournalEntry) []Transition {
	switch e.Kind {
	case EntryObservation:
		obs := *e.Observation
		key := Key(obs.Capability, obs.Actor)
		rec := NewRecord(obs.Capability, obs.Actor, obs.At)
		if cur, ok := st.Capabilities[key]; ok {
			rec = *cur
		}
		rec, tr := Apply(rec, *e.Thresholds, obs, e.Seq)
		st.Capabilities[key] = &rec
		if tr != nil {
			return []Transition{*tr}
		}
	case EntryCeiling:
		c := *e.Ceiling
		key := Key(c.Capability, c.Actor)
		rec := NewRecord(c.Capability, c.Actor, c.At)
		if cur, ok := st.Capabilities[key]; ok {
			rec = *cur
		}
		rec, trs := SetCeiling(rec, c.Ceiling, c.At, c.Reason, e.Seq)
		st.Capabilities[key] = &rec
		return trs
	}
	return nil
}

// scanJournal reads journal lines starting at byte offset and calls fn with
// each parsed entry and its raw line (without the newline).
func scanJournal(r io.Reader, offset int64, fn func(JournalEntry, []byte) error) error {
	br := bufio.NewReader(r)
	pos := offset
	for {
		line, err := br.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			if len(line) > 0 {
				return fmt.Errorf("the journal ends in an incomplete line at byte %d (a write was interrupted); truncate the journal to %d bytes or move it aside", pos, pos)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the journal at byte %d: %w", pos, err)
		}
		body := line[:len(line)-1]
		var e JournalEntry
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&e); err != nil {
			return fmt.Errorf("the journal line at byte %d is not a journal entry: %w", pos, err)
		}
		if err := e.validate(); err != nil {
			return fmt.Errorf("journal entry %d (byte %d): %w", e.Seq, pos, err)
		}
		if err := fn(e, body); err != nil {
			return err
		}
		pos += int64(len(line))
	}
}
