# AXIS Release Notes

**Canonical reference:** [`CHANGELOG.md`](../CHANGELOG.md)

**GitHub Releases:** https://github.com/toasterbook88/axis/releases

This document provides user-friendly summaries of recent AXIS releases, highlighting key features, bug fixes, and upgrade considerations.

---

## v0.22.0 upgrade guide

This section describes the v0.22.0 source candidate. Consult
[GitHub Releases](https://github.com/toasterbook88/axis/releases) for publication
and downloads; a versioned changelog is not proof of publication. Changes since
v0.21.0 are listed in the [changelog](../CHANGELOG.md#v0220-2026-10-28).
There is no required YAML migration. These changes can alter what an existing
setup sees.

### External MCP tools ask in every mode

An MCP tool that is not listed by exact name under
`mcp_servers.<server>.auto_approve_tools` in `nodes.yaml` now prompts in
`edit`, `full` and with `--auto-approve`, as it already did in default mode.
To keep a tool running without a prompt, list it there. `full` mode is now
"all but safety-blocked and unlisted MCP".

### `--auto-approve` works in the console again

The console dropped `--auto-approve` at startup, so default mode asked about
every tool. Built-in tools that score below 70 now run without a prompt again
when the flag is set.

### `axis ai route` can now refuse a listed model

For a LiteLLM-style hub on the same machine, a route now also needs the server
behind the model to answer. A model whose upstream is down is skipped; with no
other backend, `axis ai route` exits 7 (`ExitErrModelUnlisted`) with
"(listed, but no upstream answers)" where it used to report the model healthy.
Scripts that read the exit code or JSON see this as "not available", which is
what a request to that model would have found. The `upstreams` field lists
each check. Hubs on another node and hubs without `/model/info` behave as
before and say "upstream not checked".

### The HTTP API needs a token on TCP

`ServeWithContext` refuses a TCP address when the API token is empty.
`axis serve` always loads or generates a token, so normal installs are not
affected; only code that served TCP with an empty token is.

### New, opt-in

- `axis agent --autonomy cluster` (or `/autonomy cluster`) answers cluster
  questions with only the AXIS cluster tools; workspace, git and MCP tools are
  hidden.
- `--objective spread` on `axis task place` and `axis placement explain` (and
  the matching HTTP, MCP and agent fields) ranks by reservation balance.
  `capacity` stays the default.

### New warnings

- `axis doctor` on Linux warns about services in a restart loop now. Its fix
  line names the unit's journal.
- `axis doctor` counts every axis daemon on the host for the mesh port check
  and warns about system units that start an axis daemon outside the user
  service. A root daemon left by an earlier `sudo axis update` shows up here;
  stop it by hand. `sudo axis update` no longer starts one.

### VRAM observations

Task history and execution observations now record only GPU memory used by the
task's own processes. Earlier records hold 0 (an ordering bug meant VRAM was
never stored), and 0 still means "not observed" when per-process accounting is
unavailable.

---

## v0.21.0 upgrade guide

This section describes the v0.21.0 source candidate. Consult
[GitHub Releases](https://github.com/toasterbook88/axis/releases) for publication
and downloads; a versioned changelog is not proof of publication. Changes since
v0.20.0 are listed in the [changelog](../CHANGELOG.md#v0210-2026-10-23).
There is no required YAML migration.

### Cortex: select the service independently of SSH

`AXIS_CORTEX_URL` overrides node-derived Cortex addressing for `status`, `recall`
and `events`, without requiring `nodes.yaml`. For example:

```sh
AXIS_CORTEX_URL=https://cortex.example.test/mcp axis cortex status
AXIS_CORTEX_URL=https://cortex.example.test/mcp axis cortex recall "release procedure"
```

These are documentation endpoints; substitute the service you administer. A URL
with no path, or only `/`, gets `/mcp`. Only HTTP(S) is accepted. Invalid nonempty
values, including whitespace-only values, fail rather than silently choosing a
node. Credentials, query strings and fragments are rejected in the URL; keep
using `AXIS_CORTEX_SECRET` or `~/.axis/cortex.token` for authentication. HTTPS
uses the system trust store, and existing timeouts and cancellation still bound
requests. An unset or empty override preserves node selection and its legacy
`http://<hostname>:8200/mcp` address.

Installing the executable does not set this environment variable for a shell,
agent process or daemon service. Set it in each intended execution context and
verify that context. Do not change a node's SSH identity to repair a service URL.
A successful tools listing proves MCP reachability, not recall quality or the
health of every operation; check native `axis cortex status` and a representative
read-only `axis cortex recall` separately.

With an explicit MCP endpoint, the optional `AXIS_CORTEX_QDRANT_URL` supplies a
Qdrant REST base URL for counting `cortex_memories` points. If it is unset, the
count is unknown. Failed requests and missing/null `points_count` also produce
`memories_unknown: true`, on both explicit and legacy paths; text shows unknown.
JSON retains `memories: 0` in that case, which must not be interpreted as an
observed empty store. A successfully observed numeric zero remains known.
The additive JSON fields are `endpoint` and `memories_unknown` (the latter is
omitted when false). A healthy MCP status can coexist with an unknown count.

### Agent safety and conversation recovery

Choosing **always** in a tool approval now approves that tool for the session,
including inherited sub-agent approvals. Other tools still follow their own
approval policy; **never** blocks previously approved tools too. Safety-blocked
commands continue to require confirmation. External MCP tools display
**NOT RATED** with an explanation, because their annotations are not a risk
assessment or authorization. Private `approval` events include the call ID and
decision; the decision can come from an autonomy policy, not only a human click.

Empty tool output is stored as `(tool returned no output)`. Empty user/tool
messages in existing or resumed history receive placeholders when requests are
built, preventing empty-content rejection from breaking every later turn.
Assistant messages containing only tool calls retain their valid representation.

The session-start cluster summary and Git context travel with the newest user
message instead of changing the stable system prefix. They remain session-start
observations; this change performs no fresh fleet or Git collection. Today's
local date is refreshed on each request. Cache reuse and latency depend on the
backend and workload; measure server-reported reuse before claiming a speedup.

### Streaming usage and private log readers

OpenAI-style streamed requests ask for `stream_options.include_usage`. A 4xx
rejection naming that option is retried once without it, and that backend stops
sending the option. Other errors retain their failure behavior. Usage is read
from chunks with choices as well as empty-choice chunks; the final usage report
is counted once per reply rather than added repeatedly.

Each new private `turn.finished` record has `usage_reported`. True means nonzero
reported usage reached the agent. False means absent counters are unknown cost;
the current integer interface also maps an explicit zero/zero report to false.
Older log records lack the field, so consumers must treat absence as unknown.
Backend-internal estimates do not become observed log usage.

Private session logging and the bare `axis` terminal entry point were introduced
in v0.20.0 and remain in effect: bare `axis` on a TTY opens the agent console,
`axis tui` opens the dashboard, `axis agent --plain` selects the line reader,
and a non-TTY bare invocation prints help. Logs remain local and unencrypted,
with owner-only directories/files, credential-pattern masking and bounded
retention (8 MiB segments, current day plus 29 preceding days, 1 GiB per log
directory). Masking cannot recognize every secret. Keep logs private and inspect
truncation and omission markers; see the [logging runbook](runbooks/agent-session-logs.md).
Hardware facts and macOS storage representation are unchanged in this release.

### Installation and verification boundary

AXIS source builds remain on **Go 1.26.10**. The pinned GoReleaser validator uses
its separate compatible toolchain; it does not change the AXIS build compiler.
Before tagging, release preparation must be merged, and both the clean release
preflight and the nonpublishing cross-platform workflow must pass on that exact
merged SHA. Native macOS console acceptance is separate from cross-compilation.

When a download is published and installation is approved, verify the executable
path and `axis version --local`, inspect shadows with `axis doctor`, then compare
the daemon commit and readiness in its actual service context. Installation and
service restart are separate actions; `axis update --no-restart` avoids its
automatic daemon restart. Preserve the replaced binary and metadata for rollback.
A binary rollback does not rewind configuration, history or logs.

---

## v0.20.0 upgrade guide

[v0.20.0](https://github.com/toasterbook88/axis/releases/tag/v0.20.0) was published
from commit `951550e`. GitHub Releases is the authority for available downloads. The [versioned changelog](../CHANGELOG.md#v0200-2026-10-22)
lists every change group since v0.19.6. There is no required configuration migration.

### Terminal entry point and agent functions

Bare `axis` on an interactive terminal now opens the transcript agent console,
with a start screen and background startup work. Use **`axis tui`** to keep opening
the cluster dashboard, **`axis --help`** for help, and **`axis agent --plain`** for
the line-oriented agent. Bare `axis` without a TTY still prints help, preserving
scripts that request it. Update terminal aliases or launchers that relied on
bare `axis` opening the dashboard.

The start screen can appear before a model is ready. Model availability still
comes from observed/configured backends; the screen does not start a model or
prove backend health. Use `/model` to select a configured choice. Escape cancels
an active turn; `/exit` and `/quit` close the console even while startup is pending.
A backend change takes effect at a model-round boundary, preserving the matching
security classification for that round's context and request.

Per-turn evidence and snapshot context now accompany the newest user request,
leaving a stable system/tool prefix eligible for prompt-cache reuse. Backend
cache reuse and timings depend on the server and workload. Updated cached
snapshot age does not mean a fresh fleet probe; `--live` explicitly requests one.
External MCP tools require approval unless their exact names are allowlisted.

### Private session logging: new local data

CLI agent sessions (console, plain and one-shot modes) now write development
trajectories beneath `~/.axis/logs`, or `AXIS_HOME/logs` when a state root is
selected. Prompts, answers, tool arguments/results, timing, outcomes, reported
usage and build/provider/model attribution may contain sensitive project data.
Logging is automatic for CLI sessions; this release adds no CLI opt-out flag.
Embedded callers log only when supplying `Config.SessionLog`.

* Directories are `0700`; segments and lock files are `0600`. This is local
  access control, **not encryption**. Legacy persisted stores are tightened when
  opened/replaced; workflows reading another account's files may need adjustment.
* Credential-shaped content is masked before serialization, but arbitrary secrets
  cannot always be recognized. Do not commit or upload runtime logs by default.
  No transcript is automatically sent to Cortex, HTTP, MCP or lifecycle events.
* Logger-owned segments retain the current day plus 29 preceding local calendar
  days and at most **1 GiB per log directory**. They rotate at **8 MiB**;
  prompts/answers cap at 256 KiB, tools at 16 KiB, and encoded records at 1 MiB.
  Age/byte cleanup runs on append, not while idle, and does not prune unrelated
  task logs or history. Multiple state roots each have their own budget.
* Logging is best effort: failures warn and later successful records report
  omissions. Truncation, retention and crashes can make a trajectory incomplete.
  Missing backend usage is unknown cost, not a measured zero.

Review state-directory placement, backups and disk budget before using the agent
with confidential material. The [logging runbook](runbooks/agent-session-logs.md)
documents captured fields, masking limits, locking and recovery behavior.

### Hardware facts and storage interpretation

```sh
axis facts
axis facts -hw
axis facts --hardware --format json
axis facts --hardware --watch --interval 2s
axis facts --help
```

Ordinary text facts are grouped for scanning. Hardware reports add CPU, memory,
GPU, storage, network, system/firmware, sensors and attached devices on Linux and
macOS. Each reading includes its source, observation time and status; rates carry
sampling windows. `--watch` keeps inventory and refreshes measurements, emitting
JSON lines or separate YAML documents. Availability depends on tools, device
support and permissions; AXIS never invokes sudo to fill gaps.

Consume `status` and `reason` before interpreting a value. A SMART permission
failure is **unknown health**, not a failing disk. Unsupported telemetry stays
unavailable, and Intel/NVIDIA GPUs do not acquire AMD-only sysfs readings.

On macOS, `profiler parent` records provider JSON containment. A nested
`physical_drive` object is a **provider description**, not proof of a distinct
physical disk. Its name comes from `device_name` or `media_name`, with an explicit
fallback and selected provider metadata retained. A volume's `parent` is observed
only when the capture supplies an explicit backing BSD identity; otherwise the
reading is unavailable with a reason. Do not infer APFS/physical topology from
profiler nesting. Non-storage `parent` output retains its existing meaning.

### Other compatibility and reliability changes

* Empty CLI JSON lists are arrays (`[]`), so consumers should no longer require
  `null` for an empty result.
* Discovery deadlines, scoped/cached endpoint sweeps and public-health-only probes
  for keyed llama.cpp servers reduce wasted work. Cached candidates are not proof
  that a model is loaded; Ollama residents now use `/api/ps` evidence.
* Placement excludes unreservable nodes before ranking. A failed placed node or
  inference liveness check fails closed; execution does not silently switch nodes.
  Existing dispatch-time safety and reservation checks remain authoritative.
* Go source builds use **Go 1.26.10**. The local release validator alone uses
  Go 1.27.0 because pinned GoReleaser v2.18.0 requires it; the validator compiler
  does not change AXIS binaries or the application's vulnerability-scan target.

### Release and upgrade checkpoints

Release preparation updates the version, summary goldens and repository facts.
The release playbook requires a clean release preflight and the nonpublishing
cross-platform workflow on the exact merged SHA before tagging. Native macOS
console checks complement cross-compilation; they are separate evidence.

Use the normal verified installer or updater for v0.20.0. Verify
`axis version --local` and check `axis doctor` for shadowed copies. Upgrading a
running daemon and approving an installation remain separate operational steps;
this guide does not claim either has happened. `axis update` can restart a managed
daemon unless `--no-restart` is selected.

---

## v0.14.14 (2026-08-15)

**Release commit:** `aa49620`  
**GitHub Release:** https://github.com/toasterbook88/axis/releases/tag/v0.14.14

### 🚀 Features

#### CLI Noun Registry (#301)
- Reorganized CLI under noun-first command structure (`axis node`, `axis cluster`, `axis daemon`, `axis task`, `axis reservations`, `axis ai`)
- Added `axis cluster umbrella` and path-first help
- Consistent `/exit` behavior in agent slash help

#### Agent Model Configuration (#300)
- Support startup model selection semantics
- Default model resolution respects local/peer locality
- Truncate `AGENTS.md` instructions on UTF-8 boundary after trim

#### Backend Locality & Advertise URL (#298, #299)
- `axis ai backends` displays locality: `here` (local process), `peer` (other cluster node), `cloud` (external endpoint)
- Probing uses `advertise_url` when configured for reverse-proxied backends
- Fixed API key resolution to be name-authoritative

### 🐛 Bug Fixes

#### Multipath SSH Path Decisions (#297)
- Validate SSH path choices before execution
- Better error handling for unreachable mesh nodes

#### Fact Collection Reliability (#296)
- Fish-safe bash launcher for remote discovery scripts
- Fix awk dependency ordering in NixOS hardware probes

### ⚠️ Upgrade Notes

- Command aliases preserved for backward compatibility
- Recommended: use noun-first command structure (`axis cluster status` instead of legacy forms)

---

## v0.14.13 (2026-08-14)

**Release commit:** `a0114d6`  
**GitHub Release:** https://github.com/toasterbook88/axis/releases/tag/v0.14.13

### 🚀 Features

#### Storage Volume Inventory (#288)
- Facts collector now inventories all mounted volumes (local + network)
- Classifies volume types: root, local, network, temporary
- Zero-cost inspection (does not stat remote NFS/CIFS shares)

#### Network Volume Owner Join (#291)
- Automatically joins network mounts to owning cluster nodes
- Supports CIFS (`//user@host/share`), NFS (`ip:/export`), mDNS
- Sets `owner` + `owner_mount` fields
- Ambiguous hosts remain unresolved

#### Linux Block Bus Observation (#290)
- Observe rotational, removable, USB link speed from sysfs
- Path inference as fallback
- Network volumes not probed

#### Model Lifecycle Commands (#292)
New `axis model start` and `axis model stop` commands:
- `axis model start --node <name> --weights <path> --port <num>`
- `axis model stop --node <name> --port <num>`
- Weights must be on named local volume
- llama-server only, listens on 127.0.0.1
- No Traefik integration, no harness writes

#### Darwin Volume Observation (#293)
- Observe bus/class from `diskutil info`
- Protocol, Solid State, Removable Media, Device/Link Speed
- Network volumes not probed

#### Remote Linux Block Facts (#294)
- Bundle `sysfs_block_b64` for remote nodes
- Multi-probe fallback for non-sysfs platforms
- Applies to named local volumes only

### 🔧 Maintenance

#### Public Boundary Enforcement (#289)
- `hack/verify-public-boundary.sh` rejects non-documentation IPv4 literals
- Wired into `verify-doc-facts.sh`
- Uses RFC 5737 addresses in fixtures and worklog

### ⚠️ Upgrade Notes

- Storage facts now include all volumes, not just root
- Network volume sizes remain 0 (share not stat'd)
- Model lifecycle commands require `--node`, `--weights`, `--port`

---

## v0.14.12 (2026-08-14)

**Release commit:** `98d6b5c`  
**GitHub Release:** https://github.com/toasterbook88/axis/releases/tag/v0.14.12

### 🚀 Features

#### In-Session Commands (#286)
New REPL commands for `axis agent`:
- `/facts` — Print local resident hardware table
- `/cluster` — Print cluster node table from session snapshot
- Session header shows: `[model:] [endpoint:] [status: probed|stale]`

#### Advertise URL for Backends (#285)
- Optional `advertise_url` in `~/.axis/ai.yaml`
- Supports reverse-proxied backend names
- Non-local catalog choices use `advertise_url` for probing
- Same-box callers keep `base_url`

### 🐛 Bug Fixes

#### Hardware Validation Script Order (#284)
- Install awk/rg/jq **before** running tests
- NixOS hardware discovery scripts can now parse ports correctly

### ⚠️ Upgrade Notes

- Use `/facts` and `/cluster` in agent sessions instead of live collects
- Configure `advertise_url` for reverse-proxied backends

---

## v0.14.11 (2026-08-14)

**Release commit:** `f7f1ea3`  
**GitHub Release:** https://github.com/toasterbook88/axis/releases/tag/v0.14.11

### 🚀 Features

#### Native Daemon Service (#281)
Per-user service management:
- `axis daemon service install` — Provisions launchd (macOS) or systemd (Linux)
- `axis daemon service status` — Inspect service state
- `axis daemon service uninstall` — Remove service

**Features:**
- Foreign-file refusal (won't overwrite non-AXIS files)
- Direct argv execution (no shell wrapper)
- Seeds config from observed stable local identity
- Documents privacy, paths, and unmanaged-listener recovery

#### Multipath Telemetry (#278)
- Monotonic process-lifetime route reuse counters
- Exposed via daemon metadata/status JSON
- No persistent latency observations (no durable vantage-point identity)

### 🐛 Bug Fixes

#### Daemon Status JSON Envelope (#280)
- `axis daemon status` emits exactly one `axis.output/v1` JSON envelope
- Classifies outcomes: fresh, stale, degraded, unavailable, incompatible
- Diagnostics kept off stdout

#### Runtime State Permissions (#279)
- All runtime state is owner-only (0700 dirs, 0600 files)
- Centralized across legacy append, lock, config, event, ledger, task-log paths
- Removed readline's duplicate permissive prompt-history files

#### API Request Boundaries (#277)
- 15-second request-body reads
- 60-second idle keep-alives
- 5-second header limit
- Response writes unbounded (for streamed `/run` output)
- Legacy daemon HTTP server removed

### 🔧 Maintenance

#### Claude Workflow Hardening (#276)
- Reject prompts from untrusted author associations
- Remove issue-assignment replay
- Bound concurrency/runtime
- Prevent checkout credential persistence
- Pin checkout and Claude Code Action to immutable commits
- Fail-closed regression coverage in required CI

#### Repository Truth Separation (#275)
- Separate repository and release truth validation
- Publishing a release cannot stale `main`
- Committed current-state facts are repository-derived
- Centralized source/tag equality validation
- Hermetic Go runner shared with NixOS hardware job

#### CI Self-Approval Removal (#282)
- Removed Dependabot self-approve step
- Incompatible with read-only Actions defaults
- Auto-merge via `gh pr merge --auto --squash` preserved

### ⚠️ Upgrade Notes

- Use `axis daemon service install` instead of manual `axis daemon start`
- Daemon status output is now JSON-envelope only
- All runtime files tightened to owner-only permissions

---

## v0.14.10 (2026-08-04)

**Release commit:** `075334d`  
**GitHub Release:** https://github.com/toasterbook88/axis/releases/tag/v0.14.10

### 🚀 Features

#### System-Level Install (#271)
- Default install target: `/usr/local/bin` (was `$HOME/.local/bin`)
- Every node resolves same absolute path
- Downloaded binary staged inside destination, executed, version-checked, then renamed
- Checksum-valid but unusable artifacts can't destroy working installs
- Existing entry classified first: package-manager symlinks, unresolvable symlinks, non-regular entries refused
- Non-AXIS file requires `AXIS_FORCE_REPLACE=1`
- Path resolution fails closed on `..`, inaccessible ancestors, unresolvable symlinks

**New environment variables:**
- `AXIS_REQUIRE_PINNED` — Require specific version
- `AXIS_DRY_RUN` — Download but don't install
- `AXIS_RELEASE_BASE_URL` — Custom release base URL
- `AXIS_FORCE_REPLACE` — Allow replacing non-AXIS files

#### Duplicate Binary Detection (#271)
- `axis doctor` reports duplicate `axis` binaries on host
- Reuses shadow enumeration from `axis update`

### 🐛 Bug Fixes

#### Update Preflight (#271)
- Preflight target writability before downloading
- Privileged destinations fail immediately with elevation command
- No more multi-megabyte downloads that fail at final rename

#### Test Isolation (#270)
- Two `cmd/axis` tests now set `HOME` and `AXIS_HOME` themselves
- Bare `go test ./...` no longer reads operator's store

### 🔧 Maintenance

#### Fleet Test Containment (#272)
- New `internal/fleettest` package
- Write-containment guard for tests executing against real nodes
- Build-tagged two-node facts smoke (`make test-fleet`)
- Not imported by `cmd/`, never enters shipped binary

### ⚠️ Upgrade Notes

**Install path changed:** `/usr/local/bin` by default

```bash
# Old user-local install (still works)
AXIS_INSTALL_DIR=$HOME/.local/bin ./install.sh

# New system install (recommended)
./install.sh

# Set in config.yaml if using custom path
terminal:
  env:
    PATH: /usr/local/bin:$PATH
```

---

## Older Releases

For releases prior to v0.14.10, see:
- **CHANGELOG:** [`CHANGELOG.md`](../CHANGELOG.md) — Complete version history
- **GitHub Releases:** https://github.com/toasterbook88/axis/releases

---

## Release Cadence

AXIS follows a rapid release cadence with:
- **Patch releases** (v0.14.x) — Bug fixes, minor improvements
- **Minor releases** (v0.15.0, v0.16.0) — New features, backward-compatible changes
- **Major releases** (v1.0.0) — Breaking changes (none planned yet)

**Release notes are published** for every successful release. Every pushed tag,
including a failed publication such as v0.14.9, retains a CHANGELOG entry so the
repository's tag history remains explicit.

---

## Reporting Issues

- **GitHub Issues:** https://github.com/toasterbook88/axis/issues
- **Security vulnerabilities:** Use GitHub private vulnerability reporting (do not open public issue)

When reporting:
1. Include `axis version` output
2. Attach `axis doctor` results
3. Provide `axis cluster status --format json`
4. Include relevant logs from `~/.axis/logs/`
