# AXIS start screen and bare `axis` front door — design

Date: 2026-10-19. Base: `origin/main` 471c20a. Status: draft for operator approval.

## Intent

The operator mostly uses `axis agent` and never uses the dashboard. Typing `axis`
should open the agent the way Claude Code, Codex CLI, Gemini CLI, and Grok CLI
open: a branded start screen with a prompt you can type into right away. The
first screen should feel fast, look good in any terminal at any size, and never
crash or stall before it appears.

This is Round 1 of a larger UI overhaul. Round 2 restyles the chat transcript.

## Goals

1. Bare `axis` on an interactive terminal runs `axis agent` with default flags.
2. The agent console opens on a new start screen, "Option B": the gradient AXIS
   logo, a version line, three tips, then the prompt and footer.
3. The start screen adapts live to terminal width and height (tiers below) in
   every common terminal: Ghostty, kitty, iTerm2, Terminal.app, GNOME/Konsole,
   Linux VT, tmux, herdr, mosh, SSH.
4. Startup never crashes or blocks before the start screen paints, including
   when no model is reachable or no snapshot is cached.
5. The start screen is a pluggable piece so two more styles can be added later
   (A "quiet card", C "cluster at a glance") without reworking the console.

## Non-goals (Round 1)

- Restyling transcript entries (answers, tool cards, thoughts). Round 2.
- Implementing styles A and C, and a config key to choose a style. Later round.
- Changing `axis tui` (the dashboard). It stays as it is at `axis tui`.
- Changing `--plain`, `--print`/`-p`, or any existing flag or command.

## Anti-goals (a failure even if it works)

- Scripts break: a non-TTY bare `axis` must still print help and exit 0.
- The start screen shows cluster facts not backed by the cached snapshot, or
  triggers a live SSH sweep on startup (Truth Rule; also startup speed).
- `axis` hangs, panics, or exits non-zero on a fresh or degraded machine.
- Raw 24-bit color escapes on terminals that can't parse them.
- New dependencies. `lipgloss` and `colorprofile` (already in `go.mod`) suffice.

## Behavior

### 1. Routing (`cmd/axis/main.go`)

- Root `RunE`: with no args and stdin+stdout both TTYs, run the agent command's
  normal path with default flags (equivalent to typing `axis agent`).
  Otherwise `cmd.Help()`, unchanged.
- `axis tui` is unchanged. Update every place that says bare `axis` opens the
  dashboard: `README.md:242`, the `main.go` comment,
  `docs/decisions/agent-surface-transcript-model.md`, `docs/current-state.md`
  if it states the behavior, and `CHANGELOG.md`.

### 2. Start screen layout (Option B)

Shown inside the console's ephemeral `View()` while no turn has started (turn 0).

Width tiers (tmux/herdr half-splits of a 160-column window are 79 columns,
which must get the full layout):

| Tier | Condition | Content |
|---|---|---|
| Full | width ≥ 72 and height ≥ 16 | 6-row gradient logo with `agent · vX.Y.Z` to its right; blank; "Tips for getting started:" and 3 numbered tips; dim status lines; prompt; footer |
| Medium | 44 ≤ width < 72 and height ≥ 16 | 6-row logo with version on the line below; 2 short tips; dim status lines; prompt; footer |
| Compact | width < 44 or height < 16 | One line: `▲ AXIS vX.Y.Z`; one line `/help for commands`; prompt; footer |

