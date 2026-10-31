# `hack/verify-specs.go`: check that specs carry their reviews

Date: 2026-10-31. Base: `origin/main` 5c9975b. Status: revised after a `revise` spec review;
awaiting a second spec review. Merges after #588.

## Problem

`AGENTS.md` §8 requires every feature spec to record each review as a block of fixed lines and every
finding with its resolution. Nothing checks it. The first two specs written under §8 already drifted:

- `2026-10-30-import-direction-check-design.md` (#585) records its first review without `Kind:` or
  `Head:` and its six later reviews as prose paragraphs; it predates the block format (#586,
  2026-10-31).
- `2026-10-31-pr-review-pending-gate-design.md` (#588) recorded four reviews as blocks, then its
  fifth as prose, and left one question without a status. #588 has since fixed its own record.

A record a script cannot read cannot be held to the rule, and drift starts with the first spec.

## Non-goals

- Judging review quality, or whether a fix is real. The check reads the record only.
- The three specs that predate the format, by name (see Design): the two on `main` at Base and #585's.
- Linking PRs to specs, deciding whether a PR is a feature, or counting diff reviews against the §8
  larger-feature threshold (that needs the PR's diff).
- Commit references: a bare `Fixed` is accepted, not only `fixed in <commit>`.

## Design

`hack/verify-specs.go`, `//go:build ignore`, standard library only, run as `go run
hack/verify-specs.go` from the module root, like `hack/lifecycle-check.go` and
`hack/verify-import-tiers.go`. Every file in `docs/superpowers/specs/` must be named
`YYYY-MM-DD-<topic>-design.md`; any other file fails, so a spec cannot escape the check by its name.
Each is checked unless it is on a fixed grandfather list in the script:
`2026-10-09-model-discovery-design.md`, `2026-10-19-axis-start-screen-design.md` and
`2026-10-30-import-direction-check-design.md`. A date cutoff would let a backdated name escape.

Lines inside fenced code blocks are ignored, so a spec can show an example record. Within
`## Review`:

1. **Sections.** `## Problem`, `## Non-goals`, `## Design`, `## Alternatives`, `## Research` and
   `## Review` headings are present.
2. **Blocks.** A block starts at a `Kind:` line and runs to the next `Kind:` line or the end of the
   section. Its header is the lines before its first item; it must contain exactly one each of
   `Kind:` (`spec` or `diff`), `Reviewer:`, `Model:`, `Base:` and `Head:` (7 to 40 hex characters,
   first word), `Date:` (`YYYY-MM-DD`) and `Verdict:` (`approve`, `approve-with-changes` or
   `revise`, first word). Other header lines, such as continuations of `Model:`, are allowed.
3. **Order.** At least one `spec` block. The last `spec` block and the last `diff` block, if any, do
   not have `Verdict: revise`.
4. **Items.** A finding is a line starting `N.` in a block; a question is a line starting `- ` after a
   `Questions:` line in the block. Each item runs until a blank line, the next item, `Questions:`
   or `Kind:`, so a resolution on a continuation line counts. Matching is case-insensitive: every
   finding contains `fixed` or `declined:`; every question contains `became`, `open:` or
   `verified`, the words AGENTS.md §8 and `review-prompt.md` use.
5. **Output.** Each problem names the file, line and rule; the exit code is 1 when any exist, else
   the script prints `specs passed: N checked, M grandfathered`.

Wiring: `hack/ci-preflight.sh` and `.github/workflows/ci.yml` run the check and its tests script;
`AGENTS.md` §3's "CI also runs" line names it.

Merge order: after #585 (merged) and #588. #588 converts its own record, so that every spec on this
PR's base passes when it merges; before merging, the checker runs against main.

## Tests

`hack/verify-specs-tests.sh` runs the built checker on fixture trees: a conforming spec passes; a
grandfathered name is skipped; a backdated new name is still checked; a misnamed file fails. Each
failure has a case: a missing section, a block missing each field, a bad `Kind`, `Verdict`, `Date`
or `Head`, a duplicate field, no spec block, the last spec block `revise`, the last diff block
`revise`, a finding without a resolution, a question without a status. Plus: a resolution on a
continuation line passes; an unindented `Model:` continuation passes; lowercase `open:` and
`declined:` and capitalised `Fixed` pass; a `Kind:` line inside a fenced example is ignored; and
its own CI wiring.

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
  by the authoring agent, so a recorded resolution per finding is the property worth enforcing
  (<https://stephens.page/blog/fresh-eyes-beat-a-second-family/paper/fresh-eyes-beat-a-second-family.pdf>).

Optimization chosen: check the record format, not its content, so the gate is deterministic and costs
milliseconds; content stays with the reviewers.

## Review

Kind: spec
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author (Claude Opus 5.5 and GPT-5.6 Sol out of weekly usage)
Base: 5c9975b5
Head: b65d0df8
Date: 2026-10-31
Verdict: revise

1. Major: keywords were case-sensitive, so the lowercase `open:` that AGENTS.md §8 and the review
   prompt prescribe failed. Fixed: matching is case-insensitive (Design 4); tests per variant.
2. Major: the migration missed a question in #588's spec with no status. Fixed in #588's own PR,
   which now records every review as a block; Problem updated.
3. Major: item boundaries were undefined; resolutions on continuation lines were missed. Fixed:
   Design 4 defines an item's extent; Design 2 allows other header lines; tests for both.
4. Major: the plan edited a file not on its base and named a precedent not on main. Fixed: merge
   order stated (after #588, which fixes its own record); precedent now `hack/lifecycle-check.go`;
   counts corrected (two specs on main; six prose reviews).
5. Minor: a date cutoff could be bypassed by backdating or renaming. Fixed: a named grandfather list
   and a naming rule for every file in the directory; tests for both.

Questions:
- Is a bare `Fixed` intended? Became a non-goal: commit references are not checked.
- Was "declined without evidence" accurate? Verified against the paper: the authoring agent declined
  both; Research reworded.
- `Kind:` inside a fenced example. Became Design: fenced lines are ignored; test.
- Two diff reviews for a larger feature. Became a non-goal: it needs the PR's diff.
