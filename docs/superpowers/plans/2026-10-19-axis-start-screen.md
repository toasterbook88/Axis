# AXIS Start Screen and Bare `axis` Front Door — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task (operator's choice), following the **Execution Protocol** section below. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bare `axis` on a terminal opens `axis agent`, which paints a responsive Option B start screen instantly and finishes startup (cluster context, MCP, model) in the background behind one minimal spinner.

**Architecture:** `internal/ui` gains pure, env-driven terminal capability detection (color depth, Unicode) and a depth-aware logo gradient. `internal/console` gains a pluggable `StartScreen` (Option B = `HeroScreen`) rendered in the ephemeral `View()` until the first submit, a model-readiness state that holds prompts while a model is being found, and a single spinner. `cmd/axis` gains one async console startup path: paint first, then load runtime → connect MCP → build and bind the agent → resolve the model, each step bounded and reported on screen.

**Tech Stack:** Go 1.26.x, Bubble Tea v1, lipgloss v1 (width math only), cobra. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-19-axis-start-screen-design.md` (approved 2026-10-19). Read it before Task 1. Where this plan and the spec differ, this plan records the reason inline; the plan wins on implementation detail, the spec wins on behavior.

---

## Executor Prompt (goal contract)

You are implementing a UI change in AXIS, a Go CLI that is the operator's cluster
front door. The quality bar is flight software: every loop, wait, network call
and goroutine is bounded; every error is handled and shown; uncertainty degrades
to a visible, honest state ("no model", "no snapshot"), never a guess; every
failure path has a test. Small functions, clear names, comments that explain
why, not what.

**Goal.** When you are done, an operator who types `axis` in any terminal, at
any size, sees the AXIS logo and a prompt in well under a second, can type
immediately, and is never left with a frozen, crashing, or garbled screen —
including when no model is running, the daemon is down, or an MCP server hangs.

**Definition of done (all must hold; prove each with command output):**

1. Every task below is complete, each with its tests written first and passing.
2. `GOTOOLCHAIN=go1.26.9 ./hack/ci-preflight.sh` passes on the final commit
   (it runs test, race, build, lint, coverage, and every `hack/verify-*` gate).
3. `git status --short` is clean apart from the known test artifacts, which you
   restore (see Global Constraints) before every commit.
4. The manual matrix in Task 9 has been run and its results recorded in the PR
   description draft (do not open the PR).
5. No new direct dependency: `go.mod` direct `require` block is unchanged.
6. Nothing outside the worktree was modified. Nothing was pushed.

**Stop and ask the operator (do not improvise) if:**

- A task needs a new dependency, a config key in `nodes.yaml`/`ai.yaml`, or a
  change to an exit code other than those named here.
- A gate fails for a reason unrelated to this change (report it; don't fix it).
- An existing test asserts behavior this plan says to change, and the plan does
  not name that test. Name the test and the conflict.
- The 500-line file-size hook blocks an edit. Report the file and line counts.

**Report at the end:** verdict first; per task `complete` / `partial` /
`diverged` (why) / `deferred`, each with evidence (command + trimmed output);
then `git log --oneline origin/main..HEAD`, `git diff --stat origin/main`,
assumptions, and anything you could not verify.

---

## Global Constraints

- Base: a new branch `feat/agent-start-screen` from current `origin/main`, in a
  separate worktree (Task 0). Never work in the operator's main checkout; it has
  uncommitted files.
- Go toolchain for lint: `GOTOOLCHAIN=go1.26.9` (host Go 1.27 breaks the pinned
  staticcheck). Tests also run fine on 1.26.9; use it everywhere.
- Run Go tests only through `./hack/hermetic-go-test.sh <args>` or `make test` /
  `make test-race`. A bare `go test` writes the operator's real `~/.axis`.
- Tests rewrite tracked `artifacts/a2a-slice2-e2e-latest.json` and add
  `artifacts/a2a-slice2-e2e-<sha>.json`. Before each commit:
  `git checkout -- artifacts/a2a-slice2-e2e-latest.json` and delete the new
  `artifacts/a2a-slice2-e2e-*.json` file. Never `git add -A`; add files by name.
- Public repository: no real hostnames, IPs, usernames, home paths, node names
  or model names from any real cluster in code, tests, comments, or docs. Use
  `node-a`, `example-model`, `/home/user`. `hack/verify-public-boundary.sh` must pass.
- No new direct dependencies. Do not import `github.com/muesli/termenv`,
  `github.com/charmbracelet/colorprofile`, or `github.com/charmbracelet/x/...`
  directly; that changes the direct-dependency count AGENTS.md pins at 15.
  (Plan decision, deviating from the spec's "through lipgloss hex colors":
  lipgloss v1's profile can only be forced in tests via `termenv` types, so the
  256/16 downsample is implemented in `internal/ui` as a pure function instead.
  Behavior is identical to the spec.)
- New code goes in new files. `internal/console/model.go` (1050 lines) and
  `cmd/axis/agent_console.go` (1174 lines) and `cmd/axis/agent.go` (915 lines)
  get minimal, surgical edits only; a hook ratchets files over 500 lines.
- Exact copy (use verbatim):
  - Tips, Full tier: `Tips for getting started:` /
    `1. Ask about the fleet, models, or placement.` /
    `2. /help for commands, /model to switch models.` /
    `3. ! runs a shell command, @ completes a node name.`
  - Tips, Medium tier: `1. Ask about the fleet or models.` / `2. /help · /model`
  - Compact tier: `▲ AXIS v<ver>` (ASCII `^ AXIS v<ver>`) / `/help for commands`
  - Version beside the Full logo: `agent · v<ver>`
  - Advisory (unchanged text): `advisory: agent output is not cluster truth — it uses tools to read the fact plane`
  - Status: `finding a model…`, `✓ <model> ready` (ASCII `ok <model> ready`),
    `connecting MCP…`, `<n> MCP servers` / `1 MCP server`,
    `MCP: <k> of <n> failed (<names>)`, `loading cluster context…`,
    `snapshot <age> ago`, `no snapshot`
  - No model (footer and status): `no model · /model or axis model start`
  - No-model notice on prompt submit: `no model available — start one with axis model start <name>, or configure ~/.axis/ai.yaml`
  - Held prompt line: `waiting for model…`
  - Command before the agent is bound: `agent still starting — try again in a moment`
  - Scrollback header on dismissal: `▲ AXIS agent · v<ver> · <model>` (ASCII `^`;
    `no model` when none)
- Numbers: Full tier `width ≥ 72 && height ≥ 16`; Medium `44 ≤ width < 72 &&
  height ≥ 16`; Compact otherwise. Height `0` means unknown and counts as tall.
  Spinner interval stays `120ms`. Unicode frames `─ ╲ │ ╱`; ASCII frames
  `- \ | /`. Timeouts: runtime load `5s`, MCP connect `10s`; model resolution
  keeps its existing internal timeouts. Shutdown waits at most `3s` for the
  startup job.
- No rendered start-screen or footer line may exceed `width - 1` display columns.

## Review Focus

Inputs the spec implies that ordinary tests miss, most likely first. Each has a
test in the owning task.

1. **Quit during startup** while an MCP server or the model probe hangs: the
   program exits promptly, the job is cancelled, MCP connections close.
   → Task 7 `TestConsoleStartup_QuitWhileMCPHangs`.
2. **Operator picks a model with `/model` while background resolution is
   still running:** the operator's choice must stick; the late startup result is
   discarded. → Task 7 `TestConsoleStartup_PickerBeatsResolution`.
3. **No `WindowSizeMsg` yet / 0×0 size** (some SSH and multiplexer setups):
   renders at the default width with height treated as tall; no panic, no empty
   screen. → Task 3 `TestHeroScreen_UnknownHeight`.
4. **Repeated resizes across tiers during startup:** no line over `width-1`, and
   only one spinner tick chain runs (no speed-up). → Task 4
   `TestStartScreen_ResizeAcrossTiers`, Task 2 `TestEnsureTick_SingleChain`.
5. **Several prompts typed while the model is pending:** each is sent exactly
   once, in order, after the model is ready; none is lost or duplicated when a
   slash command is running at the moment the model arrives. → Task 5
   `TestHeldPrompts_OrderAndOnce`, `TestHeldPrompts_ReleasedAfterBusyTurn`.

---

## Execution Protocol: how the builder and reviewer work together

Execution method: **subagent-driven** (operator's choice, 2026-10-19), run with
`superpowers:subagent-driven-development`. This section is binding on that
skill. Where they disagree, this section wins, and the operator's stop triggers
in the Executor Prompt win over both.

### Roles

| Role | Who | Owns | Never |
|---|---|---|---|
| **Controller** | The operator's session | Worktree setup (Task 0), ledger, task briefs, dispatches, answering questions, rulings, checkpoint gates, manual matrix, final report | Writes product code or fixes findings itself (unreviewed changes); pushes; opens PRs |
| **Builder** | A fresh subagent per task | That task's tests (written first), code, commits, and report file | Reads other tasks' text; starts subagents or its own reviewer; edits outside the worktree; skips the failing-test step |
| **Task reviewer** | A fresh subagent per task, read-only | Two verdicts: spec compliance and quality | Edits files; reruns the builder's test suite without a specific reason; reviews code outside the task's diff |
| **Final reviewer** | A fresh subagent on the most capable model, read-only | Whole-branch review against spec, plan, and ledger | Edits files |
| **Second opinion** | An independent agent on a different model, reached through the operator's agent mailbox; read-only | An independent review of the final branch | Edits the worktree; blocks the run (bounded wait, below) |

Only one builder works at a time; they share one worktree. Tasks run strictly
in order 1→8. No two subagents ever write at the same time.

### Shared files (the only channel between roles)

The controller runs `scripts/sdd-workspace` against this plan from inside the
worktree. That gives
`.superpowers/sdd/2026-10-19-axis-start-screen/`, which git ignores. Everything
is handed over as a file path, never pasted:

| File | Written by | Read by |
|---|---|---|
| `progress.md` (ledger) | Controller only | Controller; final reviewer (deferred and parked items) |
| `task-N-brief.md` | Controller (`scripts/task-brief`) | Builder, task reviewer |
| `task-N-report.md` | Builder (appends one section per fix round) | Controller, task reviewer, later fix builders |
| review package (`scripts/review-package`) | Controller | Task reviewer, re-reviewer, final reviewer |

After compaction, the ledger and `git log` are the truth. A task with a
`Task N: complete` line is never run again.

### Builder report contract (`task-N-report.md`)

1. **Status:** `DONE`, `DONE_WITH_CONCERNS`, `NEEDS_CONTEXT`, or `BLOCKED`, with one line why.
2. **Red, then green:** the failing-test command with trimmed output showing
   the expected failure, then the same command passing. A test that never
   failed doesn't count as test-first.
3. **Commits:** `git log --oneline BASE..HEAD`.
4. **Interfaces produced:** the exact signatures as they landed, copied from
   the code, next to the task's Interfaces block. Any difference is marked
   `DIFFERS:` with the reason.
5. **Files touched, with line counts** (`wc -l`), for the 500-line hook.
6. **Hygiene:** artifacts restored; `git status --short` clean; `verify-public-boundary` passes.
7. **Deviations and concerns:** each with the plan line it departs from.

The builder sends the controller only the status, commit range, a one-line test
summary, and concerns. The rest stays in the file.

### Reviewer contract

The task reviewer gets three file paths: the brief, the report, and the review
package. It also gets the Global Constraints section copied word for word. It
returns:

- **Spec compliance:** ✅ or ❌ for each requirement and named test in the
  brief, with `file:line`.
- **Quality findings,** each `Critical`, `Important`, or `Minor`, with
  `file:line`, a concrete failure scenario, and the smallest fix. For this
  plan, quality includes the reliability checklist:
  - every goroutine, wait, and network call is bounded by a context or timeout
  - every error is handled and shown to the operator
  - every failure path has a test
  - no rendered line is wider than `width-1`
  - copy matches the exact strings
  - nothing private reaches the public repo
  - no new direct dependency
  - comments explain why
- **⚠️ Cannot verify from diff:** requirements that live in unchanged code or
  span tasks. The controller resolves each one before marking the task complete.

### The per-task loop

1. The controller records `BASE=$(git rev-parse HEAD)` and writes the brief.
   The dispatch carries:
   - one line on where the task fits
   - the brief path
   - the **interfaces that actually landed** in earlier tasks, taken from the
     ledger, not from the plan text
   - any rulings that affect this task
   - the report path
2. The builder asks questions before or during the work. The controller answers
   them. If a question hits a stop trigger, the controller asks the operator and
   the builder waits.
3. When the builder reports DONE, the controller checks three things: the
   commits exist, `git status` is clean, and the report has red and green
   evidence. Then it builds the review package for `BASE..HEAD` and dispatches
   the reviewer.
4. If both verdicts are clean, the controller writes the ledger entry
   `Task N: complete (commits a..b, review clean)` followed by
   `Interfaces landed: <signatures>`.
5. Otherwise the fix loop runs, at most 5 rounds:
   - Rounds 1–3 resume the same builder with the findings word for word.
   - Rounds 4–5 use a fresh builder one model tier up, which reads the report
     file for what was already tried.
   - Every round ends with a scoped re-review of the fix diff only.
   - Minor findings never enter the loop. They go in the ledger as
     `Task N: minor (deferred): …` for the final reviewer to sort.
6. When round 5 still leaves findings open, the controller judges each one
   against the spec. It either parks it with a ruling or makes the smallest
   ruling that unblocks the next task. It never drops one silently.

### Who decides what

- **The operator decides** everything in the Executor Prompt's "Stop and ask"
  list. The operator also approves anything outside the worktree, any push, and
  any PR. The controller stops, asks in plain numbered text, and waits.
- **The controller decides** everything else: a plan ambiguity, a
  reviewer–plan conflict, a builder's question the brief doesn't answer. Each
  decision goes in the ledger as
  `Ruling: <decision> — <why> — <cost if wrong>`. The spec settles behavior; the
  plan settles implementation detail.
- **Interface drift:** a signature that landed differently from its
  Interfaces block gets a ruling before the next task starts. Either the next
  brief adapts, or a fix round restores the planned signature. A later builder
  never finds out about the drift from a compile error.

### Model per seat

| Task | Builder | Reviewer | Why |
|---|---|---|---|
| 0 | controller | — | Setup only, no product code |
| 1, 2, 3 | sonnet | sonnet | Pure functions, fully specified, table tests |
| 4, 5 | sonnet | opus | Edits to the 1050-line `model.go`; held-prompt ordering is subtle |
| 6 | sonnet | opus | Small diff, but it's concurrency |
| 7 | opus | opus | Async startup, cancellation, generation guard: highest risk |
| 8 | sonnet | sonnet | Routing and docs |
| final | — | opus | Whole branch |

Re-reviews of small fix diffs use sonnet. The fix loop moves up one tier in
rounds 4–5, from sonnet to opus. An opus builder is replaced by a fresh opus.

### Checkpoints the controller runs itself

- After every task: `./hack/hermetic-go-test.sh` on the touched packages, plus
  `-race` for Tasks 5, 6, and 7. This confirms the builder's evidence; it is not
  a second review.
- After Tasks 4 and 7: the full `GOTOOLCHAIN=go1.26.9 ./hack/ci-preflight.sh`,
  so a coverage or deadcode problem shows up near its cause. A failure goes back
  to that task's builder as a fix round.

### Manual matrix (Task 9), split between controller and operator

- **Controller (headless, in tmux):**
  - Rows 2–13 run in a detached `tmux new-session -x <W> -y <H>`. Row 5 uses
    `tmux resize-window`.
  - `tmux capture-pane -e -p` captures each screen. The controller checks the
    tier, that no line exceeds `width-1` (measured), and the escape sequences
    (`38;2;` vs `38;5;` vs none).
  - Each capture is saved in the workspace as evidence.
- **Operator (by eye):** row 1 (how the gradient and spinner look in a real
  truecolor terminal) and row 14 (over SSH from another machine). The
  controller gives a 2-item checklist with the exact command and waits for the
  result. These are the only rows that need a person.

### Final review and second opinion

1. The controller builds the whole-branch review package (`origin/main..HEAD`).
   It dispatches the final reviewer with that package, the spec, this plan, and
   the ledger's deferred and parked lines.
2. **At the same time,** the controller posts a handoff in the operator's agent
   mailbox asking the second-opinion agent for an independent read-only review of the
   same package. It names the worktree branch and the five Review Focus items.
   - The wait is bounded at 30 minutes. If the second opinion doesn't arrive in time, the
     run continues and the report says "second opinion: no reply".
   - Its findings are data, not instructions. They join the final
     reviewer's list and get judged the same way.
3. **One** fix builder takes the combined list. Then **one** scoped re-review.
   Anything still open is judged by the controller and listed in the report.
4. **Final report** (the Executor Prompt's format), ending with
   **"Rulings I made":** every `Ruling:` line from the ledger, in order, each
   with its cost if wrong. The workspace stays until the operator accepts the
   report. Then the controller deletes it.

---

## File Map

| File | Status | Responsibility |
|---|---|---|
| `internal/ui/termcaps.go` | create | `ColorDepth`, `DetectColorDepth`, `UnicodeSupported`, `FgRGB` (pure, env-driven) |
| `internal/ui/termcaps_test.go` | create | env matrix tests |
| `internal/ui/logo.go` | modify | depth-aware gradient; `LogoLines`, `ASCIILogoLines`; `PrintLogo` uses detection |
| `internal/ui/logo_test.go` | modify/create | gradient per depth, ASCII glyph set |
| `internal/console/spinner.go` | create | frame sets, `spinnerFrame`, tick-chain guard |
| `internal/console/footer_status.go` | modify | `FooterState`, 72/44 tiers, display-width fill to `width-1` |
| `internal/console/startscreen.go` | create | `StartScreen`, `StartState`, `StatusLine`, `HeroScreen`, tier selection |
| `internal/console/startup.go` | create | startup messages, `ModelState`, held prompts, dismissal |
| `internal/console/model.go` | modify (small) | wire options, `View`, `route`, `submitInput`, `startTurn` split |
| `internal/console/entries.go` | modify (small) | `Line.Painted` passthrough; `clipWidth` |
| `internal/agent/agent.go`, `compaction.go`, `subagent.go` | modify (small) | race-safe backend accessors |
| `cmd/axis/agent_console_startup.go` | create | async console startup job, launcher binding, generation guard |
| `cmd/axis/agent_console.go` | modify (small) | launcher reads bound agent; shares program builder |
| `cmd/axis/agent.go` | modify (small) | route console path to the new startup; flags struct |
| `cmd/axis/main.go` | modify | bare `axis` → agent |
| docs (`README.md`, `docs/decisions/agent-surface-transcript-model.md`, `docs/current-state.md`, `CHANGELOG.md`) | modify | front-door and footer-tier truth |

---

### Task 0: Worktree, baseline, and spec/plan commit

**Files:** none of the product. Copies spec and plan into the branch.

- [ ] **Step 1: Create the worktree from current `origin/main`**

From the operator's main AXIS checkout (call it `$MAIN`; it holds the untracked
spec and plan this task copies):

```bash
cd "$MAIN" && git fetch origin
git worktree add -b feat/agent-start-screen ../axis-start-screen origin/main
cd ../axis-start-screen
```
Expected: `git log --oneline -1` shows the current `origin/main` head.

- [ ] **Step 2: Baseline gates (record results; all must pass before any edit)**

Run: `GOTOOLCHAIN=go1.26.9 ./hack/ci-preflight.sh`
Expected: exit 0. Then restore artifacts (Global Constraints). If anything fails here, stop and report: it is not caused by this work.

- [ ] **Step 3: Commit spec and plan**

```bash
mkdir -p docs/superpowers/specs docs/superpowers/plans
cp "$MAIN"/docs/superpowers/specs/2026-10-19-axis-start-screen-design.md docs/superpowers/specs/
cp "$MAIN"/docs/superpowers/plans/2026-10-19-axis-start-screen.md docs/superpowers/plans/
./hack/verify-public-boundary.sh
git add docs/superpowers/specs/2026-10-19-axis-start-screen-design.md docs/superpowers/plans/2026-10-19-axis-start-screen.md
git commit -m "docs: agent start screen design and implementation plan"
```

---

### Task 1: Terminal capabilities and depth-aware logo (`internal/ui`)

**Files:**
- Create: `internal/ui/termcaps.go`, `internal/ui/termcaps_test.go`
- Modify: `internal/ui/logo.go` (and its test file)

**Interfaces:**
- Produces:
  - `type ColorDepth int` with `ColorNone`, `Color16`, `Color256`, `ColorTrue` (in that order, `ColorNone == 0`).
  - `func DetectColorDepth(getenv func(string) string) ColorDepth`
  - `func UnicodeSupported(getenv func(string) string) bool`
  - `func FgRGB(r, g, b uint8, d ColorDepth) string` — SGR prefix; `""` for `ColorNone`.
  - `func LogoLines(d ColorDepth) []string` — the existing 6-row logo; painted per depth, plain for `ColorNone`; every row ends with a reset when painted.
  - `func ASCIILogoLines() []string` — 5-row `#` logo below.
  - `func LogoWidth() int` (27) and `func ASCIILogoWidth() int` (23).
  - `PrintLogo(w, version)` keeps its signature and now uses `DetectColorDepth(os.Getenv)` gated by `Enabled()`, and `ASCIILogoLines()` when `!UnicodeSupported(os.Getenv)`.

