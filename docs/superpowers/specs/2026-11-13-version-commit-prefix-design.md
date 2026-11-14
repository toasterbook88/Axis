# `axis version`: stop reporting one commit as two

Date: 2026-11-13
Status: spec and diff reviews recorded; PR open, not merged
Owning area: `internal/buildinfo`, `cmd/axis/main.go` (version command)

## Problem

`axis version` prints a false warning when the CLI and the running daemon are built from
the **same** commit by different build paths:

```
  daemon:   v0.24.2 (commit b59faba) on ~/.axis/axis.sock
  warning:  daemon commit b59faba differs from CLI commit b59faba1 (same version 0.24.2)
```

Both are commit `b59faba1…`; the release artifact carries a 7-character abbreviation and a
`make build` carries 8. Observed 2026-11-10 on v0.24.2: the installed release binary reports
`commit: b59faba`, a build from the same tree reports `commit: b59faba1`, and `axis version`
against the running daemon prints the warning above.

Cause, at base `dec1d544`:

- `internal/buildinfo/build.go:23` `ResolvedCommit()` returns whatever the build injected.
- `.goreleaser.yml:26` injects `{{ .ShortCommit }}` (7 characters).
- `Makefile:2` injects `git rev-parse --short HEAD`, which auto-scales with repository size
  and currently yields 8 characters.
- `cmd/axis/main.go:260` compares the two strings for exact equality:

  ```go
  case daemonID.Commit != "" && cliCommit != "" && daemonID.Commit != cliCommit:
  ```

The warning therefore reports a difference that does not exist. It is cosmetic — the version
comparison on the line above is independent and correct — but `axis version` is the surface
operators use to tell a stale daemon from a current one, and a permanent false positive there
teaches them to ignore the one warning that matters.

## Non-goals

- Not changing how versions are compared (`daemonID.Version != Version` stays as it is).
- Not changing the update path, install paths, or the daemon wire contract.
- Not making the two build paths emit the same abbreviation length. Older installed binaries
  would keep their length anyway, so normalising at the source cannot fix a mixed fleet.
- Not adding dirty-state reporting. `ResolvedCommit` already marks a dirty toolchain revision
  and that text stays visible in the `commit:` line.

## Design

Add one function to `internal/buildinfo`, the package that already owns commit identity:

```go
// SameCommit reports whether two commit strings name the same revision.
// Abbreviated hashes compare by prefix, because a release build (.ShortCommit, 7)
// and a make build (git rev-parse --short, 8+) abbreviate differently. A "-dirty"
// suffix marks the build, not the revision, so it is ignored.
func SameCommit(a, b string) bool
```

Rules, in order:

1. Trim space and lower-case both inputs; strip one trailing `-dirty` from each.
2. If either side is empty, return false (the caller keeps its own empty guards).
3. If both are hex and at least 7 characters, return true when one is a prefix of the other
   (equal strings are prefixes of each other).
4. Otherwise return equality of the normalised strings. An unrecognised value such as
   `unknown` therefore matches only itself, never a real hash: `unknown` vs `unknown` must
   stay silent (a tarball build reports it on both sides), while `unknown` vs `abc1234`
   stays a warning.

7 is a deliberate policy floor, not a git guarantee: `git rev-parse --short=4` will hand
back four characters if asked. The floor exists because a short prefix can collide across a
large repository, and a collision here would silently suppress a real "stale daemon"
warning. Accepted residual: a build that injects fewer than 7 characters by hand
(`-X …Commit=$(git rev-parse --short=6 HEAD)`, or a low `core.abbrev`) is compared by exact
equality and can still produce the false warning. No default build path in this repository
does that, so the residual is documented rather than engineered around.

Then `cmd/axis/main.go:260` uses it:

```go
case daemonID.Commit != "" && cliCommit != "" && !buildinfo.SameCommit(daemonID.Commit, cliCommit):
```

Nothing else changes: the printed texts keep their current wording, and a genuinely
different commit, a version mismatch, a missing commit, or a non-responding daemon all
behave exactly as they do at base.

Unrecognised values stay warnings on purpose. `Makefile:2` falls back to `unknown` when git
metadata is absent, so a tarball build prints `daemon commit abc1234 differs from CLI commit
unknown`. That is accurate — an unversioned CLI against a versioned daemon is a state worth
seeing — so the spec keeps it rather than adding a second suppression rule that would hide
real mismatches behind a substring check.

## Tests

- `internal/buildinfo/build_test.go`: table test for `SameCommit` — equal full hashes;
  7-vs-8 prefix in both directions; full-40-vs-7 prefix; unrelated commits; equal-length
  different commits; empty side; `unknown`; non-hex; 6-character prefix below the floor;
  case difference; equal full 40-character revisions; `-dirty` on one side and on both;
  padded input (`"  abc1234  "` vs `"abc1234"`) so the trim clause in rule 1 cannot regress
  untested; `-dirty` combined with a differing length (`"abc1234-dirty"` vs `"abc1234a"`);
  identical values below the floor (`"abc123"` vs `"abc123"`); and two identical `unknown`
  values.
