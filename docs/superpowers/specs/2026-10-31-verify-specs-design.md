# `hack/verify-specs.go`: check that specs carry their reviews

Date: 2026-10-31. Base: `origin/main` 5c9975b. Status: implemented; the fresh diff review approved
with changes, applied. #588 has merged.

## Problem

`AGENTS.md` §8 requires every feature spec to record each review as a block of fixed lines and every
finding with its resolution. Nothing checks it. The first two specs written under §8 already drifted:

- `2026-10-30-import-direction-check-design.md` (#585) records its first review without `Kind:` or
  `Head:` and its six later reviews as prose paragraphs. Its reviews predate the block format
  (#586, merged 2026-10-31), though #585 merged after it.
- `2026-10-31-pr-review-pending-gate-design.md` (#588) recorded four reviews as blocks, then its
  fifth as prose, and left one question without a status. #588 has since fixed its own record.

A record a script cannot read cannot be held to the rule, and drift starts with the first spec.

## Non-goals

- Judging review quality, or whether a fix is real. The check reads the record only.
- Three specs, by name (see Design): the two on `main` at Base, which predate §8, and #585's, under a
  §8 waiver the maintainer granted on 2026-10-31: its reviews were recorded before the block format
  existed and some have no Head commit that could be filled in truthfully.
- Linking PRs to specs, deciding whether a PR is a feature, or counting diff reviews against the §8
  larger-feature threshold (that needs the PR's diff).
- Commit references: a bare `Fixed` is accepted, not only `fixed in <commit>`. The content of a
  decline (the run that supports it) is not checked either.
- Meaning: a later clause that opens with a marker word counts as a status, so `Became flaky under
  load.` passes. The check catches a missing status, not a misleading one.

## Design

`hack/verify-specs.go`, `//go:build ignore`, standard library only, run as `go run
hack/verify-specs.go` from the module root, like `hack/lifecycle-check.go` and
`hack/verify-import-tiers.go`. Every file in `docs/superpowers/specs/` must be named
`YYYY-MM-DD-<topic>-design.md`; any other file or a subdirectory fails, so a spec cannot escape the
check by its name or location.
Each is checked unless it is on a fixed grandfather list in the script:
`2026-10-09-model-discovery-design.md`, `2026-10-19-axis-start-screen-design.md` and
`2026-10-30-import-direction-check-design.md`. A date cutoff would let a backdated name escape.

A feature spec fails until its first approving spec review is recorded; that is intended, since §8
lets code start only then. Lines inside fenced code blocks (paired as CommonMark pairs them) are
ignored, so a spec can show an example record outside `## Review`; a fence inside `## Review`
fails, since it would hide records there, and a fence left open at the end of the file fails,
naming its opening line. Rule 1 applies to the whole file; rules 2 to 4 apply within `## Review`:

1. **Sections.** `## Problem`, `## Non-goals`, `## Design`, `## Alternatives`, `## Research` and
   `## Review` headings are present, `## Review` exactly once; it runs to the next `## ` heading.
2. **Blocks.** A block starts at a `Kind:` line and runs to the next `Kind:` line or the end of the
   section. Any non-blank line in `## Review` before the first `Kind:` fails, so a review recorded
   without `Kind:` (as #585's first one was) cannot sit ahead of the blocks. Its header is the run
   of lines from `Kind:` to the first blank line; it must contain exactly one each of `Kind:`
   (`spec` or `diff`), `Reviewer:`, `Model:`, `Base:` and `Head:` (each 7 to 40 lowercase hex
   characters, as git prints them), `Date:` (a real `YYYY-MM-DD` date) and `Verdict:` (`approve`,
   `approve-with-changes` or `revise`). `Base`, `Head` and `Verdict` are checked on their first
   word, so `Verdict: approve-with-changes (six inline findings)` passes. Other header lines, such
   as continuations of `Model:`, are allowed, but a header line starting `- ` fails (a question
   needs `Questions:`), and so does an item or an `F<n>` line there. `Verdict:` is the last header
   line: a line glued under it fails (leave a blank line after the header). Below the header, each
   paragraph (a run of non-blank lines) must start with an item, a `Questions:` line (matched
   case-insensitively, so a copied `QUESTIONS:` works) or a closing note starting `Note:`; any other
   paragraph, such as a prose review appended after the block, fails, whether or not the block has
   findings. Notes close a block: an item or `Questions:` after a `Note:` fails, and so does an
   indented line directly under `Questions:`. Items start at column 0 with `N. ` (digits, a dot and
   a space, so `6.7 min` is not one) or `- `; indented lines continue the line above. An unindented
   line that starts none of these, directly under an item, fails rather than joining it. Findings in
   a block are numbered 1, 2, 3 ... without a restart: a second `1.` means a new review written
   without its own `Kind:` block, and fails. An unindented line starting `F<n>` (the raw reviewer
   format) fails: findings are copied into `N.` items.
3. **Order.** At least one `spec` block. A later block of a kind supersedes earlier ones (a `revise`
   is followed by a re-review), so the last `spec` block and the last `diff` block, if any, do not
   have `Verdict: revise`.
4. **Items.** A finding is a line starting `N.` in a block; a question is a line starting `- ` after
   a `Questions:` line in the block. Each item runs until a blank line, the next item, a
   `Questions:`, `Note:` or `Kind:` line, so a resolution on a continuation line counts but a note
   never does. A leading severity word (`Blocker:`, `Major:`, `Minor:`) is removed. A clause starts
   after `.`, `;`, `:` or `?` followed by a space; the text before the first such break is the
   description. A status is a marker at the start of a later clause, matched case-insensitively:
   `fixed` or `became` followed by a non-letter, or `declined:`, `open:` or `verified:` with their
   colon. So `1. Minor: fixed timeouts are too short.` has no status, while `Fixed:`, `Fixed;`,
   `Fixed in d6d2773f:` and `Fixed when parsed;` after the description do. Every finding has `fixed`
   or `declined:`; every question has `became`, `open:` or `verified:`, the statuses AGENTS.md §8
   and `review-prompt.md` define (this PR adds `verified:` to both and to the PR template).
5. **Output.** Each problem names the file, line and rule; the exit code is 1 when any exist, else
   the script prints `specs passed: N checked, M grandfathered`.

Wiring: `hack/ci-preflight.sh` and `.github/workflows/ci.yml` run the check and its tests script;
`AGENTS.md` §3's "CI also runs" line names it.

Merge order: after #585 (merged) and #588. #588 converts its own record, so that every spec on this
PR's base passes when it merges; before merging, the checker runs against main. #588 rewrites the
same AGENTS.md §8 and PR-template paragraphs this PR adds `verified:` to; the merge keeps both
edits, and the tests script asserts that AGENTS.md, `review-prompt.md` and the PR template each
still define `verified:`.

## Tests

`hack/verify-specs-tests.sh` runs the built checker on fixture trees: a conforming spec passes; a
grandfathered name is skipped; a backdated new name is still checked; a misnamed file fails. Each
failure has a case: a missing section, a block missing each field, a bad `Kind`, `Verdict`, `Date`
or `Head`, a duplicate field, no spec block, the last spec block `revise`, the last diff block
`revise`, a finding without a resolution, a question without a status. Plus: a resolution on a
continuation line passes; an unindented `Model:` continuation passes; lowercase `open:` and
`declined:` and capitalised `Fixed` pass; a `Kind:` line inside a fenced example is ignored; an
unclosed fence fails; a bad `Base` fails; a restarted `1.` after a block's findings (prose review
appended) fails even when its items say "Fixed"; a prose review with no numbered items appended
after a block fails; a `Note:` line after items passes; a `- ` question with no `Questions:` line
fails; a copied `QUESTIONS:` line works; an `F1 [major]` line fails, but an indented continuation
starting `F2` passes; a finding whose text only mentions "fixed" in passing fails, including
`1. Minor: fixed timeouts are too short.`; a question that only mentions "verified" fails; a
subdirectory in the specs directory fails; and its own CI wiring. From the #588 record:
`Verdict: approve-with-changes (six inline findings)` and a `Head:` with trailing text pass, as do
`Fixed in <sha>:`, `Fixed;` and `Fixed when parsed;` after a description. A block with no
findings followed by a prose paragraph fails, and so does one followed by `Fifth review:` and
`1. ... Fixed:` items; a `Note: fixed later.` line directly after a finding with no status does
not resolve it; an indented line starting `6.7 min` continues its item. A #585-style review (fields
but no `Kind:`) before a conforming block fails; an unindented prose line glued under the last item
fails; an unindented `6.7 min` line is not an item; a second `## Review` fails. From the diff
review: a line glued under `Verdict:`, an `F<n>` line anywhere in a header, an indented line under
`Questions:`, an item after a `Note:`, a fence inside `## Review`, an impossible date and an empty
field fail; `Fixedness` and an `open:` inside a clause are not statuses. From Copilot: fences pair
as CommonMark does (a longer fence holds a shorter one, `~~~` holds backticks, a line with an info
string does not close); a fixture for each.

## Alternatives

| Option | Why not chosen |
| --- | --- |
| Front matter (YAML) for review records | Machine-friendly, but specs are read by people first; the block lines are already the §8 format and read as prose. |
| A shell `grep` check | Section scoping and per-block field counts get fragile in grep; a small Go program matches `verify-import-tiers.go`. |
| Check only on PRs that touch a spec | Needs base-diff plumbing in CI; checking every dated spec is cheap (a handful of files) and catches edits too. |
| GitHub Spec Kit's `/speckit.analyze` | An agent command that judges spec consistency, not a deterministic CI gate; complementary, not a substitute. |

## Research

- Kubernetes KEPs keep required metadata in a `kep.yaml` per proposal
  (<https://github.com/kubernetes/enhancements/blob/master/keps/sig-architecture/0000-kep-process/README.md>)
  and validate it in CI with `hack/verify-kep-metadata.sh`
  (<https://github.com/kubernetes/enhancements/blob/master/hack/verify-kep-metadata.sh>), which is what
  makes their status fields trustworthy.
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
- Was "declined without evidence" accurate? Verified: against the paper, the authoring agent declined
  both; Research reworded.
- `Kind:` inside a fenced example. Became Design: fenced lines are ignored; test.
- Two diff reviews for a larger feature. Became a non-goal: it needs the PR's diff.

Kind: spec
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author
Base: 5c9975b5
Head: 238d889e
Date: 2026-10-31
Verdict: revise

1. Major: a prose review appended after a block, or findings in the `F1` format, passed unchecked.
   Fixed: findings are numbered without a restart and `F<n>` lines fail (Design 2); tests with the
   real d6d2773f shape (items saying "Fixed") and an `F1` block.
2. Major: a status keyword anywhere in an item counted. Fixed: a status is a marker at the start of
   the item or a clause (Design 4); failing fixtures with "fixed" and "verified" used in passing.
3. Minor: an unclosed fence hid everything after it. Fixed: it fails, naming its opening line; test.
4. Minor: `verified` was not a status AGENTS.md §8 or the prompt defines. Fixed: this PR adds
   `verified: <how>` to both.
5. Minor: `Base:` had no format check. Fixed: same rule as `Head:`; test.

Questions:
- Does "last diff block" stand for every current diff review? Became Design 3: a later block of a
  kind supersedes earlier ones; counting two diff reviews stays a non-goal.
- A bare `declined:` passes. Became a non-goal: the content of a decline is not checked.
- Rule 1's scope was worded under "Within `## Review`". Became Design wording: rule 1 is file-wide.

Kind: spec
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author
Base: 5c9975b5
Head: 2db69509
Date: 2026-10-31
Verdict: revise

1. Major: a prose review with no numbered items, or questions without `Questions:`, still passed.
   Fixed: after a block's first item only items, `Questions:` and `Note:` lines are allowed, and a
   header line starting `- ` fails (Design 2); #588's two closing notes now start `Note:`; fixtures.
2. Minor: this spec failed its own rule 4 (a question began "Verified against"). Fixed: reworded.
3. Minor: the severity prefix made "fixed" in a description count as a status. Fixed: a leading
   severity word is removed before matching (Design 4); fixture.
4. Minor: the PR template did not list `verified:`. Fixed: added in this PR.
5. Minor: grandfathering #585 was presented as a date fact although it merged after the format.
   Fixed: recorded as the maintainer's §8 waiver (Non-goals).
6. Minor: the KEP link did not support "validated in CI". Fixed: Research cites
   `hack/verify-kep-metadata.sh`.

Questions:
- Does every new spec fail CI until its first approving review? Became Design: yes, intended.
- `F<n>` before or after trimming indentation? Became Design 2: unindented lines only; fixture.
- Is `Questions:` case-insensitive? Became Design 2: yes; fixture with `QUESTIONS:`.
- Are subdirectories scanned? Became Design: a subdirectory fails; fixture.

Kind: spec
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author
Base: 5c9975b5
Head: bf11867d
Date: 2026-10-31
Verdict: revise

1. Major: removing the severity word put "fixed" at the start of the text, so the spec's own
   failing fixture passed. Fixed: the first clause is the description and a status must start a
   later clause (Design 4); fixtures for the #588 forms; a misleading marker became a non-goal.
2. Major: a prose paragraph after a block with no findings counted as header. Fixed: the header
   ends at the first blank line and every later paragraph must start with an item, `Questions:` or
   `Note:` (Design 2); fixtures with and without numbered items.
3. Minor: no test pinned the first-word rule #588's `Verdict:` relies on. Fixed: fixtures for a
   `Verdict:` and a `Head:` with trailing text.

Questions:
- Can a `Note:` line right after an item resolve it? Became Design 4: an item ends at `Note:`;
  fixture.
- Is an indented `N.` an item? Became Design 2: items start at column 0; fixture.

Kind: spec
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author
Base: 5c9975b5
Head: 272fe232
Date: 2026-10-31
Verdict: approve-with-changes

1. Major: text before the first `Kind:` belonged to no block, and an unindented line glued under an
   item joined it, so a review without `Kind:` or an appended review passed. Fixed: both fail
   (Design 2); fixtures for each.
2. Minor: the merge plan did not say #588 rewrites the paragraphs this PR adds `verified:` to.
   Fixed: Merge order says the merge keeps both; the tests script asserts `verified:` survives.

Questions:
- Is an unindented `6.7 min` line an item? Became Design 2: an item needs `N. ` with a space;
  fixture.
- Does the colon stay with `open:` after the clause split? Became Design 4: markers are matched
  at the clause start with their colon.
- Is a second `## Review` checked? Became Design 1: exactly one; fixture.

Note: code starts; every finding is resolved.

Kind: diff
Reviewer: fresh-context subagent (Cursor)
Model: same vendor family as the author (the second diff review is Copilot's, on the PR)
Base: f71c239d
Head: 5c5c117a
Date: 2026-10-31
Verdict: approve-with-changes

1. Minor: lines that joined no item passed unchecked: prose or an `F<n>` line glued under a header,
   and an indented line under `Questions:`. Fixed: `Verdict:` ends the header and later glued
   lines fail, `F<n>` fails in a header, and an indented line under `Questions:` fails; fixtures.
2. Minor: `Note:` was accepted between items. Fixed: a note closes its block; fixture.
3. Minor: `Date:` accepted impossible dates. Fixed: parsed as a date; fixture.
4. Minor: three rules had no failing test (the non-letter guard, colon markers only at a clause
   start, an empty field). Fixed: a fixture for each; the reviewer's three surviving mutants now
   fail the suite.

Questions:
- A fence inside `## Review` hid a `revise` block. Became Design: a fence there fails; fixture.
- Uppercase hex is rejected. Became Design 2 wording: lowercase, as git prints commits.
- Does the merged §8 still list `verified:`? Verified: the #588 merge (3f5884c1) keeps
  `verified: <how>` in AGENTS.md §8 and the PR template; the tests script asserts it.
- staticcheck and actionlint were not run by the reviewer. Verified: `make ci-preflight` ran both
  and passed at 5c5c117a.

Note: every fix has a test that fails when the fix is removed.

Kind: diff
Reviewer: GitHub Copilot code review (PR #593)
Model: other vendor family than the author
Base: f71c239d
Head: 14bcb726
Date: 2026-10-31
Verdict: approve-with-changes (two inline findings)

1. Any triple-backtick line toggled a fence, so a four-backtick fence around an example, a tilde
   fence, or a line with an info string paired wrongly and could hide `## Review`. Fixed: fences
   pair by character and length, and a closing fence has nothing after it; three fixtures.
2. A raw `F<n>` line glued under `Verdict:` was dropped as a header line. Fixed in 770d920b, from
   the fresh diff review: `F<n>` fails anywhere in a header; fixture.

Note: each fence fixture fails when its part of the pairing rule is removed.