Detection rules (decide nothing else):
- `DetectColorDepth`: `NO_COLOR` non-empty → None; `TERM` empty or `dumb` → None;
  `COLORTERM` ∈ {`truecolor`, `24bit`} → True; `TERM` ∈ {`xterm-ghostty`,
  `xterm-kitty`, `wezterm`} or has suffix `-direct` → True; `TERM` contains
  `256color` → 256; otherwise 16.
- `UnicodeSupported`: `TERM` ∈ {`linux`, `dumb`} → false; locale = first
  non-empty of `LC_ALL`, `LC_CTYPE`, `LANG`; empty → true; `C` or `POSIX` →
  false; contains `utf-8` or `utf8` (case-insensitive) → true; else false.
- `FgRGB`: True → `\033[38;2;R;G;Bm`; 256 → `\033[38;5;Nm` with
  `N = 16 + 36*q(r) + 6*q(g) + q(b)`, `q(x) = round(x/255*5)`; 16 → `\033[9Km`
  with `K = (r>127?1:0) | (g>127?2:0) | (b>127?4:0)`; None → `""`.

ASCII logo rows (exact, letters A X I S joined by one space):
```
 ###  #   # #####  ####
#   #  # #    #   #    
#####   #     #    ### 
#   #  # #    #       #
#   # #   # ##### #### 
```

