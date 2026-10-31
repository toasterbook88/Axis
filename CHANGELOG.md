## Unreleased

### Features

* `axis tui` is now a full-screen workspace in the style of OpenCode: the
  `axis agent` conversation fills the main pane and a sidebar shows cluster
  health, every node's RAM use and the snapshot's alerts. The conversation is
  the same in-process agent and startup path as `axis agent` (streaming, tool
  cards, approvals, `/model`, `/skills`, `/mcp`, `@node` completion), hosted
  by a new embedded console mode that draws the transcript in-frame
  (`pgup`/`pgdown` scroll; colored bars mark operator, tool, reasoning,
  approval and error entries). `ctrl+o` opens a command palette and `ctrl+x`
  is a leader key (`d` dashboard, `p` place, `m` model, `b` sidebar, `q`
  quit). `F2` toggles the dashboard; `a` there returns to the conversation
  with `@node` prefilled. `--model` and `--autonomy` pass through to the
  agent; `axis tui --dashboard` opens only the dashboard.
  User guide: `docs/runbooks/tui.md`; design record:
  `docs/decisions/tui-agent-workspace.md` (supersedes the "`axis tui` stays
  observational" part of `agent-surface-transcript-model.md`).
* `axis tui` dashboard redesign. Every frame fits the terminal exactly at any size: the
  node table drops low-priority columns on narrow terminals and scrolls when
  there are more nodes than rows, and the inspector fills the remaining height.
  The header shows reachable/total nodes, the snapshot's age (colored as it
  goes stale), its source and the version. A summary strip shows cluster
  memory and the warning count. Gauges show percentages. `?` opens a help
  overlay. `g`/`G`, `tab`/`shift+tab` and page keys move faster. The placement
  wizard is centered. Terminals without Unicode (`TERM=linux`, a C locale) get
  a pure-ASCII frame.

### Fixes

* The dead-upstream check behind a LiteLLM hub (`axis ai route`, MCP
  `inference_route_explain`) no longer misjudges four shapes:
  * MCP now resolves a backend's `node:` through `nodes.yaml` as the CLI does,
    so a hub bound by its logical name is checked instead of reported healthy.
  * A hub without `node:` at a private address is no longer assumed to be on
    this machine; its upstreams are left unchecked unless the URL names this
    machine (loopback, a local address, or the exact hostname: a qualified
    name sharing only a first label does not count).
  * An `api_base` with an API prefix other than `/v1` (such as `/api`) is
    listed at `<api_base>/models` after a 404 at `/v1/models`.
  * A model with a deployment that has no `api_base` (a provider default) is
    no longer rejected because its custom deployment is down; the check
    reports "not checked" instead.
* `axis tui` no longer shows cluster state the snapshot does not contain. The
  link column names the observed network class (`relayed`, `vpn`, `unknown`,
  `not observed`) instead of calling every non-Tailscale path `LAN`. The
  Reservations tab shows the snapshot's reserved RAM instead of a hard-coded
  "No active reservations". Unreachable nodes show their error instead of
  empty gauges, and snapshot warnings and partial-probe reasons appear on the
  node they concern.
* `axis tui` no longer starts an extra 30 s refresh timer each time `r` is
  pressed. Refresh ticks from a superseded cycle are dropped.
* `axis tui` keys typed quickly (delivered as one multi-rune message, such as
  `jjG`) now each act as their own key instead of being ignored.
* `hack/verify-public-boundary.sh` no longer passes when it cannot list
  tracked files. Run outside a git work tree (for example in a `git archive`
  extract) it scanned nothing and printed "passed"; it now fails, as it does
  when git tracks no files. Regression tests cover both.
* Model matching in routing no longer treats two different tags of one model
  as the same model: a role asking for `coder:small` is no longer satisfied
  by a hub that lists only `coder:large`. A name without a tag still matches
  any tag of that name, and a registry port is not a tag. Found by the Copilot
  review of #577 after it merged (#590).

### Dependencies and CI

