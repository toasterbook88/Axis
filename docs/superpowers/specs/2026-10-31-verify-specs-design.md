# `hack/verify-specs.go`: check that specs carry their reviews

Date: 2026-10-31. Base: `origin/main` 5c9975b. Status: draft, awaiting spec review.

## Problem

`AGENTS.md` §8 requires every feature spec to record each review as a block of fixed lines and every
finding with its resolution. Nothing checks it. The first two specs written under §8 already drifted:

- `2026-10-30-import-direction-check-design.md` records its first review without `Kind:` or `Head:`
  and its four later reviews as prose paragraphs (it predates the block format, defined 2026-10-31).
- `2026-10-31-pr-review-pending-gate-design.md` records four reviews as blocks, then its fifth as a
  prose paragraph.

A record a script cannot read cannot be held to the rule, and drift starts with the first spec.

## Non-goals

- Judging review quality, or whether a fix is real. The check reads the record only.
- Specs dated before 2026-10-31 (the three on `main` today and #585's), which predate the format.
- Linking PRs to specs, or deciding whether a PR is a feature.

## Design

`hack/verify-specs.go`, `//go:build ignore`, standard library only, run as `go run
hack/verify-specs.go` from the module root, like `hack/verify-import-tiers.go`. It checks every
`docs/superpowers/specs/YYYY-MM-DD-*-design.md` whose date is 2026-10-31 or later:

1. **Sections.** `## Problem`, `## Non-goals`, `## Design`, `## Alternatives`, `## Research` and
   `## Review` headings are present.
2. **Blocks.** Inside `## Review`, a block starts at a `Kind:` line and runs to the next `Kind:` line
   or the end. Before its first numbered item it must have exactly one each of `Kind:` (`spec` or
   `diff`), `Reviewer:`, `Model:`, `Base:` and `Head:` (7 to 40 hex characters), `Date:`
   (`YYYY-MM-DD`) and `Verdict:` (`approve`, `approve-with-changes` or `revise`). Free text after the
   value, such as `Model:` notes on a following indented line, is allowed.
3. **Order.** At least one `spec` block. The last `spec` block and the last `diff` block, if any, do
   not have `Verdict: revise`.
4. **Resolutions.** Every numbered finding (`N.` at the start of a line inside a block) states a
   resolution: it contains `Fixed`, `fixed in` or `declined:`. Every question (a `- ` line after a
   `Questions:` line in a block) contains `Became`, `became`, `Open:` or `Verified`.
5. **Output.** Each problem names the file, line and rule; the exit code is 1 when any exist, else
   the script prints `specs passed: N checked, M grandfathered`.

Wiring: `hack/ci-preflight.sh` and `.github/workflows/ci.yml` run the check and its tests script;
`AGENTS.md` §3's "CI also runs" line names it. The pending-gate spec's prose fifth review becomes a
block in the same PR.

## Tests

`hack/verify-specs-tests.sh` runs the built checker on fixture trees: a conforming spec passes; a
spec dated before 2026-10-31 is skipped; each failure has a case: a missing section, a block missing
each field, a bad `Kind`, `Verdict`, `Date` or `Head`, a duplicate field, no spec block, the last
spec block `revise`, the last diff block `revise`, a finding without a resolution, a question
without a status; plus its own CI wiring.

## Alternatives

| Option | Why not chosen |
| --- | --- |
| Front matter (YAML) for review records | Machine-friendly, but specs are read by people first; the block lines are already the §8 format and read as prose. |
| A shell `grep` check | Section scoping and per-block field counts get fragile in grep; a small Go program matches `verify-import-tiers.go`. |
| Check only on PRs that touch a spec | Needs base-diff plumbing in CI; checking every dated spec is cheap (a handful of files) and catches edits too. |
| GitHub Spec Kit's `/speckit.analyze` | An agent command that judges spec consistency, not a deterministic CI gate; complementary, not a substitute. |

## Research

- Kubernetes KEPs keep required metadata in a `kep.yaml` per proposal and validate it in CI, which is
  what makes their status fields trustworthy
  (<https://github.com/kubernetes/enhancements/blob/master/keps/sig-architecture/0000-kep-process/README.md>).
- GitHub Spec Kit's `analyze` command checks spec artifacts for missing sections and inconsistency, as
  an agent step (<https://github.com/github/spec-kit/blob/main/templates/commands/analyze.md>).
- The Stephens replay study found that two of six escaped bugs had been raised in review and declined
  without evidence, so a recorded resolution per finding is the property worth enforcing
  (<https://stephens.page/blog/fresh-eyes-beat-a-second-family/paper/fresh-eyes-beat-a-second-family.pdf>).

Optimization chosen: check the record format, not its content, so the gate is deterministic and costs
milliseconds; content stays with the reviewers.

## Review