- [ ] **Step 1: Write failing tests** in `termcaps_test.go` and the logo test file:

```go
func TestDetectColorDepth(t *testing.T) {
	cases := []struct{ name string; env map[string]string; want ColorDepth }{
		{"no_color wins", map[string]string{"NO_COLOR": "1", "COLORTERM": "truecolor", "TERM": "xterm-256color"}, ColorNone},
		{"dumb", map[string]string{"TERM": "dumb"}, ColorNone},
		{"empty term", map[string]string{}, ColorNone},
		{"colorterm truecolor", map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, ColorTrue},
		{"ghostty", map[string]string{"TERM": "xterm-ghostty"}, ColorTrue},
		{"direct suffix", map[string]string{"TERM": "xterm-direct"}, ColorTrue},
		{"apple terminal 256", map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "Apple_Terminal"}, Color256},
		{"tmux 256", map[string]string{"TERM": "tmux-256color"}, Color256},
		{"linux vt", map[string]string{"TERM": "linux"}, Color16},
	}
	// table loop with getenv := func(k string) string { return tc.env[k] }
}

func TestUnicodeSupported(t *testing.T) {
	// linux→false, dumb→false, LC_ALL=C→false, LANG=POSIX→false,
	// LC_ALL=C.UTF-8→true, LANG=en_US.utf8→true, LANG=en_US.ISO-8859-1→false,
	// all empty with TERM=xterm-256color→true, LC_ALL beats LANG (LC_ALL=C, LANG=en_US.UTF-8 → false)
}

func TestFgRGB(t *testing.T) {
	// ColorTrue (0,240,255)  == "\x1b[38;2;0;240;255m"
	// Color256 (0,240,255)  == "\x1b[38;5;51m"   // q = 0,5,5 → 16+0+30+5
	// Color256 (185,30,255) == "\x1b[38;5;171m"  // q = 4,1,5 → 16+144+6+5
	// Color16 (0,240,255) == "\x1b[96m"; Color16 (185,30,255) == "\x1b[95m"; ColorNone == ""
}

func TestLogoLines_Depths(t *testing.T) {
	// ColorNone: lines equal the plain logo rows, no "\x1b".
	// Color256: no line contains "38;2;"; at least one contains "38;5;".
	// ColorTrue: contains "38;2;". Every painted line ends with "\x1b[0m".
}

func TestASCIILogoLines(t *testing.T) {
	// 5 rows; every rune is '#' or ' '; LogoWidth()==27; ASCIILogoWidth()==23
}
```
- [ ] **Step 2: Run, verify failure**