* `go run hack/verify-import-tiers.go` fails CI when an `internal/` package
  imports a package in a higher tier of the `AGENTS.md` §4 layout. The layout
  is read from `AGENTS.md` itself, so it has one home; exceptions need a
  rationale in `hack/import-tiers-allowlist.txt`, and stale ones fail. The
  layout now matches the code: `knowledge` is Layer 5, `reservation` and
  `skills` are Layer 2, leaving three allowlisted exceptions. It replaces the
  package-inventory check in `verify-doc-facts.sh` (#585).

## v0.22.0 (2026-10-28)

A cluster-only agent mode, an explicit spread placement objective, model
routing that checks the server behind a listed model, restart-loop reporting in
`axis doctor`, and approval fixes for external MCP tools. See the
[upgrade guide](docs/release-notes.md#v0220-upgrade-guide) for behavior that
changes for existing setups. Publication and available downloads are tracked
on GitHub Releases.

### Features

* `axis agent --autonomy cluster` (and `/autonomy cluster`) limits the agent to
  the AXIS cluster tools: `axis_status`, `axis_facts`, `axis_models`,
  `axis_place` and `axis_reservations`. Workspace, git, MCP and `todo` tools,
  and dynamically granted extras, are hidden and refused if called. It uses the
  existing scope path; there is no new flag or second gate (#566). The session
  banner names the mode (#570).
* Placement accepts an explicit `spread` objective: `axis task place` and
  `axis placement explain` take `--objective capacity|spread`, HTTP
  `/v2/placement/dry-run` and `/v2/batch/place` take an `objective` field, and
  the MCP `placement_decision` / `simulate_workload_plan` tools and agent
  `axis_place` take an optional `objective`. Spread ranks feasible nodes by the
  resulting cluster reservation-share spread (`reservation_skew`, lower wins).
  The default `capacity` objective is unchanged (#567).
* `axis doctor` adds a Linux **Service restart loops** check. It reads restart
  counters for every loaded system and user service (read-only, 5 s per scope)
  and warns about units with more than 20 restarts that are looping now:
  restarting, failed, or active for less than five minutes. Such units are never
  `failed`, so other checks miss them. A unit that looped once and has run
  cleanly since is not flagged (#573, #575).

### Fixes

* `axis ai route` (and the MCP `inference_route_explain` tool) no longer calls a
  model healthy only because the hub lists it. For a LiteLLM-style hub on the
  same machine, it reads the model's upstream servers from the hub's
  `/model/info` and asks each for its model list (2 s, no prompt). When none
  answers, the hub is skipped like an unhealthy backend, the reasoning names the
  upstream and its error, and strict mode fails with
  "(listed, but no upstream answers)". Checks appear in the new `upstreams`
  field. Hubs on another node, or without `/model/info`, are not checked and
  say so (#577).
* External MCP tools that are not on `mcp_servers.<server>.auto_approve_tools`
  ask the operator in every autonomy mode. AXIS cannot rate them, and a score of
  zero let them run without a prompt under `edit`, `full` and `--auto-approve`
  (#560).
* `--auto-approve` stays in effect after the console installs its approval
  prompt. It was dropped at startup, so default mode asked about every tool
  (#574).
* The `run_shell` tool description no longer tells the model that destructive
  commands are blocked; it states what the safety gate does and names its known
  gaps. Wording only; the gate is unchanged (#565).
* The HTTP API refuses to start on a TCP address without an API token. A Unix
  socket is unchanged, and `axis serve` always has a token (#576).
* Task history and execution observations record task-attributed VRAM: only GPU
  memory used by processes in the task's process tree, from
  `nvidia-smi --query-compute-apps`. VRAM was stored as 0 because of an ordering
  bug, and device-wide memory would have charged resident models to every task.
  With no per-process accounting the value stays 0, meaning not observed (#561).
* `axis agent` startup keeps a probed hub role instead of taking another node's
  Ollama model when the startup model name is not a usable catalog choice; a
  notice names any remaining borrow (#558). Unnamed local startup takes the
  first usable local catalog row, in catalog order (#559).
* `sudo axis update` restarts the invoking user's AXIS-managed service (systemd
  user unit or launchd agent) instead of spawning `axis serve` as root on
  root's socket. That root daemon was unsupervised, outlived the update and
  contended with the user's daemon for the mesh gossip port. With no active
  service for the invoking user, the update now prints how to restart as that
  user and never starts a root daemon. Readiness is read from `/health`, since
  root does not hold the owner's API token (#571).
* `axis doctor` counts every axis daemon on the host for the mesh UDP port
  check, not only those on the configured socket, and lists each daemon's pid
  and address when more than one is running (#571).
* `axis doctor` and `axis daemon service install` warn about system units that
  start an axis daemon outside the AXIS-managed user service. At boot such a
  unit races the user service for the socket and mesh port, and the loser
  restarts forever (#571).

### Observability

* The daemon logs a warning with the trigger, duration and error when a
  snapshot refresh fails; success is logged at debug level (#562).
* The HTTP API accepts or generates an `X-Request-ID`, echoes it on every
  response and logs one line per request (method, path without query, status,
  duration); 5xx responses log at warning level (#563).
* Fact collection logs a warning when `ssh -G` fails and built-in SSH defaults
  are used (#564).

### Dependencies and CI

* `github.com/mark3labs/mcp-go` 1.4.0 (#568); GitHub Actions minor updates
  (#569). Restart-path tests no longer reach the real service manager (#572).
* `make lint` pins staticcheck 2026.2.2, the first release that reads the
  export data Go 1.27.2 writes. With 2025.1.1, lint failed on any machine with
  Go 1.27.2 while CI, on go.mod's Go 1.26.10, passed (#578).
* `TestA2ASlice2E2E` writes its artifact to a temporary directory; the tracked
  `artifacts/` copies are written only with `AXIS_A2A_E2E_ARTIFACTS=1`, which
  `hack/a2a-slice2-e2e.sh` sets. Running the tests no longer dirties the tree
  (#579).
* govulncheck is pinned at v1.9.0 in CI, the release workflow and
  `hack/release-preflight.sh`. v1.1.4 crashed parsing Go 1.27 source on any
  machine whose local Go is newer than go.mod's (#580).

## v0.21.0 (2026-10-23)

Explicit Cortex service addressing, tighter agent approvals, more complete
streaming usage reports and cache-friendly request context. See the
[upgrade guide](docs/release-notes.md#v0210-upgrade-guide) for configuration
and log-reader compatibility. Publication and available downloads are tracked
on GitHub Releases.

### Features

* `axis cortex` accepts `AXIS_CORTEX_URL` to address an HTTP(S) MCP service
  independently of the node's SSH identity. A URL with no path gets `/mcp`;
  invalid nonempty overrides fail instead of falling back. Embedded credentials,
  query strings and fragments are rejected. Existing authentication, TLS trust,
  timeouts and cancellation still apply (#554).
* Optional `AXIS_CORTEX_QDRANT_URL` provides a REST base URL for memory counts
  with an explicit MCP endpoint. Status prints the probed endpoint and reports
  `memories_unknown: true` when a count cannot be observed, including failure
  on the legacy node-derived path. A missing or null count is no longer
  presented as a measured zero (#554).
* Private `turn.finished` session-log records explicitly include
  `usage_reported`. False means no nonzero usage reached the agent; absent
  counters are unknown cost. An explicit zero/zero report also maps to false
  through the current count-based interface; older records omit the field
  (#555).

### Fixes

* `axis agent` asks OpenAI-style servers for token usage in streamed replies
  (`stream_options.include_usage`). Without it, OpenAI and LiteLLM send no
  usage, so session stats and the private session log had no token counts. A
  server that rejects the option is retried once without it, and the backend
  stops sending it.
* `axis agent` also reads token usage sent on a stream chunk that still
  carries a choice (`choices: [{"delta": {}}]`), as some OpenAI-compatible
  servers do, instead of only on a chunk with an empty `choices` list. That
  usage was dropped, so the session log still had no token counts and the
  session summary showed an estimate. Usage is now counted once per reply,
  from the last report.
* `axis agent` "always" approval now covers only the tool it was given for.
  It used to approve every tool for the rest of the session, so approving one
  read-only MCP query also let later MCP writes and shell, node, fleet and
  background commands run without asking (safety-blocked commands still asked).
  Sub-agents inherit the same per-tool approvals. "Never" still blocks every
  tool, including ones approved "always" earlier.
* The console approval prompt marks external MCP tools `[NOT RATED]` and says
  AXIS cannot rate them, instead of a green `[LOW RISK]` badge with
  "no gate finding" for a tool that may change shared state.
* Approval decisions are written to the private session log (`approval`
  events with the tool-call ID), so a review can tell what was approved.
* `axis agent` no longer gets stuck after a tool returns no output. An empty
  tool result was sent as an empty message; servers that reject those (LiteLLM:
  "All non-assistant messages must contain 'content'") failed the request, the
  empty message stayed in history, and every later prompt failed. Empty tool
  results are now stored as `(tool returned no output)`, and empty tool or user
  messages already in history (including resumed sessions) are filled when a
  request is built.
* `axis agent` sends today's local date with each prompt, so the model does not
  guess dates for date arguments. It goes with the user message, not the system
  prompt, so prompt-cache reuse is unchanged.

### Performance

* `axis agent` sends the session-start cluster summary (node count, free RAM)
  with the newest user message instead of in the system prompt. Free RAM differs
  every session, so the stable prefix from #539 still missed the cache between
  separate runs. Measured with a 27B local model: a short run after the first
  took 3.6 s instead of 20 s (about 1.1k prompt tokens re-read instead of 12.4k).
* `axis agent` also sends the session-start git context (branch, HEAD commit,
  clean or dirty file count) with the user message instead of in the system
  prompt. Editing one file or committing between runs changed it, so the
  system prompt still missed the cache while working in a repository.
  These measurements describe a particular backend and workload; this release
  does not guarantee cache reuse or a fixed speedup. The cluster and Git fields
  are captured at session startup, rather than refreshed by these changes.

### Maintenance

* The model-eviction freeze regression asserts the exact `800 MiB` rendering,
  preventing an unrelated `800` in the output from hiding a failure (#556).
* Installation and support documentation were aligned with the published
  v0.20.0 baseline; a source version bump does not make new downloads available
  before publication (#548).

## v0.20.0 (2026-10-22)

Twenty-one feature, fix, documentation and dependency PRs merged since v0.19.6,
plus release preparation. See [upgrade notes](docs/release-notes.md#v0200-upgrade-guide)
for the changed terminal entry point, private session logging and JSON contracts.

### Features

* **Agent front door and responsive start screen:** bare `axis` on an interactive
  terminal opens the agent console with a responsive logo, tips and startup
  status. With default flags, model probes and MCP connections run after first
  paint; the console can open with no model available. `axis tui` explicitly
  opens the dashboard; non-TTY bare invocations still print help (#537).
* **Private bounded session trajectories:** `axis agent` records prompts,
  assistant answers, tool calls/results, outcomes, reported usage and build/model
  attribution locally. Owner-only files, credential masking, bounded records,
  8 MiB rotation, 30-day/1 GiB retention and explicit omission warnings protect
  the corpus. No transcript is published to Cortex or lifecycle events. See
  [logging policy and limits](docs/runbooks/agent-session-logs.md) (#544).
* **Organized hardware facts:** `axis facts --hardware` / `-hw` and
  `axis node facts --hardware` report Linux/macOS component specifications and
  sampled load with source, time, units and explicit unavailable reasons.
  Text, JSON and YAML output and `--watch --interval` are supported; `-h` and
  `--help` remain help. The collector is read-only, never invokes sudo and
  excludes unique device identifiers (#541).

### Fixes

* **Console exit:** `/exit` and `/quit` close the session both before startup
  finishes and after readiness, rather than leaving the console open (#545).
* **Backend switching:** a model round snapshots the backend and its security
  class together, keeping evidence selection, compaction and streaming consistent
  during concurrent model changes (#540).
* **External MCP approval:** external tools require confirmation unless explicitly
  allowlisted by exact name; provider read-only annotations do not grant approval
  (#527).
* **Owner-only persistence:** AXIS-created runtime directories use `0700` and
  files use `0600`; reused legacy append/lock files and replaced atomic stores
  have permissions tightened (#526).
* **Guarded execution:** an unavailable, unconfigured or no-longer-live placed
  node fails closed instead of silently dispatching elsewhere. Local Ollama and
  llama.cpp inference endpoints receive a bounded pre-dispatch liveness check
  (#523).
* **Reservable placement:** nodes unable to cover the guarded reservation request
  are excluded before ranking; placement/explanation share reasons and execution
  retains its live checks and ledger authority (#532).
* **Bounded discovery:** per-node collection has a default 12-second budget and
  SSH dialing a default 5-second budget, with explicit configured overrides;
  canceled work stops queued and follow-up probes and cannot claim completeness
  (#524).
* **Endpoint sweep safety and cost:** reserved non-HTTP ports are skipped;
  default discovery is scoped to model-related listeners/configured ports, with
  whole-sweep caching and explicit broad/disabled modes. Candidate and cached
  evidence stay distinct from a confirmed loaded model (#528, #529).
* **Resident model truth:** Ollama residents come from `/api/ps`, including
  runtime-reported processor/memory/context fields, rather than parsing the
  human-oriented CLI table (#530).
* **Keyed llama.cpp probes:** an explicit API-key argument or `/props` 401/403
  suppresses keyed metadata requests; public health and auth provenance remain,
  without inventing a served name or context window (#535).
* **Hardware truth and robustness:** SMART permission/device-open stub JSON cannot
  become observed failing health; AMD-only sysfs probes run only for AMD GPUs;
  empty macOS BSD/interface names retain distinct IDs. Malformed/non-finite
  telemetry is rejected, counter precision and sampling windows retained,
  live links refreshed, missing devices/permission failures surfaced, and
  subprocess output/inherited-pipe waits bounded. Logical CPU labeling and
  duplicate Ollama tool output are corrected (#541).
* **macOS storage relationships:** captured provider descriptions retain selected
  metadata and meaningful names. `profiler parent` describes JSON containment;
  a volume's `parent` is unavailable unless an explicit backing identity supports
  it. Profiler nesting alone cannot establish a physical drive relationship;
  non-storage parent behavior is unchanged (#541).
* **Empty JSON collections:** list-valued CLI results serialize as `[]` instead
  of `null` (#525).

### Performance

* **Prompt-cache reuse:** per-turn evidence and snapshot context are injected
  into the newest outgoing user message rather than changing the system prompt.
  The unchanged system/tool prefix is eligible for backend cache reuse while
  request context refreshes. This does not force a live discovery sweep, guarantee
  cache residency or promise a fixed speedup (#539).

### Documentation, dependencies and release tooling

* README onboarding and theme-aware banners refreshed (#531); `AGENTS.md` is the
  canonical repository instruction file with a thin Copilot entry point (#536).
* AXIS builds use Go **1.26.10**, fixing `crypto/x509` GO-2026-6809 (#538).
  Local release preflight runs only the pinned GoReleaser validator with its
  separate Go 1.27.0 toolchain; application gates and vulnerability analysis keep
  the caller's AXIS compiler. Regression tests cover isolation and failure stops.
* `mcp-go` updated from 1.2.1 to 1.3.0 (#533); pinned GitHub Actions updated for
  Claude Code, agent-workflow setup and GoReleaser (#534).
* Hardware regressions include the fuller sanitized macOS storage capture,
  empty/colliding device IDs, partial SMART output and native-parser fuzz seeds.
  Session-log tests cover concurrency, retention, masking, permissions and
  failure recovery; console tests cover startup, switching, cancellation and exit.

## v0.19.6 (2026-10-15)

Fifteen pull requests merged on `main` since `v0.19.5`. This patch release mainly retracts the versions whose tags moved during the 2026-10-12 history rewrite, and ships the model-discovery, reservation-cleanup and agent-ergonomics work.

### Module retraction

* **`retract [v0.9.0, v0.19.5]` in `go.mod`:** the 2026-10-12 history rewrite moved every tag from v0.9.0 through v0.19.5. Those versions now fail `go install` with `GOPROXY=direct` because their checksums no longer match sum.golang.org. `go list -m -u` and `go get` now steer users to v0.19.6. Tags v0.2.0 through v0.8.0 are unaffected and are not retracted.

### Features

* **Model catalog as a fact:** consistent, honest model lists in `axis agent` (#507).
* **Probe-backed model load state** for llama.cpp, MLX and Apple Foundation Models (#509).
* **Agent autonomy and tool ergonomics:** `axis agent` prompts are aware of the active autonomy tier, and the search/listing tools gain a tree cap, grep include handling and symlink confinement (#508).
* **Model discovery:** a loopback endpoint sweep for servers the probes miss (#514); Core AI and Core ML bundles listed as unknown-kind disk weights (#516); Private Cloud Compute facts from the read-only Apple probe (#515).

### Fixed

* **Reservations:** the dead `HoldDevice` API and the `device_holds` ledger field are removed; peak observations are renamed `ObservedRSSMB`/`ObservedVRAMMB` (legacy `peak_*` state keys still load); `axis model plan` warns and skips observation-based exclusions when state fails to load instead of aborting (#506).
* **Disk-weight scan** skips iCloud placeholders and always finishes (#513).
* **Agent:** finds keys for this machine's model servers and never swaps a pinned `--model` (#517).
* **PR review gate:** rendering repaired, and the gate fails closed unless CLEAN (#510, #511); private hostnames blocked by shape, not by name (#519).

### Tests & housekeeping

* Operator-specific names and addresses replaced in fixtures and comments (#518).
* Go module minor/patch bumps (#520); GitHub Actions minor/patch bumps (#521).

## v0.19.5 (2026-10-05)

Twenty-four pull requests (#476–#493, #495, #497, #498, #500, #501, #504; #496 and #499 shipped inside #495) merged on `main` since `v0.19.4`, plus this release-prep PR (#494). This patch release ships placement hardening (required operator labels on `axis task place`, off-box bearer-token refusal across daemon endpoints), a hardened self-updater (host allowlist on every redirect, fail-closed digest verification, daemon restart after update), supervisor-aware emergency model eviction/resume, daily model-start profiling, Apple Foundation Model discovery/placement groundwork, and honest release/version plumbing.

### Features

* **Cache-first status reads:** `axis status` and the agent role read the snapshot inside the bounded 5-minute publication window without re-probing nodes (#480).
* **Agent reachability upgrades:** path resolution, multi-candidate placement, and startup auth for agent sessions (#481).
* **Supervisor-aware emergency model eviction:** stopped-model fallback can freeze or terminate supervised llama/MLX residents with identity guards on pid + start time, plus a rotating JSONL eviction telemetry lane and an RFC proposal for durable emergency placement (#487).
* **Model run profiling:** `axis model run-profile` records a daily model-start profile per backend so placement sees runtime history, with new resident-model GPU bundles (every nvidia-smi row preserved) (#489).
* **Apple Foundation Model discovery:** remote AFM probe plus inventory/placement support on Apple nodes (#484), with a planning-only roadmap (#485).
* **`axis update` restarts the daemon after a successful update:** a systemd/launchd-supervised daemon is restarted through its service manager and polled on the service's own `--addr`; `--no-restart` skips the restart. `axis version` reports the running daemon's version and commit (`--local` skips the query, `--cache-addr` targets another daemon) and warns when the daemon runs a different commit at the same version. Public `/health` adds `commit` and `started_at`; PID and executable path stay on the authenticated `/snapshot/meta` (#497).

### Fixes

* **Placement plan helpers back on the cache-first path** (#483).
* **AFM probe no longer emits helper self-test stdout in remote discovery** (#486).
* **`axis version` prints the toolchain VCS commit when ldflags are empty**, so manually-built binaries still report a revision (#488).
* **Off-box bearer-token refusal widened:** the daemon refuses to attach the cluster bearer token to out-of-band addresses on every leaked surface (snapshot/meta/refresh, guarded-exec streams, mesh/meta/refresh/restart, reservations), extending the #468 A2A-only gate cluster-wide; Gate requires `AXIS_ALLOW_OFFBOX_BEARER=1` to opt back in (#491).
* **Required node labels:** `axis task place --require-label k=v` is repeatable and must all match; labels ride discovery into the published snapshot and exclude nodes without changing rank (#491).
* **Agent endpoint health probes attach the resolved `ai.yaml` bearer** (#478).
* **Hardware-validation fixture shells keep stub binaries ahead of the inherited host `PATH`**, with `/usr/bin:/bin` appended as a fallback, so Nix-profile runners still find `awk`/`base64` (#493).
* **Self-update download hardening:** every request and redirect hop must be HTTPS to an allowlisted GitHub host (`api.github.com`, `github.com`, `objects`/`releases`/`release-assets.githubusercontent.com`), capped at 5 redirects. The archive is verified against the GitHub API sha256 digest and `checksums.txt` (both must match when both exist), and the update fails closed before anything is replaced when neither is available. Download errors redact signed query strings and name the responding host (#495, including #496 and #499).
* **`axis model stop --live`** is now a defined flag, honored on `--ollama-model` and `--node/--port` stops and refused with a clear error on generation-ID stops. A failed Ollama unload still writes a `failed` receipt with `started_at` captured before the unload attempt. MLX placement honors profile refusals (#500).
* **nvidia-smi rows with `[N/A]` memory cells are kept** when the GPU index parses, so placement no longer under-counts devices. An unparseable `memory.total` leaves `VRAMMB=0`, and `VRAMFreeMeasured` is true only when `memory.free` parses (#501).
* **Stub-first fixture `PATH` helper:** hardware-validation fixtures resolve `sh`/`sleep` from the same stub-first `PATH` list they hand the child, so the `/usr/bin:/bin` fallback works even with an empty inherited `PATH`; covered by `hack/e2e-fixture-empty-path.sh` (#498).
* **MLX lifecycle:** MLX discovery publishes each resident's `pid`, `executable`, and `process_start_token`, so MLX residents get generation IDs and generation-bound stops like llama.cpp; a server without `--port` is still discovered on 8080. The stop/probe ownership guard also accepts the macOS entry-point form (`Python …/bin/mlx_lm.server`) and only that position, so an unrelated listener whose arguments merely end in `mlx_lm.server` is refused. `axis model start` help now states that MLX "started" means the server process answers `/v1/models`, not that weights are loaded (#504).

### Docs & CI

* **PR review gate:** repair review/comment rendering, regression-test the jq filters, and fail closed unless GitHub reports a `CLEAN` merge state.
* Merge-readiness: judge a PR before declaring it review-clean (#490).
* Unicode ANSI shadow wordmark in the CLI branding (#482).
* Dependabot action-bump group for workflow dependencies (#492).
* README branding, cropped banner, and pasteable quick start (#476, #479).
* Runbook for the A2A off-box bearer gate (#477).

## v0.19.4 (2026-09-28)

Ten pull requests merged on `main` since `v0.19.3`. This patch release ships the A2A agent-card and task plane (cards, authenticated observe send/get, approval queue for exec-shaped work, delegate CLI), honest placement ranking, and related docs/deps housekeeping.

### Features

* **A2A agent cards:** each node serves `/.well-known/agent-card.json` with capability metadata (name, description, version, url, capabilities, skills). Skills derive from the schema-mask scope tier — observe / edit / exec — so a node advertises only the tier it actually serves. Wired through `api.ServeWithContext` with a nil-safe `cardFn` hook; `cmd/axis serve` builds the card from the local snapshot identity at the default observe tier (#464).
* **A2A slice 2 — authenticated task send+get (observe-only):** `POST /a2a/v1/message:send` and `GET /a2a/v1/tasks/{id}` behind the same bearer `withAuth` as `/run`. Observe skills (`axis-status`, `axis-facts`, `axis-place`, `axis-reservations`, `workspace-read`) map to read helpers only; exec-shaped / unknown / over-tier skills reject fail-closed on send. Public agent card remains outside auth with `Streaming=false`. Owner surface `a2a-task` (#465).
* **A2A slice 3 — approval queue for exec-shaped tasks:** `guarded-exec` and `workspace-write` park as `pending` on send (no timer, no batch, no auto-approve). The only promotion path is `POST /a2a/v1/tasks/{id}/approve` with `confirm=YES` and `mode` of `script` or `exec`, which runs `runLiveGuarded` once and stores `completed` or `failed`. `POST .../reject` requires a non-empty reason. Unknown skills still reject at send (#466).
* **A2A slice 4 — task delegate CLI and lifecycle client:** native Go `internal/a2a` client (Unix socket + TCP, bearer attach) with `FetchCard` / `Send` / `Get` / `Approve` / `Reject`, plus `axis task delegate|status|approve|reject` mounted under `axis task` for cross-node delegation against the slice 2/3 HTTP surface (#468).
* **Honest placement ranking metric:** surfaces the real ranking objective (`allocatable_ram`) as the placement headline across CLI, HTTP, MCP, and agent surfaces; FitScore remains diagnostic suitability only. Adds structured `PlacementRanking` (objective, source, metric, decisive criterion, tie-break) and unifies the ranker comparator + decisive-criterion walk into a single `[]comparisonStep` pipeline so explanation cannot drift from sort order (#474).

### Fixed

* **Repo-instructions walk stops at repository sentinels:** `loadRepoInstructions` no longer ascends past `.git` / `go.mod` (or above `$HOME`) looking for `AGENTS.md`, so a parent-tree or home-directory `AGENTS.md` cannot leak into the agent system prompt (#463).

### Documentation

* **Authority secrets:** documents that API token rotation requires a daemon restart (token is captured by value at route registration — deleting the file alone yields 401), and documents `AXIS_ALLOW_OFFBOX_BEARER` next to the API Tokens section (#471).

### Tests & housekeeping

* **A2A slice-2 E2E artifact** restamped against the post-slice-3 deployment tip (#467).
* Bump `github.com/mark3labs/mcp-go` 1.1.0 → 1.1.1 (#472).
* Bump GitHub Actions minor/patch group (`anthropics/claude-code-action`, `github/gh-aw-actions/setup`) (#473).

## v0.19.3 (2026-09-25)

### Fixed

* **Consolidated leading system messages for Jinja templates:** collapses contiguous leading system messages into a single system message on OpenAI-compatible, Anthropic, and Ollama streaming backends. Many model templates (notably Qwen, Mistral, and Llama standard Jinja templates) strictly reject multiple system messages with `System message must be at the beginning.`, which previously failed turns on resident models like Qwen 3.8. Anthropic backend now joins all system message segments rather than dropping preceding ones. On turn 0 failure, unresponded user turns are pruned from history.
* **Stale free-VRAM stays free in plan text:** `axis model plan` treats a positive `VRAMFreeMB` as measured even when a snapshot omits `VRAMFreeMeasured`. Fit math and the printed label both say `N MiB free`. A measured 0 stays `0 MiB free`. An unknown 0, and a negative free figure, fall back to total capacity and print `N MiB total, unmeasured`. Plan JSON gains `vram_free_measured` on each candidate; no external parser of that object is known.

## v0.19.2 (2026-09-21)

Two pull requests merged on `main` since `v0.19.1`. This patch release closes the
installer deletion defect class and hardens model-placement VRAM accounting.

### Fixed

* **Installer no longer unlinks a running binary:** `install.sh`'s legacy-path cleanup removed any superseded copy that identified as AXIS without checking whether a process was currently executing it. A systemd unit or launchd job that had already mapped the binary kept running it from the deleted inode, so the file was gone while the daemon was still serving and the next restart had no binary to start. `path_in_use()` now refuses to remove a path a running process holds — resolving `/proc/<pid>/exe` for real binaries and `/proc/<pid>/fd/*` for interpreted wrappers — and tells the operator to restart the process instead. The absolute legacy candidates are also overridable via `AXIS_LEGACY_CANDIDATES`, which `make test-install` uses to sandbox them so the suite can no longer delete the host binary on any machine with a system install (#445).
* **Free-VRAM-gated placement eligibility:** `axis model plan` gates candidate eligibility on *measured* free VRAM rather than total capacity, so a node with a saturated card no longer claims full offload or slips past the insufficient-memory check. `models.GPUInfo` gains `VRAMFreeMeasured` so a card genuinely reporting 0 MiB free is evaluated honestly instead of falling back to `TotalMB`; the best device is chosen by most free VRAM (tie-broken by total), so an idle card wins over a busier one with higher capacity (#442).
* **Gossip UDP port decoupling:** removes legacy code that copied the beacon `udp_port` onto `gossipPort` in the daemon and CLI, which produced a false beacon/gossip collision and could silently disable the mesh (#442).
* **Snapshot source in plan text:** `FormatModelPlacementPlanText` now prints `Snapshot Source: %s`, eliminating silent cache-to-live degradation on the operator CLI text surface (#442).
* **Backend referencing safety:** replaces unsafe string slicing of `DeviceName` with direct backend referencing, removing a potential slice panic in multi-device tensor-split reasoning (#442).

## v0.19.1 (2026-09-18)

One pull request merged on `main` since `v0.19.0`. This patch release brings single-device VRAM honesty to model placement, free GPU memory probing, mesh UDP isolation, doctor process enumeration resilience, and unified model snapshot acquisition.

### Features

* **Single-device VRAM honesty & multi-GPU annotations:** `axis model plan` evaluates accelerator fit against the best single device rather than summing VRAM across distinct GPUs into an imaginary shared pool. Multi-device nodes are annotated with tensor-split feasibility (`--split-mode layer`) without misrepresenting distinct devices as pooled capacity (#440).
* **Measured GPU free memory:** queries `nvidia-smi` for `memory.free` and records `VRAMFreeMB` in `models.GPUInfo`, enabling real VRAM headroom evaluation while honestly disclosing unmeasured capacity (#440).

### Fixed

* **Mesh UDP port isolation:** cleanly separates the discovery beacon listen port (`UDP :42424`) from the gossip mesh listen port (`UDP :42426`) in daemon initialization and adds `gossip_port` to discovery configuration, eliminating self-inflicted bind address collisions (#440).
* **Doctor process probe resilience:** filters `/proc/<pid>/comm` before attempting to read `/proc/<pid>/cmdline`, preventing `listDaemonProcs` and `axis doctor` from hanging on processes stuck in `exit_mm` or uninterruptible sleep `D`-state (#440).
* **Unified model command snapshot seam:** introduces `loadModelCommandSnapshot` for all `axis model` subcommands, providing cache-first snapshot loading with advisory live fallback for read-only commands (`plan`, `inspect`, `query`) and `ExitErrNoNodesFit` error classification for unresolvable specs (#440).
* **Degraded daemon cache surfacing:** `daemon.FetchSnapshot` appends a typed warning when the daemon cache is degraded (`LastError != ""`), preventing unhealthy fact plane state from rendering as green (#440).

## v0.19.0 (2026-09-18)

Thirteen pull requests merged on `main` since `v0.18.1`. This release introduces the interactive TUI front door with truthful placement preview, a resident-model and GPU/disk weight truth plane, a daemon-first diagnostic mesh facade, non-POSIX remote shell hardening, and a major 38% codebase refactoring.

### Features

* **Interactive TUI front door:** running bare `axis` on an interactive terminal opens the full-screen Bubble Tea cluster dashboard; scripted and non-TTY invocations continue printing standard help (#429).
* **Truthful TUI placement wizard:** the dashboard placement wizard scores the exact snapshot already on screen, badges observation time, shows inferred requirements and deterministic rank order, and labels `FitScore` as diagnostic rather than the ranking objective. Confirmation remains advisory and explicitly executes nothing. Snapshot failures render setup/recovery guidance and state that the TUI starts nothing until the operator chooses a command (#434).
* **AI resident-model & disk-weight truth plane:** surfaces canonical resident model discovery across llama-server and Ollama backends, integrates GPU and disk weight allocations into placement scoring, and provides deterministic hardware-fit placement planning via `axis model` and `axis ai` (#432).
* **Daemon-first diagnostic mesh facade:** `axis mesh status` and `axis mesh peers` query the daemon and `/v2/mesh` rather than opening conflicting UDP listeners; cleanly separates the discovery beacon plane (UDP 42424) from the gossip mesh plane (UDP 42426); adds `/v2/mesh?view=all` with view echoing; differentiates `daemon incompatible` from `daemon unavailable`; surfaces listener bind failures in daemon `LastError` and snapshot discovery warnings with full recovery when reconfigured (#438).

### Fixed

* **Fish shell execution safety:** remote guarded executions wrap commands with `bash -lc` to ensure POSIX-compatible execution on nodes with non-POSIX login shells such as fish (#433).
* **Daemon execution context delivery:** forwards stdin streams properly across SSH execution layers and honors explicit node pinning through the daemon `/run` API (#430).
* **Ollama process probe self-match safety:** running-state process checks exclude the probe process itself to prevent false-positive running detections (#437).
* **Agent tool call promotion:** whole-message bare JSON and Hermes tool calls are cleanly promoted to structured tool invocations (#427).
* **Agent startup model check:** live backend checks verify model availability before defaulting to implicit model tags, avoiding failures on unpulled models (#428).
* **Serve graceful drain order:** signals context cancellation prior to `WaitStopped` in `serve.go` to prevent HTTP shutdown drain timeouts (#438).

### Refactoring & Architecture

* **Decoupled package taxonomy:** cut 38% of Go LOC by breaking up god packages into focused modules (`internal/modellife`, `internal/modelplan`, `internal/modelinventory`, `internal/multipath`), strengthening layered architectural invariance (#436).
* **Agent test coverage:** added characterization tests covering deferred JSON flush paths and Hermes grammar drift (#435).
* **Dependencies:** bumped `anthropics/claude-code-action` from 1.0.216 to 1.0.222 in CI (#423).

## v0.18.1 (2026-09-15)

### Fixed
* **Console streaming panic:** interactive `axis agent` console sessions crashed with `panic: strings: illegal use of non-zero Builder copied by value` on any streamed response of two or more chunks. `Model.stream` was a `strings.Builder` value inside a value-receiver Elm loop; every Bubble Tea model copy invalidated the Builder's address self-stamp and the next write panicked. It is now a `*strings.Builder` allocated in the constructor with a lazy guard at the write site, so all Elm copies share one heap Builder. The regression test delivers its second chunk from a separate goroutine — the shape that actually triggers the copy — and was verified to panic at the pre-fix code.

## v0.18.0 (2026-09-14)

### Features
* **Console footer real usage:** when the backend has reported per-turn token usage, the statusline shows `usage: <in>/<out> · <mode>` on wide terminals. The context gauge stays occupancy (`ContextTokens` / max); session spend is not painted as window fill. Autonomy mode rides the usage segment so it stays visible after turn 1. No usage segment when the backend reported none.
* **One-shot piped execution:** `axis agent -p "prompt"` runs one instruction with a pipeline-clean output contract — the assistant's text on stdout, compact tool badges with timing receipts and a session summary on stderr, no session banner, and a fail-closed confirm (denies with a pointer to `--auto-approve`/`--autonomy full` instead of blocking). Positional one-shot without `-p` keeps the legacy behavior byte-for-byte.
* **esc esc clears the console draft:** the first idle Esc arms the gesture, the second clears — a single accidental Esc no longer wipes a draft (mirrors the ctrl+c quit gesture). Esc during a running turn still cancels it.
* **Console prompt history persists:** submitted prompts append to `~/.axis/history.jsonl` (best effort, private file) and the console re-seeds its recall ring with the last 100 entries at launch; consecutive duplicates collapse.
* **Console is the default interactive surface:** bare `axis agent` on an interactive terminal now launches the transcript console; the legacy line-at-a-time REPL moves to `--plain` (unconditional downgrade, wins over `--console`). Scripted and non-TTY runs never engage the console. `--console` is retained as a force alias and marked deprecated in help.

* **Agent cache-first startup:** `axis agent` loads cluster context from the local daemon unix socket, then disk `snapshot.json`, then a bootstrap skeleton. It does not present cache as live runtime: reads are badged with age, stale, and vantage (`unknown` if missing). `--live` is the explicit discovery sweep. Bootstrap publication source is `bootstrap`, never `live-runtime`. Minted fallback envelopes use the snapshot timestamp as `AssembledAt`, not read time.
* **Console approvals:** the console presents an ApprovalOverlay. Yes is explicit `y`. Enter does not approve. Timeout (2 minutes) and context cancel deny. (Console default: see the console-default entry below.)
* **Model cache refresh:** `axis model start` and `axis model stop` POST the existing daemon `/refresh` after a successful mutation. Refresh failure is a stderr warning; the mutation still succeeds.

### Truth plane

* Record snapshot vantage (the collecting host) at `snapshot.Build` and deep-copy it through reservation overlays. Cached reads keep the daemon host as vantage; render time does not re-infer the CLI host.
* Replace `axis summary` pairwise CIDR `CLUSTER TOPOLOGY` with a vantage-labeled `REACHABILITY` view. Routes are `vantage → node` observations (network class + SSH handshake duration). Subnet equality is not edge evidence. See `docs/decisions/topology-truth-contract.md`.

## v0.17.1 (2026-09-07)

Nine commits on `main` since `v0.17.0`. No new operator command surface. Cluster nodes pick this up with `axis update`, then `axis daemon restart && axis daemon status` where a daemon is installed.

### Dependencies

* Bump `github.com/mark3labs/mcp-go` from `0.58.0` to `1.0.0`. Live `axis mcp serve` `tools/list` remains 20 tools / 17 read-only.
* Bump `al.essio.dev/pkg/shellescape` from `1.6.0` to `1.6.1`.
* Bump the GitHub Actions minor/patch group (2 updates).

### CI & Truth Gates

* Add a fail-closed deadcode allowlist gate (`hack/verify-deadcode.sh`, pinned `golang.org/x/tools/cmd/deadcode`). Unreachable symbols not in `hack/deadcode-allowlist.txt` fail CI.
* Add `hack/verify-deadcode-tests.sh` for the gate's failure paths (tool did not run, `go:` error after stripping download progress, unallowlisted symbol) and wire both scripts into `.github/workflows/ci.yml` and `hack/ci-preflight.sh`.
* Extend `hack/verify-doc-facts.sh` with an `internal/` package inventory check against `AGENTS.md` and a 30KB `AGENTS.md` budget (margin before the 32KB agent injection cap).
* Document those gates in `AGENTS.md`.
* Raise CI coverage floors to match live package coverage: total 65%, `internal/mcp` 70%, `internal/api` 65%.

### Fixes

* **Docs:** Align `SECURITY.md` supported line with `v0.17.x`. Document structured safety as live on the guarded path and `axis chat` as removed in `docs/architecture.md` and `docs/current-state.md`.
* **Tests:** Run `TestPrepareGuardedExecutionSafetyDeny` in the default test set (drop `safety_scaffolded` tag). Isolate `hack/hermetic-go-test-tests.sh` from inherited `GOCACHE`/`GOPATH`/`GOMODCACHE`.
* **API:** Require bearer auth on `/v2/metrics` (same `withAuth` as other `/v2` reads). Default listen remains the Unix socket.

### Cleanup

* Remove unused `cmd/axis/noun_registry.go`, deprecated `Fatal()`, and unused TUI logo helpers.
* Split `axis agent` slash-verb dispatch and REPL runtime out of `agent.go`, extract startup model-resolution, and add characterization tests for the previous coverage blindspots.

## v0.17.0 (2026-09-04)

### Features

* **Model await & query:** Add `axis model await <instance-id>` for polling readiness with `--timeout` and `--interval`, and `axis model query <instance-id|model> "<prompt>"` for prompt inference against local or remote model instances via HTTP and SSH curl execution. Supports `--temperature` (with `0.0` greedy decoding support), `--max-tokens`, and a bounded 10MB remote response stream limit.
* **Model placement planning:** Add `axis model plan <spec|weights>` to perform dry-run evaluation of cluster nodes for model placement without mutating services. Introduces canonical `ModelSpec` schema (`axis.model-spec/v1`) keeping format, weight size, context overhead, runtime overhead, and accelerator compatibility separate. Evaluates candidate nodes against available RAM, VRAM, and port availability; checks resident models for port collisions; scores eligible nodes deterministically with fit labels; provides reasoning; and supports `--format text|json|yaml`, `--port`, `--cache-addr`, and `--live`.
* **Model start:** Make `axis model start` cache-first by default, eliminating the 54-second full-cluster discovery sweep while supporting `--live` for explicit discovery. Add preflight port conflict verification against resident models. Emit typed `axis.model-operation/v1` lifecycle receipts with publication binding, supporting `--format text|json|yaml` and `--cache-addr` for local daemon authority.
* **Model stop:** Add generation-bound `axis model stop <generation-id>`. Resolves the model instance and host from the local daemon cache, revalidates process ownership, PID, executable, and process start token on the host before terminating, refuses to kill if the process generation changed, and writes a typed `axis.model-operation/v1` operation receipt with snapshot publication binding.
* **Model inventory:** Add a canonical read-only model-instance schema plus `axis model list` and `axis model inspect <instance-id>`. Inventory reads are daemon-cache-first for fast operator feedback; `--live` is an explicit fresh collection with no hidden fallback. Every instance preserves the observed node status and timestamp, while the inventory carries its snapshot source, publication ID, and warnings. Instance IDs are deterministic keys for the observed node/engine/model/port slot, not fabricated process IDs.
* **Documentation & Architecture:** Refresh `AGENTS.md` and `README.md` to document the completed native model lifecycle loop (`plan` → `start` → `await` → `query` → `stop`), catalog all 46 internal packages across the 5-layer trust hierarchy, and align minimum Go version with `go.mod` (`Go 1.26.6+`).

### Breaking

* **CLI exit contracts:** `axis doctor`, `axis daemon status`, and `axis model stop` now exit **4** when they report an unhealthy or no-op result, after writing their complete diagnostic output. They previously exited 0, so shell automation could not distinguish a healthy cluster from a broken one. There is no deprecation path for an exit code: **shell consumers that treat these commands as pass/fail must branch on exit 4.**
  * `axis doctor` exits 4 when any check is `fail`, in both bare and `--strict` mode. `--strict` is unchanged in meaning: it only promotes daemon-cache unavailability from `warn` to `fail`. Warning-only runs still exit 0, and the interactive setup-wizard path is unchanged — an accepted wizard's success and its error are both passed through.
  * `axis daemon status` exits 4 for `unavailable`, `stale`, `degraded`, and `incompatible` metadata. The machine-readable envelope is still written first and is unchanged; only the exit code is new. A failure to write that envelope still outranks the health disposition.
  * `axis model stop` now reports a typed disposition — `stopped`, `not_running`, `wrong_owner`, `inspection_unavailable`, or `generation_mismatch` — and exits 4 for everything except `stopped`. A port with no listener previously printed `stopped` and exited 0. Missing `fuser`/`lsof`/`ps` is reported as `inspection_unavailable`, never as `not_running`, because the port was never observed.

### Security & Boundaries

* **Dependencies:** Update `golang.org/x/crypto` from `v0.55.0` to `v0.56.0` to clear reachable SSH denial-of-service vulnerabilities GO-2026-6354 and GO-2026-6355 across all remote fact-collection and execution paths.
* **CI & Runner Isolation:** Remove `pull_request` trigger from `.github/workflows/hardware-validation.yml` to isolate self-hosted LAN runners from untrusted PR execution before making the repository public.
* **Boundary Enforcement:** Add `hack/verify-public-boundary.sh` and wire `Public boundary guardrails` into `.github/workflows/ci.yml` to mechanically enforce RFC 2606 example domains and prevent private cluster domain patterns or non-allowlisted IPv4 literals from entering the repository.
* **Cortex Node Decoupling:** Decouple `axis cortex` and `internal/cortex/client.go` from hardcoded target hosts; target nodes now dynamically resolve via `AXIS_CORTEX_NODE` → `role: cortex` → name `cortex` → backward-compatible fallback `foundry`.

### Bug Fixes

* **Model truth:** Detect a running `llama-server` from process evidence even when its supervised absolute executable path is outside the collector's login `PATH`. Keep on-disk weight bytes, resident process RAM, and runtime-reported accelerator memory as separate facts (`weight_size_mb`, `size_ram_mb`, and `size_vram_mb`); llama.cpp model-file size and MLX RSS are no longer mislabeled as VRAM.
* **Daemon:** Make `axis daemon restart` readiness honest. Both the short-circuit and the post-restart poll now use one predicate requiring the exact current version, `Ready=true`, `Stale=false`, and an empty `LastError`; previously both checked only version and staleness, so a daemon that was not ready — or that was reporting a refresh error — was announced as "already fresh". A current-version daemon that is merely still starting gets one bounded 3-second grace period before at most one terminate/start cycle, so repeated restarts cannot churn a daemon that was about to come up. Wrong-version and stale daemons get no grace: exact version equality is an intentional split-binary guard for installations where the CLI and the supervised daemon run from different filesystem paths.

### Operational Notes

* Installed daemons on cluster nodes may need `axis daemon restart && axis daemon status` after operators self-update via `axis update` to ensure running services transition cleanly to the new release binary.

## v0.16.0 (2026-09-01)

### Features

* **Snapshot authority:** Attach a publication envelope to live and daemon-cached snapshots with a unique publication ID, assembly source/time, cache age, facts observation time and discovery digest, reservation-ledger digest/count, and state digest/schema/update time. The ledger entry set is frozen once and used for both evidence and reservation overlay so the published capacity view cannot race its own ledger fingerprint.
* **Snapshot authority:** Bind daemon metadata to its snapshot publication ID. Cached clients reject missing IDs and refresh races that return metadata and snapshots from different publications before combining either response.
* **Snapshot authority:** Keep discovery freshness native to the published snapshot. Cached clients no longer backfill a missing snapshot field from the separately fetched metadata response.
* **Authority observability:** Emit typed, structured maintenance receipts after state cleanup or ledger reconciliation successfully changes persisted records. Receipts remain advisory slog telemetry and never feed control decisions.
* **Docs:** Mechanical authority-doc regression guards in `hack/verify-doc-facts.sh`. The reservation-overlay code quoted in `docs/authority-reservation.md`, `docs/authority-transition.md`, and `docs/reservations.md` must match the live `internal/snapshotview/overlay.go` legacy-state branch (both directions: stale quotes fail, and removal of the fallback forces doc updates). New checks also pin the `docs/authority-reservation.md` §6 grep invariants mechanically: ledger `Reserve`/`Release`/`Heartbeat` callers are restricted to the documented surfaces (execution, `/v2/reservations`, MCP triangle), `RAMReservedMB` stays assignment-read-only inside `internal/snapshotview`, and a future `WatchLedger` daemon watcher cannot coexist with the doc claim that `ledger.json` is not watched.
* **Facts:** Inventory on-disk weight artifacts (`DiskWeights`) during local and remote fact collection. Catalog-first (HuggingFace hub `models--*`, ollama manifests, systemd `--model-path`/`-m`), then a bounded per-volume find of GGUF/safetensors above 20 MiB. Shards group to one tree; empty name-only directories are not weights; GGUF files must have magic `GGUF`. Scan is time-capped and may set `disk_weights_truncated`. `axis facts` prints the list. Distinct from resident (loaded) models. Remote scan skips NFS/CIFS/SSHFS and other network df sources, enforces a global 400-file cap (not per-mount), and applies an 8s wall-clock deadline. Safetensors below the size floor and Git LFS pointer files are not counted as trees.

### Bug Fixes

* **Reservations:** Treat a successfully loaded reservation ledger as authoritative even when it reports zero, so stale legacy `state.json` values cannot reintroduce reservations into snapshot views. Ledger load failures now fail runtime reads and daemon refreshes closed, preserving the last valid snapshot and the corrupt ledger for operator repair instead of silently publishing an empty authority.
* **Doctor:** Report daemon ownership instead of reachability alone. `axis doctor` now fails on more than one axis daemon serving the same API address and on a daemon whose executable inode has been deleted or replaced, and reports socket ownership at the daemon address (live listener, stale socket file, non-socket squatter, missing path, and whether the `<addr>.lock` guard is held) plus mesh UDP port contention as warnings. All probes are observational: doctor never kills a process, unlinks a socket, or takes the ownership lock.
* **Daemon:** Take exclusive ownership of the API socket. A second daemon on the same address now fails to start with a clear error instead of unlinking the live socket and silently orphaning the incumbent, which kept serving a divergent snapshot cache on an unlinked inode. Ownership combines a non-blocking advisory lock on `<addr>.lock` with a connect probe, so a socket file left by a crashed daemon is still reclaimed. TCP addresses were already exclusive.
* **Daemon:** Serve the mesh route the CLI actually requests. `axis daemon mesh` returned 404 against every deployed daemon because the CLI called `/mesh`, which only an unused internal route registration ever defined; the production mux served `/v2/mesh`. The CLI now requests `/v2/mesh`, which works against already-running daemons without a restart, and `/mesh` is registered on the production mux as an alias for older clients.
* **Execution:** Upload remote execution context with base64 rather than a POSIX heredoc, so context content equal to the heredoc delimiter can no longer terminate the document early and execute shell on the target node.

## v0.15.0 (2026-08-25)

### Features

* **Agent:** Add `fleet_exec` (parallel multi-node shell execution behind the safety gate), `remote_write_file` (base64 transport so file content cannot escape into a remote shell), `remote_tail_logs`, and async `spawn_subagent` runs that return a background task id.
* **Agent:** Add REPL slash commands `/plan`, `/todo`, `/diff`, `/undo`, `/compact`, `/autonomy`, `/export` (session worklog Markdown export), and `/fleet`, with readline completion.
* **Agent:** Raise conversation defaults to `--max-tokens 32768` and `--max-turns 25`.
* **Agent (experimental):** Add `axis agent --console`, an opt-in interactive transcript console that renders structured loop events; tool approvals are denied until an asynchronous approval overlay exists.
* **AI:** `axis ai backends` prints a viewer-relative locality column: `here` (this process), `peer` (another enrolled node), `cloud` (public URL, no node). Backend names are unchanged.

### Bug Fixes

* **Agent:** Restore the OpenAI streaming scanner error check so a truncated stream fails instead of feeding partial content to fallback tool-call extraction, and stop promoting naked JSON objects in prose to executable tool calls.
* **Agent:** Encode `remote_write_file` content as base64 so a content line equal to the heredoc delimiter can no longer execute shell on the target node.
* **Agent:** Keep surface-installed confirmation functions intact across autonomy-mode changes and serialize turn execution so an abandoned turn cannot race its replacement.
* **Reservations:** Make `reservations inspect` use the non-reconciling ledger load so inspecting a stale entry cannot delete it, and propagate text writer failures after rendering the complete detail block.
* **Reservations:** Revalidate every `reservations doctor --fix` finding against the locked, current ledger state so a heartbeat or ownership change between diagnosis and repair cannot release a revived reservation.
* **Reservations:** Make `reservations release` hold the ledger lock across its read/check/write transaction, preserve unrelated stale entries, avoid a redundant second save, and surface text or JSON writer failures instead of reporting success after dropped output.
* **Config/AI:** Reject provider priorities outside `0..100` and negative, NaN, or infinite model-cost and inference-budget values before they can distort provider ordering or bypass cost controls.
* **Config:** Reject negative or overflowing node/discovery durations and out-of-range SSH/UDP ports during `nodes.yaml` load instead of silently defaulting malformed values or failing later in socket/timer paths.
* **AI:** Reject non-positive backend and route probe budgets instead of constructing an already-expired context and reporting every configured backend as unavailable.
* **Reservations/CLI:** Make `reservations doctor --fix` remediate exactly the entries classified by the requested `--stale-window`, persist each release before reporting it fixed, and reject non-positive stale/watch/refresh durations before destructive work or ticker construction.
* **CLI:** Route non-streaming command output and diagnostics through Cobra's configured writers, including context, skills, placement, status, and summary; structured/text helpers now propagate write failures instead of reporting success after dropped output.
* **CLI:** Preserve structured output on empty AI backend/role and MCP server inventories (`[]` or `{}` instead of prose), reject structured formats for the text-only `status --watch` stream, and reject non-positive watch intervals before `time.NewTicker` can panic.
* **CLI:** Reject unknown `--format` values across every structured-output command instead of silently falling back to text, preventing successful automation runs with an unintended output contract.
* **Agent:** Surface conversation-history persistence failures in single-shot and plain-input modes instead of silently reporting an otherwise successful session while losing its history.
* **Model:** Fail closed when an observed remote node has no matching `nodes.yaml` entry instead of running the model lifecycle command on the local machine. Snapshot-confirmed local nodes remain executable without an SSH seed.
* **Model:** `axis model stop` uses `fuser` when available and falls back to `lsof` on platforms such as macOS. It remains idempotent when no process owns the port but now reports an error instead of claiming success when neither port tool exists.
* **Model:** Before killing a listener, `axis model stop` verifies every owning PID is exactly `llama-server`; a mistyped port now refuses to terminate unrelated services.
* **Model:** `axis model start` now refuses an already-occupied port before launching, then verifies the post-start listener is owned by `llama-server` before probing `/v1/models`. An existing OpenAI-compatible service or bind race can no longer make a failed launch look successful.
* **AI:** Resolve each backend's `node` as the documented `nodes.yaml` name before classifying locality or choosing between `base_url` and `advertise_url`. Logical node names no longer make an on-box backend appear to be a peer or probe its off-box URL.

### Maintenance

* **Release/CI:** Require a successful pre-tag cross-platform dry run, strict SemVer and `main` ancestry, install regressions, full-SHA Action pins, Cosign-verified GoReleaser installation, complete release-manifest/SBOM validation, and GitHub provenance attestations. Add repository-owned CI/release preflight commands and align release documentation with the guarded workflow.


## v0.14.14 (2026-08-15)

### 🚀 Features


* **CLI:** Noun registry with two tiers (operate: cluster/node/model/task/daemon/agent; inspect: doctor/mesh/ai/init/serve/update/version). `axis node facts` is this machine; `axis cluster` is `status`/`summary` only. `axis chat` and `axis llm` print a removal message. `/nodes` retires with a use-this message. `/cluster` prints `session-snapshot` and age. Leaf `--help` no longer invents subcommands. (#297)
* **CLI:** `axis summary` stays the dashboard verb (also `axis cluster summary`). Help states cache-default and `--cached=false` for a live collect. Not a flag on `status`. (#298)
* **Agent:** `nodes.yaml` key is `agent.default_model`. `chat.default_model` is still read if the new key is unset. `axis chat` is gone; this is the agent startup default. (#299)
* **AI:** `axis ai backends` probes `advertise_url` when the backend belongs to another node (same selection as the agent catalog) and prints the URL it actually probed. Off-box backends no longer report `down` while the Host is green. (#300)






## v0.14.13 (2026-08-15)

### 🚀 Features

* **Facts:** Inventory every mounted volume (root plus other local mounts), not just root + `_Ext` totals. Virtual filesystems (tmpfs, overlay, docker, snap/loop, Darwin synthetic, 0 GB images) are omitted. Local sizes come from `df -kPl` (never stats a remote server). CIFS/NFS/SMB rows come from `mount`/`/proc/mounts` with sizes left 0. Bus/class inferred from the device path only; role is `root` or `other`. `disk_total_gb` is still the root filesystem. (#288)
* **Facts:** Join network volumes to the owning cluster node from `Device` (CIFS `//user@host/share`, NFS `ip:/export`, mDNS `._smb._tcp.local`). Sets `owner` + `owner_mount`; does not copy sizes (share stays 0/0). Ambiguous hosts stay unresolved. (#289)
* **Facts:** Observe Linux block bus/class from sysfs (`rotational`, `removable`, USB `device/speed` in Mbit). Path inference remains the fallback. Network volumes are not probed. (#290)
* **Facts:** After owner join, copy the owning node's observed bus/class/removable/link_mbit onto the network row. Sizes stay 0. (#291)
* **CLI:** `axis model start --node --weights --port` and `axis model stop --node --port`. Weights must sit on a named local volume. llama-server only; listen on 127.0.0.1; port is required (no 8080 default). No Traefik, no harness writes. (#292)
* **Facts:** Observe Darwin volume bus/class from `diskutil info` (Protocol, Solid State, Removable Media, Device/Link Speed). Network volumes are not probed. (#293)
* **Facts:** Remote Linux collect (bundle `sysfs_block_b64` and the multi-probe fallback) dumps `/sys/class/block` rotational/removable/speed once and applies it to named local volumes. Does not stat network mounts. (#294)

### Maintenance
* **Docs/Tests:** Replace operator-cluster hostnames and LAN/Tailscale addresses in fixtures and worklog with RFC 5737 / symbolic names. `hack/verify-public-boundary.sh` (wired into `verify-doc-facts`) rejects non-documentation IPv4 literals in Go sources and CHANGELOG. (#289)

## v0.14.12 (2026-08-14)


### 🚀 Features
* **Agent:** In-session `/facts` and `/cluster` on `axis agent` print the local resident table (with probed ports) and the cluster node table from the **session snapshot** without a live collect. `/nodes` still does the live/daemon path. Session header adds `[model:] [endpoint:] [status: probed|stale]`. `/exec` is not shipped. (#286)
* **AI:** Optional `advertise_url` on `~/.axis/ai.yaml` backends for reverse-proxy / cluster-ingress names. Non-local catalog choices use it and probe before enabling; same-box callers keep `base_url`. `resolveNodeEndpoint` (direct IP/SSH) is unchanged. (#285)

### 🐛 Bug Fixes
* **Facts/Agent:** Parse llama-server `--port`/`-p` from argv (default 8080 only when unset) and probe **local** llama.cpp/MLX `/v1/models` the same as remotes so a dead default port is not selectable. Hardware Validation installs awk/rg/jq **before** tests so NixOS discovery scripts can parse ports. (#284)

## v0.14.11 (2026-08-14)

### 🚀 Features
* **Daemon:** Add native per-user service management — `axis daemon service install|status|uninstall` provisions a managed launchd (macOS) or systemd (Linux) user service with foreign-file refusal and direct argv execution, seeds first-time configuration from the observed stable local identity, and documents persistence ownership, paths, privacy, and unmanaged-listener recovery. (#281)
* **Multipath:** Expose monotonic process-lifetime route reuse counters (route decisions, candidate attempts, cached-path revalidations, full fan-outs, failures) through daemon metadata/status JSON, without persisting latency observations that lack a durable vantage-point identity. (#278)

### 🐛 Bug Fixes
* **CLI:** `axis daemon status` now emits exactly one versioned `axis.output/v1` JSON envelope on stdout, classifying fresh, stale, degraded, unavailable, and incompatible outcomes with typed warnings and keeping diagnostics off stdout. (#280)
* **Persistence:** Keep runtime state owner-only — centralized 0700 directories and 0600 files across legacy append, lock, config, event, ledger, and task-log paths; removed readline's duplicate permissive prompt-history files while retaining structured private chat/agent history. (#279)
* **API:** Bound daemon request lifetimes — 15-second request-body reads and 60-second idle keep-alives alongside the existing 5-second header limit, response writes left unbounded for streamed `/run` output, and the unused legacy daemon HTTP server removed. (#277)

### 🔧 Maintenance
* **CI:** Harden the Claude workflow entry points — reject prompts from untrusted author associations before the write-capable action starts, remove issue-assignment replay, bound concurrency/runtime, prevent checkout credential persistence, pin checkout and Claude Code Action to immutable commits, and add fail-closed regression coverage to required CI. (#276)
* **CI:** Separate repository and release truth so publishing a release cannot stale `main` — keep committed current-state facts repository-derived, centralize source/tag equality validation with executable regressions, run canonical hermetic Makefile gates in CI/release, and share the hermetic Go runner with the NixOS hardware job. (#275)
* **CI:** Remove the Dependabot self-approve step, incompatible with read-only Actions defaults and unnecessary since `main` requires zero approving reviews; auto-merge via `gh pr merge --auto --squash` is preserved. (#282)

## v0.14.10 (2026-08-04)

### 🚀 Features
* **Install:** `install.sh` installs to `/usr/local/bin` by default so every node resolves the same absolute path, and retires superseded user-local copies. The downloaded binary is staged inside the destination, executed and version-checked, then renamed over the canonical path, so a checksum-valid but unusable artifact can no longer destroy a working install. The existing entry is classified first: package-manager symlinks, unresolvable symlinks and non-regular entries are refused, and a non-AXIS file requires `AXIS_FORCE_REPLACE=1`. Path resolution fails closed on `..`, inaccessible ancestors and unresolvable symlinks. Adds `AXIS_REQUIRE_PINNED`, `AXIS_DRY_RUN`, `AXIS_RELEASE_BASE_URL`, and a committed regression suite (`make test-install`). (#271)
* **Doctor:** Report duplicate `axis` binaries on a host, reusing the shadow enumeration already present in `axis update`. (#271)

### 🐛 Bug Fixes
* **Update:** Preflight target writability before downloading, so a privileged destination fails immediately with the elevation command rather than at the final rename after a multi-megabyte download. (#271)
* **Tests:** Two `cmd/axis` tests relied on the caller to isolate `HOME`; they now set `HOME` and `AXIS_HOME` themselves, so a bare `go test ./...` no longer reads the operator's store. (#270)

### 🔧 Maintenance
* **Fleet tests:** Add `internal/fleettest`, a write-containment guard for tests that execute against real nodes, plus a build-tagged two-node facts smoke (`make test-fleet`). Not imported by `cmd/`, so it never enters the shipped binary. (#272)

**Upgrade note:** `install.sh` now defaults to `/usr/local/bin` rather than `$HOME/.local/bin` and removes the superseded user-local copy. Set `AXIS_INSTALL_DIR` to override, or `AXIS_KEEP_LEGACY=1` to leave existing copies in place.


## v0.14.9 (2026-08-04)

Tagged but never published. The release workflow failed its documentation gate
before GoReleaser ran, so no artifacts exist for this tag. Superseded by
v0.14.10, which carries the same changes. The entry is retained because
`hack/verify-doc-facts.sh` requires one for every git tag.

## v0.14.8 (2026-08-03)

### 🚀 Features
* **MCP:** Add read-only `verify_execution_safety` and `simulate_workload_plan` tools for structured safety inspection and ranked placement simulation. (#264)
* **Execution:** Sample best-effort peak NVIDIA VRAM during local guarded execution and record it in execution observations. (#265)
* **Facts:** Add an unwired, single-vantage `PairwiseLinkMatrix` substrate for directional probe observations; snapshot topology remains absent unless a caller explicitly builds it. (#266)

### 🐛 Bug Fixes
* **Multipath:** Bound concurrent SSH path probes, collapse redundant candidates, revalidate a short-lived successful path cache, reject unusable routes and non-SSH listeners, and preserve logical SSH identity and fallback behavior. (#267)
* **Tests/state:** Isolate the full test suite from operator `~/.axis` state and harden state, skills, reservation, and persistence update paths. (#260)

### 🔧 Maintenance
* **Truth contracts:** Reconcile current-state claims and add evidence/provenance contracts for placement, capability probes, and topology observations. (#257)
* **CI:** Bump `actions/checkout` from 7.0.0 to 7.0.1. (#258)
* **Deps:** Bump `github.com/mark3labs/mcp-go` to 0.57.0 and `github.com/mattn/go-isatty` to 0.0.24. (#259)
* **Docs:** Refresh post-v0.14.7 current-state facts; remove the short-lived OpenWiki workflow while retaining local agent-history ignores. (#255, #256, #261, #263)

## v0.14.7 (2026-07-24)

### 🚀 Features
* **AI:** Operator-local `~/.axis/ai.yaml` backends and roles with health probes and prefer-order dry-run routing (`axis ai backends|roles|route`). (#251)
* **AI:** Strict model listing on non-empty catalogs (exit code 7); MCP read-only tool `inference_route_explain`. (#252)
* **AI:** Wire roles into placement advisory reasoning and agent catalog/startup (`--role`); document `ai.yaml` vs `ai_providers` vs `chat.default_model`. (#253)

### 🔧 Maintenance
* **CI:** Bump `actions/setup-go` from 6.5.0 to 7.0.0. (#249)
* **Deps:** Bump `github.com/mattn/go-isatty` from 0.0.22 to 0.0.23. (#250)

## v0.14.6 (2026-07-17)

### 🐛 Bug Fixes
* **Transport:** Scope SSH-agent sockets to a single authentication handshake and close them on success or failure, preventing periodic cluster polling from exhausting the desktop keyring daemon's file-descriptor limit. (#245)

## v0.14.5 (2026-07-16)

### 🐛 Bug Fixes
* **CLI:** Harden `axis update`: default remains a bounded self-update of the running install; report PATH/common shadows; `--all` updates other installs only after per-target AXIS identity (`debug/buildinfo` exact module path), version (no silent downgrade; unknown version allowed only for explicit `--path`/self), package-manager path checks, and symlink-safe replace of the resolved target. `--check` reports multi-install staleness without redundant hints. (#242)

### 🔧 Maintenance
* **Release truth:** Retry the same public GitHub release endpoints without credentials when authenticated API requests are unavailable, while continuing to fail on missing or unpublished releases. (#242)

## v0.14.4 (2026-07-16)

### 🚀 Features
* **Doctor:** Skip remote SSH and shell probes for the local node (avoids false pubkey fail on self Tailscale IP). (#239)
* **Config:** `Normalize()` (StableID only); pure `Validate()`; authored `hostname` never synthesized from `endpoints[]`; `NodeConfig.IsLocal()` is the single endpoint-aware locality API. (#239)
* **Facts:** One-shot remote fact bundle (single bash session) with legacy multi-probe fallback — core + thermal/storage/tools coverage, far less sensitive to slow login shells (e.g. fish+conda). (#238)
* **Facts:** Portable bash launcher for remote probes (`command -v bash` + FHS + NixOS paths); no hard-coded `/bin/bash`. (#238)
* **Config:** `collect_timeout_sec` / `dial_timeout_sec` (defaults: collect floor 45s, dial inherits `timeout_sec`); optional `endpoints[]` for LAN+Tailscale dial targets with fallback; `SSHDialSpec()`. (#238)
* **Config:** `MembershipFingerprint()` for stable cluster membership identity (name/role/user). (#238)
* **Models:** `PartialReasons` + `FormatPartialReasons` for probe-level partial diagnostics. (#238)
* **Doctor:** Membership fingerprint in config check; mDNS `.local` seed warning; dial/collect timeout display; remote shell cost probe (slow login shell advisory). (#238)
* **Transport:** Dial fallbacks + `ConnectedHost()`; handshake bounded by dial timeout (not full collect context). (#238)
* **Agent:** Inject nearest `AGENTS.md` into the system prompt. (#237)
* **Agent:** Bind model selection to endpoint and protocol. (#235)

### 🐛 Bug Fixes
* **Daemon/mesh:** Seed peers from `PrimaryHostname()` so endpoints-only nodes stay in gossip fan-out after Hostname backfill removal. (#239)
* **Config:** Load→edit→Save preserves endpoints-only YAML (no synthetic node `hostname`); `NodeConfig.IsLocal()` used by doctor, discovery, daemon, execution, and reservations. (#239)
* **Discovery:** Use collect timeout (not short dial timeout) as the full remote fact budget so multi-probe/slow-shell nodes complete instead of silent partials. (#238)
* **Transport:** Endpoint fallback uses logical SSH alias names (preserves HostKeyAlias/IdentityFile); dial timeout caps handshake so stalled peers do not burn collect budget. (#238)
* **Facts:** Bundle path fills ThermalState/ThermalZones (placement safety), tool versions, and mapper-aware storage when needed; SSHTarget tracks connected endpoint after fallback. (#238)
* **Execution/MCP/Agent/Chat:** Propagate `endpoints[]` dial fallbacks through guarded exec, MCP, agent remote tools, and chat tunnel routing. (#238)
* **Agent:** Wire `shell` and `run_on_node` through guarded execution. (#236)

## v0.14.3 (2026-07-15)

### 🚀 Features
* **CLI:** Professionalize `axis init` onboarding — first-run vs update flows, discovery paths, validated atomic config saves with backups. (#230)

### 🐛 Bug Fixes
* **Events:** Harden event log isolation and flush; restore JSONL decoder; non-fatal init host:port duplicate handling. (#229, #230 follow-ups)
* **Static analysis:** Resolve lint/static-analysis warnings including safe JSON-RPC request ID marshaling. (#226)
* **UI:** Resolve repeating title bug in terminal select menu. (#222)

### 🔧 Maintenance
* **Release:** Harden release workflow dry-run, gate parity with CI, strict version parse, asset checksum verification. (#231)
* **Docs/agents:** Restore thin Copilot entry point; fix AGENTS.md mesh/reservation wiring claims; sync dependencies with go.mod. (#227, #228)
* **Architecture:** Invert L4→L5 dependencies in safety/execution. (#225)
* **Daemon:** Lock-free snapshot reads and hardening scaffolding. (#223)
* **Deps:** Bump go-modules minor/patch group. (#224)

## v0.14.2 (2026-07-12)

### 🚀 Features
* **Transport:** SSH multipath routing with endpoint authority. (#213)
* **Agent:** Endpoint authority and resident model picker. (#212)

### 🐛 Bug Fixes
* **Multipath:** Activate multipath routing + resident-model ports; filter Docker bridge IPs and fix `probeSSH` return. (#214, #219)
* **API:** Require bearer-token auth on `/debug/pprof` endpoints. (#216)
* **State/API:** Harden state updates and API safeguards. (#220)
* **Error handling:** Propagate errors that were silently swallowed. (#217)
* Close P0 residuals (daemon restart, PR helper, docs). (#211)

### 🔧 Maintenance
* **Refactor:** Consolidate `~/.axis` path and atomic-write duplication into `persist`. (#218)
* **Tests:** Cover previously untested internal packages. (#215)
* **Docs:** Post-release current-state facts (v0.14.1). (#210)

## v0.14.1 (2026-07-10)

### Fixes
* **Placement network class:** Classify by SSH dial target (`NodeFacts.SSHTarget`), not observed machine hostname. LAN-reached nodes no longer take a false Tailscale/VPN −20 penalty when overlay interfaces are present. Tailscale IPv6 ULA and public-dial edge cases handled. (#206)
* **make lint fail-closed:** Propagate `gofmt` process failures so a missing/crashing gofmt cannot pass CI. (#208)
* **CI:** Run push CI only on `main` (stop double Test & Build on in-repo PR pushes); invoke `make lint` in CI; remove permanently disabled Claude review workflow. (#207)

### Maintenance
* **`make install-user`:** Install to `~/.local/bin` with commit/date ldflags (matches operator `axis update` path). (#207)
* **Release truth:** Drop live `published_at` timestamps from generated `docs/current-state.md` facts; disable broken auto-refresh workflow that could not open PRs under branch protection. GitHub Releases remain authority. (#207)
* **Docs:** Honest merge-policy description; tag-only GoReleaser release process; daemon restart guidance. (#207)
* **`hack/pr-review-cycle.sh`:** Fail-closed required-check helper for the solo-operator PR loop (collection only; no auto-merge). (#208)

## v0.14.0 (2026-07-09)

### 🚀 Features

* **Agent Harness (P0–P3):** Turn `axis agent` into a distributed, cluster-native agent harness (32 tools, was 13).
  * Parallel tool dispatch within a turn (bounded worker pool); results append in tool-call order. LLM-backed context compaction at 70% of the token budget.
  * Robust editing: `multi_edit` (batch edits to one file, atomic on failure), `edit_file` `replace_all`, `undo_last`/`review_changes` via a session checkpointer.
  * In-session plan tracking (`todo`) and conversation branching (`branch_session`/`rollback_session`).
  * `symbol_search` (Go-AST-aware definition lookup + generic fallback), `web_fetch`/`web_search`.
  * Remote tool surface: `run_on_node`, `remote_read_file`, `remote_grep`, `remote_list` — the cluster as an extended workspace.
  * Cluster-aware context: a live cluster snapshot (node health, free RAM, resident models) injected into the system prompt every turn.
  * Distributed sub-agents: `spawn_subagent` delegates a focused sub-task to a child agent running its own tool loop on a target cluster node.
  * Autonomy modes (`--autonomy default/edit/full`) and multi-model routing (`--cheap-model`).
  * Background/async tasks: `run_background`/`check_task`/`list_background_tasks`.
  * Cortex as native cluster memory + coordination: when Cortex MCP tools are connected, the agent proactively recalls, remembers, locks shared files, and publishes events.
* **Placement:** Route-based network classification — a node reached over a private LAN address is classified `direct-lan` even if it also carries a Tailscale/VPN interface (previously penalized −20 for the overlay interface alone). Handles IPv4-mapped IPv6 and IPv6 ULA.

### 🔧 Maintenance

* Bump Go toolchain to 1.26.5 (fixes GO-2026-5856, Encrypted Client Hello privacy leak in crypto/tls).

## v0.13.0 (2026-07-07)

### 🚀 Features

* **Routing & Placement:**
  * Implement Intelligent Auto-Routing for `axis chat` with zero-latency SSH tunneling and `modelWarmthRank` support.
  * Implement v2 placement endpoints.
* **Execution & Safety:**
  * Add `--expose-port` flag for task execution.
  * Swap old blocker with the new Structured Safety Engine.
* **UX & Networking:**
  * Zero-config Tailscale auto-discovery for cluster setup.
  * Agent REPL UX improvements.

### 🔧 Maintenance

* Update Go dependencies.
* Safety/speed class allocations optimizations.

## v0.12.3 (2026-06-29)

### 🚀 Features

* **Network & Topology Enrichment:**
  * Wire local identity and ignore docker bridges.
  * Add secondary disk probing.
  * Measure explicit network `speed_class` via sysfs to improve deterministic classification across hosts.

### 🐛 Bug Fixes

* **Parser & Facts Refinement:**
  * Normalize timestamps to UTC to resolve cross-timezone issues.
  * Add execution timeout to the local `df` command and fix trailing space parsing for mount points.
  * Optimize `shellSplit` allocations and improve word tracking logic in the structured safety parser.

### 🔧 Maintenance

* Dependency bumps: `github.com/mark3labs/mcp-go` (v0.55.1), `actions/checkout` (v7.0.0), `actions/setup-go` (v6.5.0).
* Documentation: sanitize local paths in `session-handoff.md` and reconcile stale facts.

## v0.12.2 (2026-06-19)

### 🚀 Features

* **Distributed Model Planner (PR #175):**
  * Snapshot-backed multi-node pipeline and single-node advisory placement planner.
  * Support for exact layer memory mapping, context window cache scaling, and freshness validation.
* **Interactive CLI UI:**
  * Added interactive selection dropdown UI, live remote Ollama probing, `/mcp` diagnostics, and active SSH verification during initialization.

### 🐛 Bug Fixes

* **Planner Tie-Breaker Logic:**
  * Fixed `betterLink` selection logic bug where alphabetical sorting on identical link qualities incorrectly overrode downstream candidate memory checks (`betterCandidate`).
* **Test Stability:**
  * Resolved an asynchronous race condition in the MCP lifecycle event test suite (`TestLifecycleEventTools`).

## v0.12.1 (2026-06-18)

### 🔒 Security

* `axis agent` now classifies LLM backends as local or remote and restricts untrusted evidence for remote backends.
  * Added `BackendSecurityClass` to `agent.Config` and `Agent`; constructor code paths must declare locality explicitly.
  * Remote backends receive only structured skill metadata (ID, success count, timestamps, preferred node, node success counts); free-form descriptions, decision summaries, and raw commands are excluded.
  * Local backends continue to receive descriptions and decision summaries; raw command text is redacted by default and only included when `--allow-raw-command-evidence` is set.

### 🔧 Internal

* Encapsulated `ToolContext` in `internal/agent`: the current runtime view is stored in an unexported `atomic.Pointer` and reloaded atomically via `NewToolContext`, `Current`, and `ReloadCurrent`. Incomplete reloads preserve the previous view.
* Hardened `axis llm configure` and `axis llm select` key/config file transactions:
  * Symlink rejection via `os.Lstat`.
  * `O_CREATE|O_EXCL` temporary files with `fsync`, atomic rename, and parent-directory `fsync`.
  * Advisory file locking on `nodes.yaml.lock` and SHA-256 hash check to detect concurrent modification.
  * `llm configure` rolls back only a newly created key file when YAML validation fails; pre-existing keys survive.
  * Inferred cloud provider `kind` is persisted into `nodes.yaml` AST automatically.
* Fixed portability and resource-handling issues from PR 173 review:
  * Replaced direct `syscall.Flock` with portable `internal/lockutil` package.
  * Closed temporary files before removal to support Windows cleanup.
  * Always close HTTP response bodies and defer context cancellation in the `/reservations` slash handler.
  * Rewrote command redaction with a single-pass parser and optimized payload truncation.
  * Added a nil guard for runtime snapshots during guarded execution preparation.

## v0.12.0 (2026-06-17)

### 🚀 Features

* **Cloud LLMs + premium terminal UI/UX (#171):** Support cloud LLM backends with local capability checks and a premium terminal UI/UX.
* **Reservations doctor (#169):** `axis reservations doctor` diagnoses reservation inconsistencies, stale leases, and memory leaks.
* **Pressure-aware lease-based cluster RAM balancer (#166):** Scheduler shifts from instantaneous free memory to allocatable headroom, leased soft-claims, and Linux kernel PSI.
* **Cluster topology, interactive LLM select, nested parent reservations (#165).**
* **Git-aware interactive preflight checks for dirty trees (#164).**
* **Dynamic local-port forwarding over SSH for remote tasks (#163).**

### 🔧 Maintenance

* Make stash preflight test hermetic; refresh docs (#168).
* Make LLM select command tests hermetic (#167).
* Bump go-modules-minor-patch group with 2 updates (#170).

## v0.11.0 (2026-06-10)

### 🚀 Features

* **Structured AXIS lifecycle events + flock rotation (#160).**
* **Git Intelligence for workspace context (#156).**
* **`axis init` CLI command, mesh gossip peer diagnostics, and topology (#161).**
* **Generic SSH connection latency and path classification (#159).**
* **Per-session MCP cache and daemon snapshot hook deadlock fix (#153).**
* **Ollama warmth lifetime scoring as a bounded placement tiebreaker (#151).**

### 🐛 Bug Fixes

* Print CLI execution errors to stderr before exiting (#157).
* Resolve daemon data race and wire MCP snapshot cache invalidation (#155).

### 📚 Documentation

* MCP defense-in-depth paragraph and assertion test (#150).
* Update agent worklog (#158).

### 🔧 Maintenance

* Bump Go toolchain 1.26.3 → 1.26.4 (#154).
* Bump actions/checkout 6.0.2 → 6.0.3 (#152).

## v0.10.9 (2026-06-01)

### 🚀 Features

* **Phase 1 advisory leases and structured safety evaluator (#144):** Adds the Triangle advisory lease MCP tools (`triangle_request_lease`, `triangle_release_lease`, `triangle_heartbeat_lease`) and the structured safety evaluator.
* **Per-node configurable system RAM reserve (#147).**

### 🐛 Bug Fixes

* Resolve PR #145 post-merge bugs in api/v2 and mcp (#146).
* Make `TestLocalCollectorCollectsFacts` hermetic (#148).
* Correct release badge to track tag pushes (#143).

### 🔧 Maintenance

* Add safety benchmarks and enable structured evaluator tests (#145).
* Version bump to v0.10.9 (#149).

## v0.10.8 (2026-05-29)

### 🚀 Features

* **Unified MCP client (#140):** prompts, caching, retry, batch, REPL, metrics, and auto-routing.
* **Execution observations in `axis task run` + `axis observations` CLI (#139).**

### 🔧 Maintenance

* Bump `github.com/mark3labs/mcp-go` 0.54.0 → 0.54.1 (#141).
* Release v0.10.8 (#142).

## v0.10.7 (2026-05-22)

### 🚀 Features

* New `axis daemon mesh` subcommand for operator mesh introspection.
  * Queries the daemon's `/mesh` endpoint and displays active gossip peers in a table.
  * Shows peer name, hostname, state, source, and relative last-seen time.
  * Handles empty peer lists and "mesh not available" gracefully.

### 🔧 Internal

* Added `Mesh() *mesh.Mesh` to the `daemon.SnapshotCache` interface and implemented it on `*Daemon`.
* Added `/mesh` handler to the daemon router (`internal/daemon/handlers.go`).
* Added `MarshalJSON`/`UnmarshalJSON` to `mesh.PeerState` so the API serializes states as human-readable strings (`"discovered"`, `"verified"`, `"trusted"`, `"suspect"`, `"dead"`).
* Refactored daemon HTTP request creation into shared `newDaemonRequest` helper with consistent auth handling.

## v0.10.6 (2026-05-22)

### 🚀 Features

* Surface hidden hardware facts in `axis facts` and `axis status`:
  * `axis status` table expanded with **STORAGE** and **GPU** columns.
  * `axis facts` now shows storage class, GPU details (vendor, VRAM, capabilities), thermal state, power source, battery level, load averages, memory topology, and network addresses with interface/speed class.
  * Refactored GPU name formatting into shared `formatGPUBaseName` helper to ensure consistent vendor redundancy handling and "unknown" filtering across commands.

## v0.10.5 (2026-05-22)

### 🚀 Features

* `axis chat` and `axis agent` UX improvements:
  * Signal context wiring (Ctrl+C interrupts all modes: single-shot, resume, REPL).
  * `--verbose` flag prints model auto-detection, turn progress, and tool parameters.
  * `--dry-run` flag on `axis agent` skips tool execution while preserving reasoning loop.
  * Cobra output compliance (`--no-color`, redirection) across all chat/agent handlers.
* Placement explain now displays **headroom** (allocatable minus reserved) per candidate.
* New Copilot CLI skill: `.github/copilot/skills/pr-review-responder.yml` — codifies the PR lifecycle workflow (prepare → push → monitor → fix → respond → verify).

### 🔧 Maintenance

* Upgrade `golang.org/x/crypto` v0.51.0 → v0.52.0 to resolve `govulncheck` findings (GO-2026-5013 through GO-2026-5021).

### 📚 Documentation

* Refresh `docs/current-state.md` for v0.10.5 release.

## v0.10.4 (2026-05-20)

### 🔧 Maintenance

* Extend resident-model VRAM probes to llama-server and MLX backends. `LlamaServerDiscoveryScript` now stats the model file to compute `size_vram_mb`; `MLXDiscoveryScript` now queries process RSS to compute `size_vram_mb`. Previously only Ollama populated this field.

### 📊 Observability

* `axis summary` now displays allocatable RAM alongside reserved RAM, making the cluster RAM accounting model explicit to operators. Uses `snap.Summary.TotalAllocatableMB` computed by the reservation overlay.
* `axis status` table now shows **allocatable RAM** as the primary metric (replacing raw "RAM FREE"). When a node carries active reservations, the reserved amount is shown in parentheses (e.g. `6144 MB (1024 reserved)`). Falls back to raw free RAM when the reservation overlay has not been applied.

### 🔧 API & Doctor

* `POST /v2/reservations` now returns `405 Method Not Allowed` instead of `501 Not Implemented`, making the read-only API contract explicit per `docs/decisions/v2-reservations-endpoint.md`.
* `axis doctor` now probes the **Ollama** local AI backend alongside llama-server and MLX, ensuring parity with the primary inference backend used throughout the project.

## v0.10.3 (2026-05-18)

### 🐛 Bug Fixes

* Prefer tool-capable models when auto-selecting for `axis agent` — prevents 400 Bad Request when the alphabetically first installed model (e.g. `gemma3n:e2b`) does not support Ollama tool calling. Falls back to known tool-capable families (llama3.1, qwen3.5, qwen3, etc.) and skips embedding/vision variants (PR #127).

### 🔧 Maintenance

* Extract `state.Maintain()` from `state.Load()` — eliminates repair-on-read side effect, making `Load()` idempotent and preventing silent `state.json` rewrites on every CLI invocation (PR #126).
* Update summary golden files to reflect version bump.

### 📚 Documentation

* Add `docs/roadmap-status.md` — final status of all 53 v9 roadmap items (48 done, 5 Phase G items blocked by evidence discipline).

## v0.10.2 (2026-05-17)

### 🚀 Features

* Daemon health endpoints (`axis daemon status`) with reservation count and last-refresh timestamp.
* Placement ranking 54% faster for 50-node clusters via unified memory caching.

### 📚 Documentation

* Refresh `docs/current-state.md` for v0.10.2 release.

## v0.10.1 (2026-05-06)

### 🚀 Features

* Integrated reservation-ledger-wiring enhancements:
  * Power source detection (AC/battery) for better placement decisions
  * Thermal state monitoring to avoid throttled nodes
  * Thermal zones enumeration for detailed thermal monitoring

### 📚 Documentation

* fac90d9: chore: refresh current-state.md for v0.10.0 release

### 🐛 Bug Fixes

* None in this release

### 🔧 Maintenance

* Version bump to 0.10.1

## v0.10.0 — Operator-honest groundwork: shell safety, reservation ledger, mesh scaffolding

**Shell quoting vulnerability fix (PR #96)**

Remote cleanup traps in `runRemote` used `trap 'rm -f QUOTED_PATH' EXIT`, which
created a nested quoting interaction: `shellescape.Quote` wraps paths in single
quotes, and a single-quoted path containing a single quote produces an unparseable
trap body. Replaced with variable assignment pattern
`_axis_ctx=QUOTED; trap 'rm -f "$_axis_ctx"' EXIT`, which eliminates the nesting
entirely. The heredoc delimiter changed from `EOF` to `AXIS_EOF`. An adversarial
test suite covers paths with spaces, single quotes, dollar signs, backticks,
and semicolons.

**Cobra error handling overhaul (PR #96)**

`os.Exit` and `Fatal` calls in Cobra `RunE` handlers skip Cobra's cleanup. Added
`ExitCodeError` type carrying both an exit code and a user-facing message, with
`errors.As`-based unwrapping. Root command now uses `SilenceErrors`/`SilenceUsage`
to prevent double-printing. All `RunE` handlers in placement, task, agent, and
chat commands converted. `Fatal()` marked as deprecated.

**v0.10.0 groundwork (PRs #94, #95)**

* `POST /v2/batch/place` returns `501 Not Implemented` instead of synthetic `200 OK`
* Reservation accounting fails closed when node capacity is unknown
* Structured safety learned approvals deliberately disabled (program-name-only too broad)
* Mesh gossip remains internal scaffolding; HMAC present, replay protection not enforced
* Dashboard/rendering helpers present but not registered as CLI commands
* Release pipeline and GoReleaser improvements

**AX-005/006/007/024/025 integration (PR #93)**

* Link-local addresses tagged with `scope: "link-local"` instead of silently dropped
* SSH `IdentitiesOnly yes` from config now respected (skips agent, default keys)
* `ssh -G` passes `-F` for correct config file resolution on macOS
* Cached-reads doctrine documented: explicit, operator-facing, no hidden fallbacks
* Daemon staleness threshold configurable (default 5 min)

---

## v0.9.0 — Cortex MCP client, hybrid AI router, VRAM observation

**`axis cortex` MCP client (PR #88)**

New command connects to the AXIS Cortex cluster brain via MCP protocol, supporting
tool discovery, resource listing, and prompt execution. Aligns with FastMCP 3.x
Streamable HTTP protocol. Timeout increased to 45s for recall operations.

**Hybrid AI router (PRs #84–#87)**

Three-phase `axis llm` implementation: provider registry + config + model listing
(Phase 1), semantic reflex classification + `axis llm` command (Phase 2), cloud
provider module with OpenRouter/Groq/Anthropic support + secrets management (Phase 3).
Local model auto-selection when no model is recommended.

**Ollama VRAM observation (PR #76)**

Resident model VRAM usage surfaced in `axis status` output column. Unknown VRAM
shown explicitly rather than silently omitted.

---

## v0.8.0 — Empirical placement arc + multiruntime resident models + doctor AI checks

**Empirical placement arc (PRs #66–#71)**

The v0.8.0 release lands the full empirical placement arc. Prior releases tracked
execution observations but only used them as soft ranking bonuses. v0.8.0 makes
empirical history load-bearing:

* **Per-model observation scoping** (#69): `ObservationScope` now carries a
  `ModelName` field so different models on the same node accumulate independent
  peak-RAM histories. Observation key derivation uses SHA-256 over the base scope
  fields (node, workload class, backend, tool), conditionally extending the hash
  input with model name when known to prevent cross-model contamination while
  preserving existing keys for unscoped observations.

* **MLX resident model detection** (#70): `axis facts` and cluster snapshots now
  include models served by `mlx_lm.server` alongside the existing Ollama collector.
  MLX models are discovered via the `/v1/models` HTTP endpoint and tagged with
  `runtime: mlx`, `source: mlx-lm-api`.

* **Hard `PeakRAMMB` pre-filter** (#71): `FilterCandidates` now excludes any node
  whose freshly-observed `PeakRAMMB` exceeds the node's current allocatable RAM
  before the ranking phase begins. The filter short-circuits on stale or missing
  observations (safe default: allow). `inferenceModelName` is hoisted outside the
  per-node loop to avoid repeating model-name extraction/matching for each node.

**`axis status` resident model display (PR #72)**

`axis status` now renders a **RESIDENT MODELS** table when at least one node has
live resident models. Rows are ordered node-first, then by runtime in canonical
order (ollama → llama.cpp → mlx → apple-foundation-models), with unknown runtimes
sorted alphabetically for deterministic output. Model lists exceeding three entries
are truncated with a `+N more` suffix. Runtime labels are colour-coded (ollama:
green, llama.cpp: yellow, mlx: cyan, apple-fm: green).

**`axis doctor` AI backend health checks (PR #73)**

`axis doctor` now probes local AI backends as advisory checks:

* `llama-server` and `mlx` are probed via the same discovery scripts used by
  `axis facts`, keeping the doctor and fact-collection views consistent.
* Each probe reports installed / running / port / model-count state.
* Probe errors (e.g. `bash: command not found`) surface `stderr` for actionability
  instead of emitting an opaque exit-code message.
* Each backend gets an independent 5-second timeout derived from the command
  context, preventing a slow first probe from starving the second.
* `--strict` flag now also promotes daemon-cache failure to a core failure (existing
  behaviour documented and tested in this release).

**Earlier arc PRs in this series**

* **#66** — Exact-scope execution observations separate from failure memory
* **#67** — `freshObservation` scoping helper and ranking integration
* **#68** — TurboQuant-aware backend grading for long-context placement hints

---

## v0.7.0

See GitHub release notes: <https://github.com/toasterbook88/axis/releases/tag/v0.7.0>
