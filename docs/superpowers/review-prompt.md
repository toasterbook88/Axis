# Fresh-context review prompt

The standard prompt for the reviews AGENTS.md §8 requires: the spec review before code, and the
review of the finished diff. Paste it into a new session or subagent, fill in the five inputs, and
give it nothing else: not the drafting conversation, not earlier reviews, not your own concerns.

```text
You are a fresh-context reviewer. You did not write this change and you have not seen the
conversation that produced it. Your job is to find what is missing or wrong. Never invent a finding.

Inputs (nothing else is in scope):
  Repo:   <absolute path to a checkout of the repo>
  Base:   <commit the change starts from>
  Head:   <commit under review>
  Spec:   <path at Head, or "none">
  Ask:    <"spec review" | "diff review against the spec" | one line from the author>

Read with `git -C <Repo> show <Head>:<path>` and `git -C <Repo> diff <Base> <Head>`. To run code,
extract Head into a new temp directory (`git -C <Repo> archive <Head> | tar -x -C "$(mktemp -d)"`)
and work there. Do not edit, commit or push in <Repo>, and do not touch shared services or real
cluster state.

What to check:
- Spec review: is the problem real (verify it in the code at Base), are the non-goals honest, does
  the design work against the code as it is, are the alternatives real, is the research sound, are
  the tests listed enough to fail when the design is broken?
- Diff review: compare the diff with the spec and classify each gap as missing (the spec asks, the
  code lacks), partial, contradicts (the code does something the spec rules out), or unrequested
  (the code does something the spec never asked for). Then look for defects the spec did not
  foresee: wrong behavior, failure paths, edge cases, gates in AGENTS.md this change breaks.
- Without a spec, review the diff on its own: defects, failure paths, edge cases, AGENTS.md rules.

Evidence rules:
- Every finding cites a file:line at Head and carries evidence you produced: a command and its
  output, or a test that fails while the finding is true. Say which.
- If you could not run a probe (a guard blocked it, or it needs a real system), say so in the
  verification log and mark that finding's evidence `static:` with the file:line reasoning.
- A concern you could not back with evidence is not a finding. List it under QUESTIONS.
- Report a defect class once, with its instances as evidence.
- Severity: blocker (must not merge), major (wrong behavior a user or CI would hit), minor.
- Approving with no findings is a valid result when the verification log supports it.

Return exactly this, and nothing after it:

VERDICT: approve | approve-with-changes | revise
REVIEWED: base <sha> head <sha> spec <path|none>
VERIFICATION LOG:
- <what you checked> | <command or file:line> | <result>
FINDINGS:
F1 [blocker|major|minor] [missing|partial|contradicts|unrequested|defect] <title>
  Where: <file:line at Head>
  Evidence: <command and output, or the failing test>
  Consequence: <what breaks, for whom>
  Fix: <the concrete change>
QUESTIONS:
- <concern you could not establish, and what would establish it>
```

## Choosing the reviewer

- **Spec reviews:** Claude Opus 5.5 as the second-family reviewer (maintainer's choice, 2026-10-30).
  Use the highest effort the harness offers; in Cursor subagents the available setting is
  `claude-opus-5-5-medium`. If the spec's author is itself a Claude model, the review is still a
  fresh top-tier session, not a cross-family one; say so in `Model:`.
- **Diff reviews:** a fresh session of the authoring model, plus a second independent one for a
  larger feature (AGENTS.md §8, step 4).
- Do not use a lightweight model as the second reviewer; it scored below a fresh same-model review.

## Recording the result

Add one block to the spec's `## Review` section per review (AGENTS.md §8, step 3):

```text
Kind: spec | diff
Reviewer: fresh-context subagent | new session (and the harness)
Model: <vendor family>
Base: <sha from REVIEWED>
Head: <sha from REVIEWED>
Date: <YYYY-MM-DD>
Verdict: approve | approve-with-changes | revise
```

Then list each finding with its resolution, `fixed in <commit>` or `declined: <reason>, <the command
or test run that supports declining>`, and each question as `became F<n>` or `open: <why not
probed>`. A decline without a run is not a resolution. Copy declines and open questions into the PR
body.

## Why this prompt

- A fresh look is the lever, more than a different model. In a preregistered replay of 183 reviews,
  a fresh same-model session caught as many escaped defects as another model family at every
  checkpoint; a second look after a whole feature was built found real missed bugs in 19 of 30
  features (fresh same model) and 16 of 30 (other family), with largely disjoint findings
  ([Stephens 2026](https://stephens.page/blog/fresh-eyes-beat-a-second-family/paper/fresh-eyes-beat-a-second-family.pdf)).
- Pairing models still adds coverage: one fresh same-model review plus one cross-model review matched
  56.7% of planted errors against 42.7% for two same-model reviews, and cross-model review did best
  on documents such as specs; a third reviewer added 1.3 points
  ([arXiv:2610.01471](https://arxiv.org/abs/2610.01471)).
- Findings must be acted on. Two of the six bugs that escaped in Stephens' study had been raised by
  the original review and declined by the author. On this repo, the Copilot review of #577 arrived
  after the merge and was never read; one of its findings (tag-less model matching in
  `ModelListed`) still holds on main.
- Evidence keeps findings honest. Replaying this prompt on #582 found 4 of the 5 issues later
  confirmed there, raised the fifth as a question, added one new verified issue, and reported no
  wrong findings; on #585 it found 2 new verified issues but missed one that Copilot caught. Two
  replays are a direction, not a measurement.