Run: `./hack/hermetic-go-test.sh ./internal/ui -run 'TestDetectColorDepth|TestUnicodeSupported|TestFgRGB|TestLogoLines_Depths|TestASCIILogoLines' -count=1`
Expected: FAIL (undefined symbols).

- [ ] **Step 3: Implement** `termcaps.go` per the rules above, and refactor `logo.go`: keep the diagonal gradient math (`t = 0.75*col + 0.25*row`, cyan `0,240,255` → magenta `185,30,255`), but emit color through `FgRGB`. `PrintLogo` behavior on a truecolor terminal is byte-identical to today (assert in a test with `ColorTrue`).

- [ ] **Step 4: Run tests — PASS**, plus `./hack/hermetic-go-test.sh ./internal/ui ./cmd/axis -count=1` (callers of `PrintLogo` still pass).

- [ ] **Step 5: Commit** — `feat(ui): detect color depth and Unicode support; depth-aware logo gradient`

---

### Task 2: One spinner, and a footer that fits (`internal/console`)

**Files:**
- Create: `internal/console/spinner.go`, `internal/console/spinner_test.go`
- Modify: `internal/console/footer_status.go`, `internal/console/footer_status_test.go`, `internal/console/entries.go` (add `clipWidth`), `internal/console/model.go` (replace `spinnerFrames[...]` reads; footer call), any test fake implementing `Footer`

**Interfaces:**
- Produces:
  - `func spinnerFrame(i int, unicode bool) string` — `─ ╲ │ ╱` or `- \ | /`, `i` taken modulo 4.
  - `func (m *Model) ensureTick() tea.Cmd` — returns `spinnerTick()` and sets `m.ticking = true` only if not already ticking; otherwise `nil`. The `spinnerTickMsg` handler sets `m.ticking = false` and returns no new tick when `!m.animating()`.
  - `func (m Model) animating() bool` — Task 2 version: `m.state != turnIdle`. Tasks 4–5 extend it.
  - `type FooterState struct { Frame string; Model ModelState }` and the interface becomes `Footer interface { Render(width int, st FooterState) []Line }`. (`ModelState` is defined in Task 5; in this task define it in `startup.go` with only `ModelReady ModelState = iota` and add `ModelPending`, `ModelNone` now too, so the type is stable.)
  - `func clipWidth(s string, w int) string` in `entries.go` — truncates by display width (sum of `lipgloss.Width(string(r))`), never splitting a wide rune.
  - `Options.Unicode bool` and `Model.unicode` — selects spinner frames. Zero value keeps today's ASCII frames, so existing tests stay valid.

Footer tiers (replacing 80/60/40): `width ≥ 72` → today's ≥80 content; `44 ≤ width < 72` → `model: <m>`, `ctxCompact`, `snap`; `< 44` → `model: <m>` only. Model segment by state: Ready → `model: <name>`; Pending → `model: <Frame>`; None → `no model · /model or axis model start`. Fill with `─` to exactly `width-1` display columns; truncate with `clipWidth` to `width-1`. Replace `utf8.RuneCountInString` with `visibleWidth`.

- [ ] **Step 1: Write failing tests**

```go
func TestSpinnerFrame(t *testing.T) {
	want := []string{"─", "╲", "│", "╱"}
	for i, w := range want { if got := spinnerFrame(i, true); got != w { t.Fatalf(...) } }
	if spinnerFrame(5, false) != "\\" { t.Fatal(...) }
}
func TestEnsureTick_SingleChain(t *testing.T) {
	m := NewModel(Options{})
	if m.ensureTick() == nil { t.Fatal("first call must start a tick") }
	if m.ensureTick() != nil { t.Fatal("second call must not start another chain") }
}
func TestStatusFooter_Tiers(t *testing.T) {
	// For widths 30, 43, 44, 71, 72, 120 with Model "example-model", Ready:
	// visibleWidth(line.Text) == width-1 exactly (filled), and segments:
	// 72,120 contain "context: [" ; 44,71 contain "context: " and "snap"/"no snapshot" but not "["; 30,43 contain only "model: example-model" (clipped).
}
func TestStatusFooter_ModelStates(t *testing.T) {
	// Pending with Frame "╲" → contains "model: ╲"; None → contains "no model · /model or axis model start" at width 120.
}
func TestStatusFooter_WideRunesNeverOverflow(t *testing.T) {
	// Model name "模型-example" (wide runes) at widths 20..120: visibleWidth(text) <= width-1.
}
```

- [ ] **Step 2: Run, verify failure** — `./hack/hermetic-go-test.sh ./internal/console -run 'TestSpinnerFrame|TestEnsureTick|TestStatusFooter_' -count=1` → FAIL.

- [ ] **Step 3: Implement.** In `model.go`: the three `spinnerFrames[m.spinner]` reads become `spinnerFrame(m.spinner, m.unicode)`; `m.spinner` advances modulo 4; `startTurn`'s `spinnerTick()` becomes `m.ensureTick()`; footer call becomes `m.footer.Render(m.width, FooterState{Frame: spinnerFrame(m.spinner, m.unicode), Model: m.modelState})` (field added in Task 5; until then pass `ModelReady`). Update every `Footer` test fake to the new signature. Update `docs/decisions/agent-surface-transcript-model.md` footer truncation list to the 72/44 tiers in the same commit.

