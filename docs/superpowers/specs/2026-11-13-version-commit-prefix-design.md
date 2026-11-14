# `axis version`: stop reporting one commit as two

Date: 2026-11-13
Status: spec review pending
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
4. Otherwise return exact string equality, so an unrecognised value such as `unknown` never
   matches a real hash by accident.

7 is the floor because `git` itself will not abbreviate below it: shorter prefixes can
collide across a large repository, and a collision here would silently suppress a real
"stale daemon" warning.

Then `cmd/axis/main.go:260` uses it:

```go
case daemonID.Commit != "" && cliCommit != "" && !buildinfo.SameCommit(daemonID.Commit, cliCommit):
```

Nothing else changes: the printed texts keep their current wording, and a genuinely
different commit, a version mismatch, a missing commit, or a non-responding daemon all
behave exactly as they do at base.

## Tests

- `internal/buildinfo/build_test.go`: table test for `SameCommit` — equal full hashes;
  7-vs-8 prefix in both directions; full-40-vs-7 prefix; unrelated commits; equal-length
  different commits; empty side; `unknown`; non-hex; 6-character prefix below the floor;
  case difference; `-dirty` on one side and on both.
- `cmd/axis/daemon_restart_supervisor_test.go`: alongside the existing different-commit
  case (`def5678` vs `abc1234`), a case where the daemon reports `def5678` and the CLI
  `def5678a`, asserting **no** warning line. The existing assertions must keep passing
  unchanged.

Failing-then-passing check: with `SameCommit` reverted to `a != b`, the new daemon test
fails; with the prefix rule removed but the helper kept, the 7-vs-8 table rows fail.

## Alternatives considered

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
- Prior art: comparing abbreviated commit ids by prefix with a minimum length is the rule
  git itself applies when it lengthens abbreviations to stay unique.
- Measurement: the change is two string comparisons per `axis version` call; no performance
  claim is made because none is measurable at this scale. The observable claim is the
  difference in output, which the tests above pin.
