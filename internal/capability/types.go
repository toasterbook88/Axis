package capability

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Schema names the on-disk format of the state file and of an export.
const Schema = "axis.capability/1"

// DefaultActor is the only actor that holds a record. Per-actor records wait
// for a verified identity for the actor; the field stays in the schema.
const DefaultActor = "axis"

// Level is how far a capability is trusted. Levels are ordered.
type Level string

const (
	// LevelUnproven is the starting level: nothing has been demonstrated.
	LevelUnproven Level = "unproven"
	// LevelAdvisory means the capability's output may be shown as advice.
	LevelAdvisory Level = "advisory"
	// LevelSupervised means the capability may act once an operator approves.
	LevelSupervised Level = "supervised"
	// LevelTrusted means the capability may act within its class unprompted.
	LevelTrusted Level = "trusted"
)

var levelOrder = []Level{LevelUnproven, LevelAdvisory, LevelSupervised, LevelTrusted}

// Rank is the position of l in the ordering, or -1 for an unknown level.
func (l Level) Rank() int {
	for i, v := range levelOrder {
		if v == l {
			return i
		}
	}
	return -1
}

// ParseLevel parses a level name.
func ParseLevel(s string) (Level, error) {
	l := Level(strings.ToLower(strings.TrimSpace(s)))
	if l.Rank() < 0 {
		return "", fmt.Errorf("unknown level %q (want unproven, advisory, supervised or trusted)", s)
	}
	return l, nil
}

func levelAt(rank int) Level {
	if rank < 0 {
		rank = 0
	}
	if rank >= len(levelOrder) {
		rank = len(levelOrder) - 1
	}
	return levelOrder[rank]
}

// Outcome is the result of one observation.
type Outcome string

const (
	OutcomePass         Outcome = "pass"
	OutcomeFail         Outcome = "fail"
	OutcomeInconclusive Outcome = "inconclusive"
)

// Severity separates an ordinary failure from one that voids all earned trust.
type Severity string

const (
	SeverityNormal   Severity = "normal"
	SeverityCritical Severity = "critical"
)

// EvidenceKind says what an observation rests on.
type EvidenceKind string

const (
	// EvidenceLiveProbe is a command, URL or reading taken at observation time.
	EvidenceLiveProbe EvidenceKind = "live-probe"
	// EvidenceFile is a file identified by its content hash.
	EvidenceFile EvidenceKind = "file"
	// EvidencePeer is another agent's or document's say-so. It is a pointer only.
	EvidencePeer EvidenceKind = "peer"
	// EvidenceNone means no evidence was supplied.
	EvidenceNone EvidenceKind = "none"
)

// Evidence is what backs an observation.
type Evidence struct {
	Kind   EvidenceKind `json:"kind"`
	Ref    string       `json:"ref,omitempty"`
	SHA256 string       `json:"sha256,omitempty"`
}

// Grounded reports whether the evidence names something that touches reality:
// a live probe that names what was probed, or a file with its content hash.
// It says nothing about whether the probe was really run; that is why a pass
// counts only when Axis's own checker produced it (see Origin).
func (e Evidence) Grounded() bool {
	switch e.Kind {
	case EvidenceLiveProbe:
		return strings.TrimSpace(e.Ref) != ""
	case EvidenceFile:
		return strings.TrimSpace(e.Ref) != "" && strings.TrimSpace(e.SHA256) != ""
	}
	return false
}

// Origin says which path wrote an observation. The store sets it; a caller
// cannot choose it.
type Origin string

const (
	// OriginChecker is an observation produced by one of Axis's own checkers,
	// which ran its probe itself (`axis capability check`).
	OriginChecker Origin = "checker"
	// OriginExternal is an observation reported by a caller (`axis capability
	// record`). Axis did not run its probe, so its passes never count.
	OriginExternal Origin = "external"
)

// Observation is one checked claim about a capability.
type Observation struct {
	At         time.Time `json:"at"`
	Capability string    `json:"capability"`
	Actor      string    `json:"actor"`
	Origin     Origin    `json:"origin"`
	Subject    string    `json:"subject,omitempty"`
	Outcome    Outcome   `json:"outcome"`
	Severity   Severity  `json:"severity,omitempty"`
	Claim      string    `json:"claim,omitempty"`
	Checker    string    `json:"checker,omitempty"`
	Evidence   Evidence  `json:"evidence"`
	// Counted is the outcome the state machine applied (see Counted).
	Counted Outcome `json:"counted"`
}

// TransitionKind says why a level or ceiling changed.
type TransitionKind string

const (
	TransitionPromoted TransitionKind = "promoted"
	TransitionDemoted  TransitionKind = "demoted"
	TransitionCeiling  TransitionKind = "ceiling"
	// TransitionResolved is an operator closing one subject's open failure.
	TransitionResolved TransitionKind = "resolved"
)

// Transition is one change to a capability's level or ceiling.
type Transition struct {
	Kind       TransitionKind `json:"kind"`
	Capability string         `json:"capability"`
	Actor      string         `json:"actor"`
	From       Level          `json:"from"`
	To         Level          `json:"to"`
	At         time.Time      `json:"at"`
	Reason     string         `json:"reason"`
	// Seq is the journal entry that caused the change.
	Seq uint64 `json:"seq"`
	// Thresholds are the ones a promotion or demotion was judged by.
	Thresholds *Thresholds `json:"thresholds,omitempty"`
}