- [ ] **Step 4: Run** `./hack/hermetic-go-test.sh ./internal/console ./cmd/axis -count=1` → PASS. If an existing test asserts the old 80/60/40 cut points or full-width fill, update it to the new contract and name it in the commit body.

- [ ] **Step 5: Commit** — `feat(console): single minimal spinner; footer tiers at 72/44 with display-width fit`

---

### Task 3: The Option B start screen (`internal/console`)

**Files:**
- Create: `internal/console/startscreen.go`, `internal/console/startscreen_test.go`
- Modify: `internal/console/entries.go` (`Line.Painted`)

**Interfaces:**
- Consumes: `ui.LogoLines`, `ui.ASCIILogoLines`, `ui.ColorDepth` (Task 1); `spinnerFrame`, `clipWidth` (Task 2).
- Produces:
  - `type StartScreen interface { Render(width, height int, s StartState) []Line }`
  - `type StartState struct { Version string; Unicode bool; Depth ui.ColorDepth; Frame string; Status []StatusLine }`
  - `type StatusState int` — `StatusInfo` (0), `StatusRunning`, `StatusDone`, `StatusFailed`.
  - `type StatusLine struct { Key string; State StatusState; Text string; Persist bool }` — `Persist` lines are printed to scrollback on dismissal (Task 4).
  - `type startTier int` — `tierCompact`, `tierMedium`, `tierFull`; `func pickTier(width, height int) startTier` per Global Constraints (height 0 = tall).
  - `type HeroScreen struct{}` implementing `StartScreen`.
  - `Line.Painted string` — when non-empty, `Paint` returns it verbatim (it already carries SGR); `Plain()` and `Width()` keep using `Text`. Logo lines set `Text` to the plain row and `Painted` to the colored row.

Layout (top to bottom):
- Full: 6 logo rows (ASCII mode: 5 ASCII rows), the last row followed by two spaces and `agent · v<ver>` (muted); blank; the 4 tip lines (Full copy); blank; status lines; blank.
- Medium: logo rows; `agent · v<ver>` on its own line; blank; 2 tip lines (Medium copy); blank; status lines; blank.
- Compact: `▲ AXIS v<ver>` (`^` in ASCII; `▲ AXIS` painted cyan via `FgRGB(0,240,255,depth)`); `/help for commands` (muted); at most one status line: the first `StatusFailed`, else the first `StatusRunning`, clipped.
- Status line rendering: Running → `<Frame> <Text>` muted; Done → `✓ <Text>` (ASCII `ok <Text>`) with the mark in `StyleGood`; Failed → `<Text>` in `StyleBad`; Info → `<Text>` muted. Every line is clipped to `width-1`.

- [ ] **Step 1: Write failing tests**

```go
func TestPickTier(t *testing.T) {
	// (120,40)=full (72,16)=full (71,40)=medium (44,16)=medium (43,40)=compact (120,15)=compact (80,0)=full (30,0)=compact
}
func TestHeroScreen_Full(t *testing.T) {
	lines := HeroScreen{}.Render(100, 40, StartState{Version: "0.19.6", Unicode: true})
	plain := PlainAll(lines)
	// contains "agent · v0.19.6", "Tips for getting started:", "3. ! runs a shell command, @ completes a node name."
	// first 6 lines' Text equal ui.LogoLines(ui.ColorNone)
}
func TestHeroScreen_Medium(t *testing.T) { /* width 60: contains "2. /help · /model", not "Tips for getting started:" */ }
func TestHeroScreen_Compact(t *testing.T) { /* width 40: first line "▲ AXIS v0.19.6"; Unicode=false → "^ AXIS v0.19.6"; no '█' anywhere */ }
func TestHeroScreen_ASCIIFull(t *testing.T) { /* Unicode=false, width 100: no rune > 0x7E in any Text except none; logo rows equal ui.ASCIILogoLines() */ }
func TestHeroScreen_NeverExceedsWidth(t *testing.T) {
	// widths 20..140 × heights {0,10,15,16,40}, with a 200-rune status Text and a wide-rune model name:
	// every line: l.Width() <= width-1
}
func TestHeroScreen_UnknownHeight(t *testing.T) { /* (80,0) renders Full tier, no panic */ }
func TestHeroScreen_StatusStates(t *testing.T) {
	// Running with Frame "╲" → "╲ finding a model…"; Done → "✓ example-model ready"; ASCII Done → "ok example-model ready"; Failed text present
}
func TestHeroScreen_NoColorHasNoEscapes(t *testing.T) { /* Depth ColorNone: PaintAll(lines) has no "\x1b[38;" */ }
func TestHeroScreen_256HasNoTruecolor(t *testing.T) { /* Depth Color256: PaintAll(lines) has no "38;2;" */ }
```
Note: `PaintAll` may still emit dim/bold SGR from `ui.Dim` when color is enabled; the no-color assertion targets foreground color codes. Call `ui.Init(true)` in the no-color test to disable `fatih/color` and assert no `"\x1b["` at all.

- [ ] **Step 2: Run, verify failure** — `./hack/hermetic-go-test.sh ./internal/console -run 'TestPickTier|TestHeroScreen_' -count=1` → FAIL.
- [ ] **Step 3: Implement** `startscreen.go` and `Line.Painted`.
- [ ] **Step 4: Run** the same command → PASS; then `./hack/hermetic-go-test.sh ./internal/console -count=1` → PASS (golden tests untouched by `Painted`).
- [ ] **Step 5: Commit** — `feat(console): Option B start screen with responsive tiers and ASCII fallback`

---

### Task 4: Start screen in the console lifecycle (`internal/console`)

**Files:**
- Create/extend: `internal/console/startup.go`, `internal/console/startup_test.go`
- Modify: `internal/console/model.go` (Options, `NewModel`, `Init`, `route`, `View`, top of `submitInput`)

**Interfaces:**
- Consumes: Task 3 types; `ensureTick` (Task 2).
- Produces:
  - `Options` fields: `Start StartScreen` (nil = no start screen; keeps every existing test as is), `Version string`, `Depth ui.ColorDepth`, `InitialStatus []StatusLine`, `HeaderModel func() string`.
  - `type StartupStatusMsg struct { Line StatusLine }` — upserts by `Key` (replace in place, else append). After dismissal, a `Persist` line is committed immediately as a `NoticeEntry` (or `ErrorEntry` if `StatusFailed`); non-persist lines are dropped.
  - `func (m Model) startVisible() bool` — `m.start != nil && !m.startDismissed`.
  - `func (m Model) dismissStart() (Model, tea.Cmd)` — sets `startDismissed`; commits one `NoticeEntry` with the header line (`▲ AXIS agent · v<ver> · <HeaderModel()>`, `no model` if it returns "" or state is `ModelNone`), then one notice per `Persist` status line in order. Called at the top of `submitInput` for any non-empty text, exactly once.
  - `animating()` becomes: turn not idle, or `startVisible()` with any `StatusRunning` line, or (Task 5) model pending or held prompts.
  - `Init` returns `tea.Batch(title, m.ensureTick())` when `animating()`.
  - `View`: when `startVisible()`, prepend `m.start.Render(m.width, m.height, m.startState())` lines.
  - Prompt rule (spec §3): when `m.start != nil` (the new startup path only, so existing golden tests are unchanged), `View` draws one muted rule of `─` (ASCII `-`) at `width-1` columns directly above the input line, on every frame, before and after dismissal. The footer below the input already reads as the bottom rule. The input line itself keeps its `> ` gutter (`❯ ` when Unicode) and never gets a right-hand border.

- [ ] **Step 1: Write failing tests**

