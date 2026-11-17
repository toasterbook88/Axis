// Package capability is EXPERIMENTAL — an evidence-backed record of how far each
// capability has earned trust, and the state machine that moves it.
//
// A capability (for example "facts.hardware") is observed, and each
// observation is a pass, a fail or inconclusive. Two rules decide what counts:
//
//   - A pass counts only when one of Axis's own checkers produced it
//     (`axis capability check`) from grounded evidence: a live probe it ran,
//     or a file it hashed. A pass reported from outside (`axis capability
//     record`) is kept in the journal and counted as inconclusive.
//   - Every failure counts, whoever reports it and whatever it cites. Only a
//     failure with grounded evidence may be critical.
//
// Counted passes raise a capability one level at a time, up to a ceiling the
// operator sets. A counted failure lowers it at once.
//
// Every observation and ceiling change is an entry in an append-only journal
// whose lines are chained by SHA-256. The state file is a cache of replaying
// that journal; `axis capability verify` replays it and reports any
// difference.
//
// The record is advisory. Nothing in AXIS reads a level to allow or refuse an
// action.
//
// PRECONDITION FOR AUTHORITY. Every agent on a node runs as the same user that
// owns ~/.axis, so anything that can run `axis capability` can also rewrite
// the journal and the state. Verify detects an edited state, an edited or
// removed journal line, and a journal shorter than the state; it does not
// detect a consistent rewrite of both. Before any surface is allowed to read a
// level to grant an action, the journal head (Report.JournalHead) must be
// anchored where a later rewrite cannot change it unnoticed, such as the
// node's existing backups, and checked against that history.
package capability