- `cmd/axis/daemon_restart_supervisor_test.go`: alongside the existing different-commit
  case (`def5678` vs `abc1234`), a case where the daemon reports `def5678` and the CLI
  `def5678a`, asserting **no** warning line, and a case where both report `unknown`,
  asserting no warning either. The existing assertions must keep passing unchanged.

Failing-then-passing check: with `SameCommit` reverted to `a != b`, the abbreviated-commit
daemon test fails; with the prefix rule removed but the helper kept, the 7-vs-8 table rows
fail; with rule 4 returning false instead of equality, the `unknown`-on-both daemon test
fails.

## Alternatives

1. **Make every build path emit the same length.** Set `--short=8` in the Makefile and
   `{{ .ShortCommit }}`-equivalent 8 in goreleaser. Rejected: binaries already installed on
   the fleet keep 7, so the warning survives for exactly the mixed case that produced it,
   and a future change to git's auto-scaling reopens it.
2. **Compare the first N characters of the shorter side without a floor.** Equivalent to the
   chosen design minus the 7-character guard; rejected because a 4-character prefix can
   collide, and a collision suppresses a real warning instead of raising a false one.
3. **Have the daemon report the full 40-character revision.** Cleanest identity, but it
   changes the daemon's version payload and still cannot help a daemon already running.
4. **Delete the commit warning.** Rejected: it is the only signal that a daemon was left
   running from another commit, which is a real and common state after an update.

## Research

- `.goreleaser.yml:26` uses `{{ .ShortCommit }}`; the v0.24.2 release artifact reports
  `commit: b59faba` (7 characters), measured with `axis version` on 2026-11-10.
- `Makefile:2` uses `git rev-parse --short HEAD`, which prints 8 characters in this
  repository (`git rev-parse --short HEAD` → `e49d8aba`) because git lengthens the
  abbreviation as object counts grow.
- `cmd/axis/main.go:260` at base compares the two with `!=`; `cmd/axis/daemon_restart_supervisor_test.go:250`
  pins the exact-string behaviour with `def5678` vs `abc1234`.
- Prior art: comparing abbreviated commit ids by prefix, with a minimum length, is the rule
  git applies when it lengthens abbreviations to stay unique
  (<https://git-scm.com/docs/git-rev-parse#Documentation/git-rev-parse.txt---shortlength>,
  <https://git-scm.com/docs/git-config#Documentation/git-config.txt-coreabbrev>); GoReleaser
  documents `.ShortCommit` as the abbreviated revision
  (<https://goreleaser.com/customization/templates/>).
- Measurement: the change is two string comparisons per `axis version` call; no performance
  claim is made because none is measurable at this scale. The observable claim is the
  difference in output, which the tests above pin.

## Review

Kind: spec
Reviewer: fresh-context subagent, five inputs only (repo, base, head, spec, ask)
Model: DeepSeek (same vendor family as the author; cross-family review not available)
Base: dec1d544
Head: 1638c5ac
Date: 2026-11-13
Verdict: approve-with-changes

1. Major: the spec failed its own gate, which requires a `## Alternatives` heading and a
   `## Review` section. `go run hack/verify-specs.go` reported "missing section ##
   Alternatives" and "missing section ## Review". Fixed: heading renamed and this block
   added; the verifier is green.
2. Minor: Research cited prior art without links, which AGENTS.md section 8 requires.
   Fixed: git-rev-parse, git-config core.abbrev and GoReleaser template links added.
3. Minor: the research said git will not abbreviate below 7 characters; `git rev-parse
   --short=4` returns four. Fixed: the floor is now stated as a policy choice with its
   collision reason and the accepted sub-7 residual.
4. Minor: no listed test row could fail if rule 1's trim clause were dropped. Fixed:
   padded-input row added, plus a `-dirty` row with a differing length.

Questions:

- Whether a tarball build's `commit: unknown` should suppress the daemon warning, since
  that fallback also produces a warning operators may learn to ignore. open: the design now
  answers no, with the reason recorded under Design.
- Residual risk for a build that injects fewer than 7 characters by hand. open: documented
  as accepted, because no default build path in this repository emits one.

Kind: diff
Reviewer: fresh-context subagent, five inputs only (repo, base, head, spec, ask)
Model: DeepSeek (same vendor family as the author; cross-family review not available)
Base: dec1d544
Head: f1d1c64a
Date: 2026-11-13
Verdict: approve-with-changes

1. Major: the implementation contradicted rule 4 — every non-hex or sub-7 value returned
   false, so a tarball build printed "daemon commit unknown differs from CLI commit unknown".
   Fixed: rule 4 is now equality of the normalised strings, and a daemon test covers both
   sides reporting `unknown`.
2. Minor: the spec's `Status:` line still said the spec review was pending. Fixed: the line
   now reflects the recorded reviews.
3. Minor: the test table had no equal-full-hash row although the spec listed one. Fixed: row
   added, plus identical-below-floor and repeated-`-dirty` rows.

Questions:

- Whether the two identical `unknown` values were meant to match. verified: yes — finding 1
  made the fallback compare them for equality, and a daemon test now covers that state.