```go
func TestStartScreen_VisibleUntilFirstSubmit(t *testing.T) {
	// Model with Start: HeroScreen{}, Version "0.19.6"; View contains "agent · v0.19.6".
	// Type "/usage" + Enter → View no longer contains it; the returned cmds print exactly one header line "▲ AXIS agent · v0.19.6 · example-model".
}
func TestStartScreen_PersistLinesFlushedOnce(t *testing.T) {
	// InitialStatus: advisory (Persist), cwd (no Persist). Submit twice. Header + advisory printed once total; cwd never printed.
}
func TestStartScreen_StatusUpsert(t *testing.T) {
	// Send StartupStatusMsg{Key:"model", Running, "finding a model…"} then {Key:"model", Done, "example-model ready"}: View has one model line, "✓ example-model ready".
}
func TestStartScreen_PersistAfterDismissCommitsNotice(t *testing.T) {
	// Dismiss, then StartupStatusMsg{Persist:true, Text:"Using model example-model instead"} → committed as notice; View unchanged.
}
func TestStartScreen_ResizeAcrossTiers(t *testing.T) {
	// Sequence of WindowSizeMsg: 120x40, 60x40, 30x10, 120x40, 0x0 while a Running status exists;
	// after each, every View line (split on "\n", ANSI stripped) has display width <= width-1 (use 80 for 0);
	// ticking stays true and only one tick cmd was ever started (count ensureTick non-nil returns via a test hook or by asserting m.ticking transitions).
}
func TestStartScreen_PromptRule(t *testing.T) {
	// Start set, width 80, Unicode: the line directly above the input is 79 × "─"; after dismissal it is still there;
	// Unicode=false → 79 × "-" and gutter "> "; Unicode=true → gutter "❯ ". Model without Start: no rule line (existing behavior).
}
func TestStartScreen_SpinnerStopsWhenNothingRuns(t *testing.T) {
	// All status Done, idle: spinnerTickMsg → no new tick cmd, ticking=false.
}
```
Use the existing test helpers in `model_test.go` for driving key presses and collecting printed lines; do not invent a parallel harness.

- [ ] **Step 2: Run, verify failure** — `./hack/hermetic-go-test.sh ./internal/console -run 'TestStartScreen_' -count=1` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `./hack/hermetic-go-test.sh ./internal/console -count=1` → PASS.
- [ ] **Step 5: Commit** — `feat(console): show the start screen until the first submit; startup status lines`

---

### Task 5: Hold prompts until a model is ready (`internal/console`)

**Files:**
- Modify: `internal/console/startup.go`, `internal/console/startup_test.go`, `internal/console/model.go` (`submitInput`, `startTurn` split, `finishTurn` tail, `requestCancel` head, `View`, `route`)

**Interfaces:**
- Produces:
  - `type ModelState int` — `ModelReady` (0, default; keeps existing behavior), `ModelPending`, `ModelNone`.
  - `Options.InitialModelState ModelState`.
  - `type ModelStateMsg struct { State ModelState }`.
  - `type RunCommandMsg struct { Text string }` — handled as if the operator submitted `Text` (used for `-s` to open `/model` at startup).
  - `func isCommand(text string) bool` — has prefix `/` or `!`.
  - `func (m Model) beginTurn(text string) (Model, tea.Cmd)` — today's `startTurn` minus the user-entry commit; `startTurn` = commit user entry + `beginTurn`.
  - `func (m Model) releaseHeld() (Model, tea.Cmd)` — if `modelState == ModelReady`, idle, and `len(held) > 0`: pop `held[0]`, `beginTurn` it. Called from `ModelStateMsg` handling and at the end of `finishTurn` when no queued prompt started.

Rules:
- `submitInput`, after dismissal and console-local commands: if `!isCommand(text)`:
  - `ModelPending`: commit the user entry, append to `held`, return (persist to history as usual).
  - `ModelNone`: commit the user entry and the no-model notice (exact copy); do not send.
- `ModelStateMsg{ModelNone}` with held prompts: commit the no-model notice once, clear `held`.
- `requestCancel` when idle and `len(held) > 0`: clear `held`, commit notice `cancelled: <n> waiting prompt(s)`; do not count toward the quit gesture.
- `View`: when `len(held) > 0`, add `<frame> waiting for model…` (muted) above the input line.

- [ ] **Step 1: Write failing tests**

```go
func TestHeldPrompts_OrderAndOnce(t *testing.T) {
	// InitialModelState Pending; Submit records calls. Enter "first", "second".
	// Submit not called; View has "waiting for model…".
	// ModelStateMsg{Ready} → Submit called once with "first"; TurnDoneMsg → called with "second"; total calls 2; user entries committed exactly twice (once each).
}
func TestHeldPrompts_CommandsBypass(t *testing.T) { /* Pending: "/status" and "!ls" reach Submit immediately */ }
func TestHeldPrompts_ReleasedAfterBusyTurn(t *testing.T) {
	// Pending; "/status" running (turn busy); type "hello" (held); ModelStateMsg{Ready} while busy → not sent;
	// TurnDoneMsg for the /status turn → "hello" sent exactly once.
}
func TestHeldPrompts_NoModel(t *testing.T) {
	// Pending, "hello" held; ModelStateMsg{None} → notice "no model available — start one with axis model start <name>, or configure ~/.axis/ai.yaml" once; held empty; Submit never called.
	// Then in None: "again" → user entry + same notice; Submit not called.
}
func TestHeldPrompts_CancelClears(t *testing.T) { /* Pending, two held; ctrl+c → notice "cancelled: 2 waiting prompt(s)"; held empty; program not quitting */ }
func TestHeldPrompts_PickerFixesNoModel(t *testing.T) { /* None → ModelStateMsg{Ready} → "hello" is sent normally */ }
func TestRunCommandMsg(t *testing.T) { /* RunCommandMsg{"/model"} → Submit called with "/model" */ }
```

- [ ] **Step 2: Run, verify failure** — `./hack/hermetic-go-test.sh ./internal/console -run 'TestHeldPrompts_|TestRunCommandMsg' -count=1` → FAIL.
- [ ] **Step 3: Implement.** Footer call now passes `m.modelState`.
- [ ] **Step 4: Run** `./hack/hermetic-go-test.sh -race ./internal/console -count=1` → PASS.
- [ ] **Step 5: Commit** — `feat(console): hold prompts while a model is being found; no-model notice`

---

### Task 6: Race-safe backend swap (`internal/agent`)

**Files:**
- Modify: `internal/agent/agent.go` (`SetBackend`, `Backend`, read at `:476`, `:726`), `internal/agent/compaction.go:171`, `internal/agent/subagent.go:167`
- Test: `internal/agent/agent_backend_race_test.go` (create)

**Interfaces:**
- Produces: `SetBackend(client ChatBackend, securityClass BackendSecurityClass)` and `Backend() ChatBackend` guarded by a new `clientMu sync.RWMutex`; add `func (a *Agent) SecurityClass() BackendSecurityClass` under the same lock if any read of `a.securityClass` happens outside construction. Every read of `a.client` / `a.securityClass` outside `New` goes through these accessors, read once per turn into a local.

Why: the console binds the agent before the model is known and swaps the backend from the startup goroutine (Task 7); `/compact` or a slash command may read it concurrently.

- [ ] **Step 1: Failing test** — `TestSetBackendConcurrentWithReads`: one goroutine calls `SetBackend` 1000 times with two fake backends, another calls `Backend()` and a compaction-path read 1000 times; run under `-race`.
- [ ] **Step 2:** `./hack/hermetic-go-test.sh -race ./internal/agent -run TestSetBackendConcurrentWithReads -count=1` → FAIL (race report).
- [ ] **Step 3: Implement.** `grep -n 'a\.client\|a\.securityClass' internal/agent/*.go` must show only accessor bodies and `New` afterwards.
- [ ] **Step 4:** `./hack/hermetic-go-test.sh -race ./internal/agent -count=1` → PASS.
- [ ] **Step 5: Commit** — `fix(agent): guard backend swaps for concurrent startup binding`

