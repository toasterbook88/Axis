package capability

import (
	"fmt"
	"time"
)

// DefaultCeiling is the ceiling of a record the operator has not raised.
// Advisory is the highest level that cannot act, so a capability never reaches
// an acting level on evidence alone.
const DefaultCeiling = LevelAdvisory

// NewRecord is the starting record for a capability nobody has observed.
func NewRecord(capability, actor string, at time.Time) Record {
	return Record{
		Capability: capability,
		Actor:      actor,
		Level:      LevelUnproven,
		Ceiling:    DefaultCeiling,
		Since:      at,
	}
}

// Counted is the outcome the state machine applies for obs.
//
//   - A failure always counts, whoever reported it and whatever it cites:
//     losing trust must be harder to avoid than gaining it.
//   - A pass counts only when Axis's own checker produced it from grounded
//     evidence. A caller can name a probe it never ran; a checker cannot.
//   - Everything else is inconclusive.
func Counted(obs Observation) Outcome {
	switch obs.Outcome {
	case OutcomeFail:
		return OutcomeFail
	case OutcomePass:
		if obs.Origin == OriginChecker && obs.Evidence.Grounded() {
			return OutcomePass
		}
	}
	return OutcomeInconclusive
}

// Critical reports whether a failure voids all earned trust. It must be marked
// critical and rest on grounded evidence; an ungrounded "critical" failure
// still counts, as an ordinary one.
func Critical(obs Observation) bool {
	return obs.Outcome == OutcomeFail && obs.Severity == SeverityCritical && obs.Evidence.Grounded()
}

// Apply folds one observation, journal entry seq, into a record and reports the
// level change it caused, if any. It is a pure function. The thresholds are
// raised to the floors first.
//
//   - An inconclusive outcome is counted and changes nothing else.
//   - A counted pass extends the streak. The level rises one step when it is
//     below the ceiling, the streak has reached PromoteAfter and the pass rate
//     over the window is at least MinRate. Any level change resets the streak,
//     so each level is earned separately.
//   - A counted failure ends the streak and lowers the level one step; a
//     critical one lowers it to unproven.
func Apply(rec Record, th Thresholds, obs Observation, seq uint64) (Record, *Transition) {
	th = th.floored()
	rec.LastObserved = obs.At
	switch Counted(obs) {
	case OutcomePass:
		rec.Totals.Pass++
		rec.Streak++
		rec.Recent = trimRecent(rec.Recent+"p", th.Window)
		rate, _ := rec.Rate()
		if rec.Level.Rank() < rec.Ceiling.Rank() && rec.Streak >= th.PromoteAfter && rate >= th.MinRate {
			return move(rec, TransitionPromoted, levelAt(rec.Level.Rank()+1), obs.At, seq, &th,
				fmt.Sprintf("%d consecutive passes, %.0f%% over the last %d", rec.Streak, rate*100, len(rec.Recent)))
		}
	case OutcomeFail:
		rec.Totals.Fail++
		rec.Streak = 0
		rec.Recent = trimRecent(rec.Recent+"f", th.Window)
		if rec.Level == LevelUnproven {
			return rec, nil
		}
		to, reason := levelAt(rec.Level.Rank()-1), "a counted failure"
		switch {
		case Critical(obs):
			to, reason = LevelUnproven, "a grounded critical failure"
		case obs.Severity == SeverityCritical:
			reason = "a failure marked critical without grounded evidence, so one level"
		}
		return move(rec, TransitionDemoted, to, obs.At, seq, &th, reason)
	default:
		rec.Totals.Inconclusive++
	}
	return rec, nil
}

// SetCeiling changes the highest level a record may reach, as journal entry
// seq. Lowering the ceiling below the current level lowers the level with it.
// Raising it changes nothing else: the level still has to be earned.
func SetCeiling(rec Record, ceiling Level, at time.Time, reason string, seq uint64) (Record, []Transition) {
	var out []Transition
	if rec.Ceiling != ceiling {
		out = append(out, Transition{
			Kind: TransitionCeiling, Capability: rec.Capability, Actor: rec.Actor,
			From: rec.Ceiling, To: ceiling, At: at, Reason: reason, Seq: seq,
		})
		rec.Ceiling = ceiling
	}
	if rec.Level.Rank() > ceiling.Rank() {
		out = append(out, Transition{
			Kind: TransitionDemoted, Capability: rec.Capability, Actor: rec.Actor,
			From: rec.Level, To: ceiling, At: at, Reason: "the ceiling was lowered", Seq: seq,
		})
		rec.Level, rec.Since, rec.Streak = ceiling, at, 0
	}
	if len(out) > 0 {
		last := out[len(out)-1]
		rec.LastTransition = &last
	}
	return rec, out
}

func move(rec Record, kind TransitionKind, to Level, at time.Time, seq uint64, th *Thresholds, reason string) (Record, *Transition) {
	t := &Transition{
		Kind: kind, Capability: rec.Capability, Actor: rec.Actor,
		From: rec.Level, To: to, At: at, Reason: reason, Seq: seq, Thresholds: th,
	}
	rec.Level, rec.Since, rec.Streak = to, at, 0
	rec.LastTransition = t
	return rec, t
}

func trimRecent(s string, window int) string {
	if window > 0 && len(s) > window {
		return s[len(s)-window:]
	}
	return s
}