// Totals counts every observation applied to a record.
type Totals struct {
	Pass         int `json:"pass"`
	Fail         int `json:"fail"`
	Inconclusive int `json:"inconclusive"`
}

// Record is the standing of one capability for one actor.
type Record struct {
	Capability string    `json:"capability"`
	Actor      string    `json:"actor"`
	Level      Level     `json:"level"`
	Ceiling    Level     `json:"ceiling"`
	Since      time.Time `json:"since"`
	// Streak is the run of consecutive counted passes since the last failure
	// or level change.
	Streak int `json:"streak"`
	// StreakSince is when the current streak's first pass was observed.
	StreakSince time.Time `json:"streak_since,omitzero"`
	// OpenFailures are the subjects whose latest check by Axis's own checker
	// failed. While any is open the capability cannot be promoted.
	OpenFailures []string `json:"open_failures,omitempty"`
	Totals       Totals   `json:"totals"`
	// Recent holds the newest counted outcomes, oldest first: 'p' or 'f'.
	Recent         string      `json:"recent"`
	LastObserved   time.Time   `json:"last_observed,omitzero"`
	LastTransition *Transition `json:"last_transition,omitempty"`
}

// Rate is the share of passes in Recent, and how many outcomes it covers.
func (r Record) Rate() (rate float64, n int) {
	n = len(r.Recent)
	if n == 0 {
		return 0, 0
	}
	return float64(strings.Count(r.Recent, "p")) / float64(n), n
}

// State is the state file: a cache of replaying the journal.
type State struct {
	Schema    string    `json:"schema"`
	UpdatedAt time.Time `json:"updated_at"`
	// NextSeq is the sequence number the next journal entry will take.
	NextSeq uint64 `json:"next_seq"`
	// Head is the SHA-256 of the last journal line counted.
	Head string `json:"head"`
	// JournalBytes is the length of the journal up to and including that line.
	JournalBytes int64              `json:"journal_bytes"`
	Capabilities map[string]*Record `json:"capabilities"`
}

// Key identifies a record: one capability held by one actor.
func Key(capability, actor string) string { return capability + "@" + actor }

var (
	capabilityPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	actorPattern      = regexp.MustCompile(`^[a-z0-9]+(?:[._:-][a-z0-9]+)*$`)
)

const (
	maxIDLen   = 96
	maxTextLen = 512
)

// ValidateCapabilityID checks a capability name such as "facts.hardware".
func ValidateCapabilityID(id string) error {
	if len(id) == 0 || len(id) > maxIDLen || !capabilityPattern.MatchString(id) {
		return fmt.Errorf("capability %q: want lower-case words joined by . _ or -, at most %d characters", id, maxIDLen)
	}
	return nil
}

func validateActor(actor string) error {
	if len(actor) == 0 || len(actor) > maxIDLen || !actorPattern.MatchString(actor) {
		return fmt.Errorf("actor %q: want lower-case words joined by . _ : or -, at most %d characters", actor, maxIDLen)
	}
	if actor != DefaultActor {
		return fmt.Errorf("actor %q: only %q holds a record until an actor's identity can be verified", actor, DefaultActor)
	}
	return nil
}

func oneLine(field, s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) > maxTextLen {
		return "", fmt.Errorf("%s is %d characters; the limit is %d", field, len(s), maxTextLen)
	}
	if strings.ContainsAny(s, "\r\n\x00") {
		return "", fmt.Errorf("%s must be a single line", field)
	}
	return s, nil
}

// Normalize fills defaults and rejects an observation that cannot be recorded.
// It does not judge the evidence; Counted does.
func (o Observation) Normalize() (Observation, error) {
	if err := ValidateCapabilityID(o.Capability); err != nil {
		return o, err
	}
	if o.Actor == "" {
		o.Actor = DefaultActor
	}
	if err := validateActor(o.Actor); err != nil {
		return o, err
	}
	switch o.Origin {
	case OriginChecker, OriginExternal:
	default:
		return o, fmt.Errorf("origin %q: want checker or external", o.Origin)
	}
	switch o.Outcome {
	case OutcomePass, OutcomeFail, OutcomeInconclusive:
	default:
		return o, fmt.Errorf("outcome %q: want pass, fail or inconclusive", o.Outcome)
	}
	switch o.Severity {
	case "":
		o.Severity = SeverityNormal
	case SeverityNormal, SeverityCritical:
	default:
		return o, fmt.Errorf("severity %q: want normal or critical", o.Severity)
	}
	switch o.Evidence.Kind {
	case "":
		o.Evidence.Kind = EvidenceNone
	case EvidenceLiveProbe, EvidenceFile, EvidencePeer, EvidenceNone:
	default:
		return o, fmt.Errorf("evidence kind %q: want live-probe, file, peer or none", o.Evidence.Kind)
	}
	var err error
	if o.Subject, err = oneLine("subject", o.Subject); err != nil {
		return o, err
	}
	if o.Claim, err = oneLine("claim", o.Claim); err != nil {
		return o, err
	}
	if o.Checker, err = oneLine("checker", o.Checker); err != nil {
		return o, err
	}
	if o.Evidence.Ref, err = oneLine("evidence ref", o.Evidence.Ref); err != nil {
		return o, err
	}
	if o.Evidence.SHA256, err = oneLine("evidence sha256", o.Evidence.SHA256); err != nil {
		return o, err
	}
	return o, nil
}