---

### Task 7: Paint first, start in the background (`cmd/axis`)

**Files:**
- Create: `cmd/axis/agent_console_startup.go`, `cmd/axis/agent_console_startup_test.go`
- Modify: `cmd/axis/agent_console.go` (launcher reads a bound agent; extract the program/footer/fleet construction from `runAgentConsole` into a helper both paths use), `cmd/axis/agent.go` (collect flags into `agentStartupInputs`; route the console path)

**Interfaces:**
- Consumes: everything above; existing `loadAgentShellRuntime`, `setupAgentStartupBackend`, `buildAgentSessionConfig`, `applyModelChoice`/`switchAgentToModelChoice`, `consoleConfirm`, `mcpclient.Registry`.
- Produces:
  - `type agentStartupInputs struct` holding every agent flag value the session needs (model, role, provider, cloudModel, cheapModel, systemMsg, maxTurns, maxTokens, timeout, autoApprove, autonomy, verbose, dryRun, allowRawCommandEvidence, resume, selectModel, live).
  - `func hasExplicitModelFlags(in agentStartupInputs) bool` — `model != "" || role != "" || cloudModel != "" || (provider != "" && provider != "auto")`.
  - `func runAgentConsoleStartup(ctx context.Context, in agentStartupInputs, errW io.Writer) (*agent.Agent, error)` — returns the bound agent (nil if never bound) so the agent command's deferred session summary still works.
  - Launcher additions: `func (l *consoleLauncher) bind(a *agent.Agent, mcpReg *mcpclient.Registry, target ModelChoice)` sets all agent-derived launcher fields under `l.mu`; every launcher read of those fields happens under `l.mu`. Before `bind`, `submit` answers any command with the notice `agent still starting — try again in a moment` and a `TurnDoneMsg`.
  - Generation guard: `l.modelGen uint64` (under `l.mu`). The `/model` switch increments it. `func (l *consoleLauncher) applyStartupModel(gen uint64, apply func() error) (applied bool, err error)` runs `apply` only if `gen` still matches.
  - Constants: `consoleRuntimeLoadTimeout = 5 * time.Second`, `consoleMCPConnectTimeout = 10 * time.Second`, `consoleStartupShutdownWait = 3 * time.Second`.

Routing in the agent command's `RunE` (surgical):
- If `printMode` or positional args or `routeAgentInteractive(...)` chooses the plain REPL → today's code, unchanged.
- Else (console path) → `runAgentConsoleStartup`.
  - With `hasExplicitModelFlags`: do today's synchronous prelude first (role lookup, `setupAgentStartupBackend`) with its `Out`/`ErrOut` captured into a buffer and `In` set so the stdin picker never runs (`-s` is handled in-console). Errors return exactly as today (exit 2 paths unchanged). Captured lines become `Persist` status lines.
  - Without: nothing slow runs before `prog.Run()`.