**Footer (designer's call):** there is one footer, the existing persistent
status footer (`internal/console/footer_status.go`), on the start screen and
every turn after. Round 1 does not redesign it; it only aligns its tiers to
72/44 and switches it to display-width math (section 5). The mockup's
"cwd + branch" segment moves into the start screen's dim status lines instead.

**Dim status lines (designer's call):** under the tips, the start screen shows
short dim lines for: cwd and git branch; snapshot age or `no snapshot`; MCP
state (`connecting MCP…`, `N MCP servers`, or the failure); model resolution
state; the advisory line; and any startup notices (section 6). Compact tier
shows none of these except a failure, clipped to one line.

The logo is 27 columns wide, so it fits every tier that uses it.

Tips text (Full):
1. Ask about the fleet, models, or placement.
2. `/help` for commands, `/model` to switch models.
3. `shift+tab` cycles observe → edit → exec.

Only list keys and commands the console actually supports. The builder verifies
each one against `internal/console` and drops or replaces any that don't exist.

The advisory line that `agent.go` prints to stderr before the console today
("advisory: agent output is not cluster truth…") moves into the start screen
as a dim line under the tips (Full and Medium) and is no longer printed above
the console. On non-console paths (`--plain`, `--print`), it stays where it is.

### 3. Prompt box

Use horizontal rules above and below the input line (or a left gutter `❯ `
with no right-hand border). No closed box with a right border on the cursor
row: it repaints on every keystroke, breaks mosh predictive echo, and fights
multiline wrapping in `internal/console/editor.go`.

### 4. Resize and dismissal

- On `tea.WindowSizeMsg`, the start screen re-renders at the new tier. Because
  it lives in `View()` and nothing is printed to scrollback before turn 1,
  resizing leaves no ghost copies.
- On first submit (prompt, `/command`, or `!shell`), the start screen leaves
  `View()` and one compact line is printed to scrollback above the first user
  entry: `▲ AXIS agent · vX.Y.Z · <model>`.
- No ephemeral line may be exactly `width` columns wide. Cap at `width - 1` to
  avoid auto-wrap on terminals with `am`/`xenl`.

### 5. Color and glyphs

- Logo gradient (cyan → magenta, as in `internal/ui/logo.go` today) is
  rendered at the terminal's color depth: truecolor where supported, 256 or 16
  colors otherwise (Terminal.app gets 256), none under `NO_COLOR`,
  `--no-color`, or a non-color terminal. Depth detection and the downsample
  are a small pure function in `internal/ui` (env-driven, unit-testable).
  **Designer's call:** no new direct dependency. Do not import `colorprofile`
  or `termenv` directly and do not move to lipgloss v2; the direct-dependency
  count in `AGENTS.md` stays 15. (lipgloss v1 can only be forced to a profile
  in tests through `termenv` types, which is why detection lives in `internal/ui`.)
- Unicode support check: treat as ASCII-only when `TERM` is `linux` or `dumb`,
  or the effective locale (`LC_ALL`, then `LC_CTYPE`, then `LANG`) is `C`,
  `POSIX`, or has no UTF-8 charset. ASCII mode uses a plain-letter `AXIS`
  wordmark, `-` rules, `> ` prompt, and `^` instead of `▲`.
- All width math in the start screen and footer uses the existing
  `visibleWidth` (display width), not rune counts. This also fixes
  `internal/console/footer_status.go`, which counts runes today.
- Align the footer's width thresholds (`footer_status.go`, today 80/60/40) with
  the 72/44 tiers so the footer and start screen step down together.

### 6. Startup ordering and degraded states

Today `axis agent` does three things before the console paints that can stall
or kill startup: a synchronous MCP `ConnectAll`, a pre-console numbered model
picker when `agent.default_model` is unset, and exit code 2 when no model is
found. On the console path, change this:

- **Paint first.** Start the Bubble Tea program as early as possible, using only
  local, fast inputs: cached snapshot (`LoadCached`, never `--live` unless the
  flag was passed), cwd, git branch, version.
- **MCP servers** connect in the background with a bounded timeout. The footer
  or a dim status line shows "connecting MCP…" then the count, or the failure.
  A turn submitted before MCP finishes proceeds without those tools; the
  builder decides whether to wait briefly (bounded) instead.
- **Model, unset default:** pick automatically with the existing
  `resolveReadyStartupModelTarget` order (warm local resident → `ai.yaml` role
  → cloud). Never show the pre-console stdin picker on the console path. With
  `-s/--select`, open the existing in-console model picker overlay at startup.
- **Model resolution runs after first paint (operator decision).** The readiness
  probe can take up to ~39 s today (3 candidates × ~13 s). The console paints
  immediately and resolves the model in a background `tea.Cmd`. This means the
  agent is constructed without a backend and receives it when resolution
  finishes; restructure `cmd/axis/agent.go` startup accordingly.
  - **Minimal spinner (operator decision).** One small glyph spins so the user
    knows the program is alive; nothing else moves. Frames are a line rotating
    on its axis: `─ ╲ │ ╱` in one fixed accent color (logo cyan, or plain under
    no-color), at the console's existing `spinnerInterval` (120 ms). ASCII
    mode uses the console's existing `- \ | /`. The text next to it is static
    and dim, e.g. `╲ finding a model…`: no shimmer, no color cycling, no
    elapsed-time counter, no per-candidate text updates.
  - **Designer's call:** this becomes the console's single spinner. The existing
    `spinnerFrames` (thinking row, pending tool rows) switch to the same
    Unicode frames with the ASCII fallback, so there is one busy indicator
    across the app.
  - While resolving, the footer's model segment shows the spinner glyph in
    place of the model name. On success the start-screen line settles to
    `✓ <model> ready` (ASCII `ok <model> ready`) and the footer shows the
    model. On failure it settles to the no-model state below. MCP connection
    uses the same spinner on its own line.
  - **Prompt submitted before a model is ready:** the user entry is committed
    to the transcript at once and shown with a spinner `waiting for model…`.
    It is sent automatically when resolution finishes, or replaced by the
    no-model notice if resolution fails. Esc/ctrl+c cancels it like any
    running turn. Slash commands and `!shell` never wait for the model. In the
    first moments of startup (cluster context load plus MCP connect, bounded
    at about 15 s worst case, usually under a second) the agent itself is not
    built yet; a command typed then gets `agent still starting — try again in
    a moment` instead of waiting.
  - Resolution is bounded by the existing timeouts; it never runs unbounded
    and never blocks input.
- **No model reachable:** still open the start screen. The footer shows
  `no model · /model or axis model start`. Slash commands and `!shell` work.
  Submitting a chat prompt prints an actionable notice into the transcript
  (`no model available — start one with axis model start <name>, or configure
  ~/.axis/ai.yaml`) and does not exit.
- **No cached snapshot / daemon down:** open normally; footer shows
  `no snapshot`. No automatic sweep.
- **Explicit flags still fail loudly (designer's call).** The open-anyway rule
  covers only implicit defaults (no `--model`, `--role`, `--provider`,
  `--cloud-model`). A pinned `--model` that isn't available, an unknown
  `--role`, or a `BuildBackend` error for an explicit choice keeps returning
  `ExitErrConfigLoad` (2), as today. Bare `axis` passes no flags, so it always
  takes the open-anyway path.
- **Pre-console output (designer's call).** On the console path, nothing is
  printed to stdout/stderr before the program starts. Today's pre-console
  lines (model fallback notes such as "Default model … is not answering" /
  "Using model … instead", the "Could not load cluster context" warning, the
  `--resume` "Resumed N messages" line, the advisory line) become startup
  notices shown in the start screen's dim status lines. On first submit they
  are printed to scrollback once, right under the compact header line.
- `--plain`, `--print`, and non-TTY behavior keep today's semantics, including
  exit codes and where their output goes.
- Every background step is bounded by a timeout, reports its failure on screen,
  and never panics the program.

### 7. Pluggable start screen

Introduce a small interface in `internal/console`, roughly
`Render(width, height int, s StartState) []Line`, where `StartState` holds
version, model (or none), mode, cwd/branch, snapshot age (or none), and
unicode/color capability. Option B is the only implementation in Round 1. How
it is selected later (config key, program state such as degraded fleet or
`--resume`) is out of scope; leave a single obvious place to choose it.

## Verification

Unit and golden tests (hermetic; `make test`):

- Tier selection at widths 30, 43, 44, 71, 72, 120 and heights 15, 16, 40.
- No rendered start-screen or footer line exceeds `width - 1` at any of those sizes.
- `NO_COLOR` / `--no-color`: no escape sequences in output.
- ASCII mode under `TERM=linux`, `TERM=dumb`, `LC_ALL=C`.
- 256-color profile produces no `38;2;` sequences.
- Start screen leaves `View()` after the first submit; compact header line
  is emitted once.
- No model: console starts, footer shows the no-model text, prompt submit
  yields the notice, no exit.
- No snapshot: console starts, footer shows `no snapshot`.
- MCP connect failure or timeout: console still starts; failure is visible.
- Slow model resolution (stubbed resolver that blocks): first frame renders
  before resolution finishes; only the spinner glyph changes between ticks
  (the rest of the line is byte-identical); a prompt
  submitted meanwhile shows `waiting for model…` and is sent once the stub
  returns; with a failing stub it becomes the no-model notice; cancel works.
- Non-TTY bare `axis` prints help and exits 0 (existing behavior, keep a test).
- `axis tui` still launches the dashboard.

Gates: `make test`, `make test-race`, `make lint` (Go 1.26.x), coverage,
`hack/verify-doc-facts.sh`, `hack/verify-repo-truth.sh`,
`hack/verify-public-boundary.sh` (or `hack/ci-preflight.sh`).

Manual check: bare `axis` in a full-width terminal, a tmux 50/50 split, a
40-column pane, a 14-row pane, and over SSH; resize live on the start screen.

## Left to the builder

- Exact file layout inside `internal/console` and how the root command reuses
  the agent command's run path.
- Exact wording of dim status lines, as long as it is plain and accurate.
- Whether a turn submitted before MCP connects waits briefly or proceeds.
- How the 256/16-color downsample is computed, as long as it goes through
  `lipgloss`/`colorprofile`.
- Test structure, provided every case above is covered.