Startup job (one goroutine, started right before `prog.Run()`, owns a context cancelled when the program exits; every step reports via `prog.Send(console.StartupStatusMsg{...})`):
1. Runtime: `loader(ctx)` under `consoleRuntimeLoadTimeout` (or `runtimectx.LoadLive` when `--live`). Status `snapshot`: Done `snapshot <age> ago` / Info `no snapshot`. The load error, if any, becomes a `Persist` line (today's "Could not load cluster context" text without the `⚠`). Prime the fleet snapshot used by the footer and `@` completion here, not before paint.
2. MCP: if configured, status `mcp` Running `connecting MCP…`; `ConnectAll` under `consoleMCPConnectTimeout`; Done `<n> MCP servers` or Failed `MCP: <k> of <n> failed (<names>)`. Close the registry when the job's context ends.
3. Agent: build the session config exactly as today (pre-resolved backend if explicit flags, else `nil` backend placeholder), `agent.New`, resume (`Resumed <n> messages from previous session.` as a `Persist` line; failure as a Failed persist line), `SetConfirm(consoleConfirm(ctx, prog.Send, time.Now))`, then `launcher.bind(...)`. If pre-resolved: send `ModelStateMsg{Ready}` and model Done.
4. Model (only when not pre-resolved): status `model` Running `finding a model…`; capture `gen`; run `setupAgentStartupBackend` with `SelectModel: false`, `In: nil`, output captured; on success apply through `applyStartupModel(gen, ...)` using the same helper the `/model` switch uses after routing (no duplicated runner/provenance code) → model Done `<model> ready` + `ModelStateMsg{Ready}`; captured fallback notes become `Persist` lines. If not applied (operator switched): discard the result and send one Info line `kept /model choice`. On failure: model Failed `no model · /model or axis model start`, error text as a `Persist` failed line, `ModelStateMsg{None}`.
5. If `selectModel`: after bind, `prog.Send(console.RunCommandMsg{Text: "/model"})`.

The console `Options` for this path: `Start: console.HeroScreen{}`, `Unicode: ui.UnicodeSupported(os.Getenv)`, `Depth: ui.DetectColorDepth(os.Getenv)` (None when `!ui.Enabled()`), `Version: buildinfo.Version`, `InitialModelState: ModelPending` unless pre-resolved, `InitialStatus`: cwd line (`~`-relative path plus ` (<branch>)` from `internal/git` when in a repo), `loading cluster context…` Running, `finding a model…` Running (unless pre-resolved), the advisory as an Info `Persist` line. Nothing is written to `errW` before `prog.Run()` on this path.

After `prog.Run()` returns: cancel the job context; wait for the job up to `consoleStartupShutdownWait`; if it has not finished, write one line to `errW` (`startup still cancelling; exiting`) and return. Save the conversation if an agent was bound (today's behavior).

- [ ] **Step 1: Write failing tests** (stub `loadAgentShellRuntime`, MCP connect, and the backend resolver through existing package-level function variables; add a variable seam only where none exists, e.g. `var consoleSetupBackendFn = setupAgentStartupBackend`; drive the program with a fake `prog` that records `Send` calls — do not start a real TTY program):

```go
func TestHasExplicitModelFlags(t *testing.T) { /* table: zero→false; model; role; cloudModel; provider "local"→true; provider "auto"→false */ }
func TestConsoleStartup_NothingWrittenBeforePaint(t *testing.T) { /* errW buffer empty when the job starts; advisory arrives as Persist status */ }
func TestConsoleStartup_SlowResolutionDoesNotBlockPaint(t *testing.T) {
	// resolver blocks on a channel; job sends model Running before release; bind happens before resolution; release → ModelStateMsg{Ready} sent.
}
func TestConsoleStartup_NoModel(t *testing.T) { /* resolver returns ExitCodeError → ModelStateMsg{None}; model Failed text exact; function returns nil error */ }
func TestConsoleStartup_RuntimeLoadTimeout(t *testing.T) { /* loader blocks until ctx done → snapshot Info "no snapshot" within 5s+slack (use a shortened timeout via an injectable var in tests) */ }
func TestConsoleStartup_MCPFailureVisible(t *testing.T) { /* one of two servers fails → "MCP: 1 of 2 failed (srv-b)" */ }
func TestConsoleStartup_QuitWhileMCPHangs(t *testing.T) {
	// MCP connect blocks until ctx done; simulate program exit immediately; runAgentConsoleStartup returns within consoleStartupShutdownWait+1s; registry Close called.
}
func TestConsoleStartup_PickerBeatsResolution(t *testing.T) {
	// resolver blocked; operator /model switch increments gen and sets backend A; release resolver with backend B → agent.Backend() is A; "kept /model choice" status sent.
}
func TestConsoleStartup_ExplicitModelUnavailableExits2(t *testing.T) { /* --model missing → ExitCodeError{Code: ExitErrConfigLoad}; program never constructed */ }
func TestConsoleStartup_SelectOpensPicker(t *testing.T) { /* selectModel → RunCommandMsg{"/model"} sent after bind */ }
func TestLauncher_CommandBeforeBind(t *testing.T) { /* submit "/status" before bind → notice "agent still starting — try again in a moment" + TurnDoneMsg */ }
func TestLauncher_BindIsRaceFree(t *testing.T) { /* bind concurrently with submit of commands, under -race */ }
```

- [ ] **Step 2: Run, verify failure** — `./hack/hermetic-go-test.sh -race ./cmd/axis -run 'TestHasExplicitModelFlags|TestConsoleStartup_|TestLauncher_' -count=1` → FAIL.
- [ ] **Step 3: Implement.** Keep `runAgentConsole`'s signature working for existing tests (it becomes a thin wrapper that binds an already-built agent immediately); do not delete tests.
- [ ] **Step 4: Run** `./hack/hermetic-go-test.sh -race ./cmd/axis ./internal/... -count=1` → PASS.
- [ ] **Step 5: Commit** — `feat(agent): paint the console first and finish startup in the background`

---

### Task 8: Bare `axis` opens the agent; docs tell the truth

**Files:**
- Modify: `cmd/axis/main.go` (root `RunE` and its comment), `README.md` (line with `axis tui`), `docs/decisions/agent-surface-transcript-model.md` (`runTUI` sentence), `docs/current-state.md` (front-door behavior, if stated), `CHANGELOG.md` (`## Unreleased`), and `AGENTS.md` only if it states the bare-`axis` behavior (grep first)
- Test: `cmd/axis/main_bare_test.go` (create)

**Interfaces:**
- Produces: `var runBareAxis = func(cmd *cobra.Command) error` — finds the `agent` subcommand (`cmd.Find([]string{"agent"})`) and executes its `RunE` with default flags. Root `RunE`: TTY on stdin and stdout and no args → `runBareAxis(cmd)`; otherwise `cmd.Help()` (unchanged).

- [ ] **Step 1: Failing tests**

```go
func TestBareAxis_TTYRunsAgent(t *testing.T) { /* stub TTY check true via existing seam or a new var isInteractiveFn; runBareAxis stub records call; Execute with no args → called once */ }
func TestBareAxis_NonTTYPrintsHelp(t *testing.T) { /* TTY false → output contains "USAGE" and exit 0; runBareAxis not called */ }
func TestBareAxis_ArgsNeverRunAgent(t *testing.T) { /* "axis version" → runBareAxis not called */ }
func TestTUICommandStillRegistered(t *testing.T) { /* root.Find("tui") resolves; RunE is runTUI */ }
```
If no TTY seam exists in `main.go`, add `var isInteractiveFn = func() bool { return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd()) }`.

- [ ] **Step 2: Run, verify failure** — `./hack/hermetic-go-test.sh ./cmd/axis -run 'TestBareAxis_|TestTUICommandStillRegistered' -count=1` → FAIL.
- [ ] **Step 3: Implement** routing; update docs: README row becomes `| \`axis tui\` | Full-screen cluster dashboard |` plus a sentence near the command table: "On an interactive terminal, bare `axis` opens `axis agent`; piped or scripted, it prints help." CHANGELOG `## Unreleased` gets: "Bare `axis` on a terminal now opens `axis agent` with a new start screen (responsive logo, tips, live startup status); startup no longer blocks on model probes or MCP connections and opens even with no model available. `axis tui` still opens the dashboard."
- [ ] **Step 4: Run** `./hack/hermetic-go-test.sh ./cmd/axis -count=1 && ./hack/verify-doc-facts.sh && ./hack/verify-repo-truth.sh && ./hack/verify-public-boundary.sh` → all pass.
- [ ] **Step 5: Commit** — `feat(cli): bare axis opens the agent; docs updated`

---

### Task 9: Full gates, manual matrix, branch review

**Files:** none new (fixes only if a check fails, each as its own commit).

- [ ] **Step 1: Full gates** — `GOTOOLCHAIN=go1.26.9 ./hack/ci-preflight.sh` → exit 0. Restore artifacts. `git status --short` → clean.
- [ ] **Step 2: Build and run manually** — `make build`, then run `./axis` (not the installed binary) in each row and record pass/fail with a one-line note:

| # | Setup | Expect |
|---|---|---|
| 1 | Full-width terminal, truecolor | Full tier; gradient logo; spinner lines settle to ✓ |
| 2 | tmux 50/50 split of a ≥146-col window | Full tier at ≥72 cols |
| 3 | 40-col pane | Compact tier, one line wordmark |
| 4 | 14-row pane | Compact tier |
| 5 | Resize live 120→50→30→120 on the start screen | Redraws in place; no ghost lines in scrollback |
| 6 | `TERM=xterm-256color COLORTERM=` | 256-color gradient (`38;5;` only) |
| 7 | `NO_COLOR=1` | No color |
| 8 | `LC_ALL=C` | ASCII logo, `- \ | /` spinner, `^ AXIS` |
| 9 | No model server running (`AXIS_HOME` pointed at a temp dir with a nodes.yaml that lists no models) | Opens; footer `no model · …`; prompt gives the notice; `/help` works |
| 10 | Type a prompt immediately after launch | Shows `waiting for model…`, sends itself once ready |
| 11 | `echo | ./axis` and `./axis > /tmp/out` | Help text, exit 0 |
| 12 | `./axis tui` | Dashboard unchanged |
| 13 | `./axis agent --model does-not-exist` | Exits 2 with today's message |
| 14 | Over SSH (`ssh -t <host> ./axis`) | Same as row 1 for that terminal |

For row 9, use a temporary `AXIS_HOME`, never the operator's real `~/.axis`.

- [ ] **Step 3: Whole-branch review** — per "Execution Protocol → Final review and second opinion": final reviewer (opus) plus the second-opinion agent in parallel on `origin/main..HEAD`; one fix wave with tests, one commit per finding; one scoped re-review.
- [ ] **Step 4: Final report** per the Executor Prompt. Do not push, do not open a PR.

---

## Self-Review (completed by the plan author)

- **Spec coverage:** routing (T8), layout and tiers (T3), prompt rules/no right border (existing editor line has no border; T3 adds none — confirmed by `TestHeroScreen_*` not drawing a box), resize/dismissal (T4), prompt rule above the input with no right border (T4), color and glyphs (T1, T3), footer alignment and width math (T2), startup ordering, MCP, model, no-model, no-snapshot, explicit flags, pre-console output (T7), minimal spinner (T2, T4, T5), pluggable start screen (T3), docs (T8), verification (T9).
- **Type consistency:** `ModelState`, `FooterState`, `StatusLine`, `StartupStatusMsg`, `ModelStateMsg`, `RunCommandMsg`, `spinnerFrame`, `ensureTick`, `clipWidth`, `pickTier`, `HeroScreen`, `runAgentConsoleStartup`, `agentStartupInputs`, `hasExplicitModelFlags`, `bind`, `applyStartupModel`, `runBareAxis` are each defined once and used with the same names later.
- **Spec deviation recorded:** color downsample implemented in `internal/ui` instead of through lipgloss (no-new-dependency and testability; see Global Constraints). Slash commands before the agent is bound (bounded by runtime load + MCP connect, ≤15s worst case) get a "still starting" notice; they never wait on model resolution, which is the long step.
- **Execution protocol:** roles, files passed between them, report and review contracts, fix-loop limits, who decides what, model per seat, checkpoints, the manual matrix split, and the final review are fixed in "Execution Protocol". The operator's stop triggers override the skill's "rule and continue" default.
