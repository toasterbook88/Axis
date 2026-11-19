# `axis config`: rendered fleet config with drift checks

Date: 2026-11-17; revised: 2026-11-19
Status: draft v7 (addressing sixth §8 spec review findings; awaiting fresh re-review; no code until approved)
Owning area: `cmd/axis`, `internal/config`, `internal/facts`, `internal/daemon`, `internal/api`, `internal/discovery`, `internal/llmrouter`

## Problem

`nodes.yaml` and `ai.yaml` are static per-node files with no derivation or replication path.

- `nodes.yaml` has one writer: the interactive `axis init` wizard (`cmd/axis/init.go:132`).
  `axis init --generate` is report-only (`cmd/axis/init.go:50`).
- `ai.yaml` has no writer at all; `internal/config/ai.go` only loads and validates it.
- Discovery sees beacon peers, but that result is advisory. It projects a peer into a snapshot node
  (`internal/discovery/beacons.go:129`) and never updates the dial/trust list.
- Nothing validates that two nodes share a roster or a role-to-model map.

The private investigation of the operator's fleet found divergent rosters, role gaps and enabled
backends pointing at ports with no listener. The counts and host detail stay in the private findings;
this public repo only needs the shape of the defect.

Editing N files to add one node or model is the bug. A first, narrower design
(`docs/superpowers/specs/2026-10-31-nodes-yaml-generate-design.md`) deliberately stopped at a
read-only report because `config.SaveAtomic` re-marshals the struct and drops comments. This is the
writing spec that one deferred, scoped to generated files rather than hand-edited ones.

## Non-goals

- No automatic trust from Tailscale or a LAN scan. A beacon proves liveness, not membership.
- No runtime resolver in phase 1. Phase 1 renders files the current loader already understands;
  phase 2 lets the daemon resolve membership and models at runtime.
- No config writes from `axis agent`, MCP, HTTP or execution surfaces.
- No change to fact schemas, collector output, snapshot contract, or UDP discovery wire protocol in
  phase 1 (only exporting `facts.RemoteNodeIdentity` in `internal/facts` and adding a read-only
  `GET /v2/beacons` route + `daemon.FetchBeacons` client exposing `(d *Daemon).BeaconsStatus()`
  without exposing `discovery.secret`, so `axis config` does not contend on `:42424` with `SO_REUSEPORT`).
- No new secret indirection in phase 1. The loader does not require `discovery.secret`
  (`internal/config/config.go:581-588` only validates ports/intervals), but beacon authentication
  requires a non-empty value (`internal/discovery/udp.go:56-59,76-82`). Phase 1 therefore requires
  a non-empty secret from `local.yaml`, writes the literal into the generated 0600 file and its 0600
  backups, and refuses migration/render without one. A loader-level secret reference is a separate
  spec.
- No cluster inventory, hostnames, addresses or live model ids in this repository.
- No replacement of the documented role names. The runbook documents `default`, `fast` and `long`
  (`docs/runbooks/ai-config.md:8`); other names are operator-private and stay out of this spec.

## Design

### Intent versus generated artifacts

Add `~/.axis/bootstrap.yaml`, `~/.axis/roster.yaml`, and `~/.axis/local.yaml` for fleet/node
intent, and make the existing config files generated:

```text
~/.axis/bootstrap.yaml      hand-written (0600): hub URL (when AI roles are used) and stable-ID allowlist
~/.axis/roster.yaml         persisted roster (0600): allowlisted nodes (name, stable_id, pinned hostname/endpoints)
~/.axis/local.yaml          hand-written (0600): discovery, local sections, per-node fields, hub auth ref, AI inventory/roles
~/.axis/nodes.yaml          generated (0600): nodes + discovery (+ local sections), with # roster_sha256: <hex>
~/.axis/ai.yaml             generated (0600): hub + healthy local backends + roles, with # model_sha256: <hex>
```

`bootstrap.yaml` is deliberately tiny (`hub_url` is required only when `local.yaml` defines
`ai.roles` referencing `hub`, and optional when operating `nodes.yaml`-only or hubless local-only
AI roles):

```yaml
hub_url: https://models.example.com/v1
allow_stable_ids: [stable-id-a, stable-id-b]
```

`roster.yaml` persists the allowlisted fleet roster and optional address pins (`hostname` and/or
`endpoints`) so offline nodes survive across `render`/`diff`/`apply` without losing required
dial fields:

```yaml
nodes:
  - name: node-a
    stable_id: stable-id-a
    hostname: node-a.example.com   # optional pin for offline nodes or static routing
    endpoints:                     # optional alternate dial targets
      - name: lan
        hostname: 192.0.2.10
```

`local.yaml` owns everything that cannot be discovered (`ai` is optional when operating without
`ai.yaml`):

```yaml
discovery:
  enabled: true
  udp_port: 42424
  secret: <existing non-empty cluster secret>
agent: {default_model: model-id-for-default}
nodes:
  node-a:
    ssh_user: operator
    role: worker
    labels: {site: example}
    system_reserve_mb: 2048
ai:
  hub:
    api_key_env: AXIS_HUB_API_KEY   # optional; or api_key_file; omit both when unauthenticated
  backends:
    local-ollama:
      kind: ollama
      base_url: http://127.0.0.1:11434
      advertise_url: https://ollama-a.example.com   # optional cluster-facing URL
      node: node-a                                  # optional nodes.yaml binding
      enabled: true                                 # optional (default true)
  roles:
    default: {model: model-id-for-default, prefer: [hub, local-ollama]}
    fast:    {model: model-id-for-fast, prefer: [hub]}
    long:    {model: model-id-for-long, prefer: [hub], require_arch: arm64}
```

#### Field ownership

Every `Config` and `AIConfig` field has exactly one owner, and all input YAMLs (`bootstrap.yaml`,
`roster.yaml`, `local.yaml`) decode strictly (`KnownFields(true)`). Validation rejects `name`,
`stable_id`, `hostname`, or `endpoints` inside `local.yaml.nodes.<name>`, and rejects any backend
named `hub` (case-insensitive) inside `local.yaml.ai.backends` even when no role references `hub`.

| Field | Owner |
|---|---|
| `nodes[].name`, `nodes[].stable_id` | `roster.yaml` (`--roster` input; immutable; duplicate names/IDs rejected) |
| `nodes[].hostname`, `nodes[].endpoints` | `roster.yaml` pin (if non-empty) else authenticated live discovery |
| `nodes[].ssh_user`, `nodes[].role` | `local.yaml` |
| `nodes[].ssh_port`, `timeout_sec`, `dial_timeout_sec`, `collect_timeout_sec`, `system_reserve_mb`, `labels` | `local.yaml` |
| `discovery.*` (including `secret`) | `local.yaml` |
| `agent`, `chat`, `ai_providers`, `inference`, `mcp_servers`, `webhooks`, `allowed_internal_hosts` | `local.yaml` |
| `ai.backends` (`{kind, base_url, advertise_url?, node?, enabled?, api_key_env?, api_key_file?}`), `ai.roles` (`{model, prefer, require_arch?}`) | `local.yaml` |
| generated `hub` backend | `bootstrap.hub_url` + `local.yaml` `ai.hub` (`{api_key_env?, api_key_file?}`) |

Each generated file carries its own single-file header so `--scope nodes` and `--scope ai` stay
decoupled:

```yaml
# For ~/.axis/nodes.yaml:
# Code generated by axis config render. DO NOT EDIT.
# roster_sha256: <hex>

# For ~/.axis/ai.yaml:
# Code generated by axis config render. DO NOT EDIT.
# model_sha256: <hex>
```

`discovery.secret` is present because beacon authentication needs it; an absent or empty value is a
named migration and render error. It is the same value the operator already keeps in `nodes.yaml`;
backups are 0600.

### Roster source

`allow_stable_ids` is the membership authority:

- Every rendered node in `nodes.yaml` must be pre-declared in `~/.axis/roster.yaml` (or `--roster PATH`),
  which owns `name` and `stable_id`, plus optional pinned `hostname` / `endpoints`. A roster entry
  is address-pinned whenever `hostname != "" || len(endpoints) > 0` (matching `config.go:404-407`:
  `hostname` is authored-only and is never synthesized from `endpoints[]`, so an endpoints-only pin
  keeps `hostname` empty even when live beacons arrive for its `stable_id`). Only entries with both
  `hostname == ""` and empty `endpoints` accept address refreshes from live discovery; if an unpinned
  entry receives neither a pin nor discovery addresses, `Config.Validate` rejects it
  (`internal/config/config.go:552-554`).
- Exact roster mismatch & validation rules:
  - Reject duplicate node `name` (case-insensitive) or duplicate `stable_id` inside `roster.yaml` /
    `--roster`.
  - Reject any `local.yaml.nodes` key whose node `name` is absent from the active `roster.yaml` /
    `--roster`, so stale or misspelled per-node overrides fail loudly rather than vanishing.
  - Do **not** fail `render` merely because a live UDP beacon's `b.Name` differs from `roster.yaml`'s
    operator-assigned `name` (beacons match strictly by `stable_id`, preserving operator aliases in
    `roster.yaml`).
  - During `axis config discover`, flag a mismatch warning/error if an SSH `facts.RemoteNodeIdentity`
    probe disagrees with `roster.yaml`'s declared `stable_id`.
- Address updates and daemon coexistence (no `SO_REUSEPORT` split):
  1. If `local.Discovery == nil || !local.Discovery.Enabled`, skip **all** beacon lookups (neither
     `daemon.FetchBeacons` nor `discovery.WatchBeaconChanges`, matching `internal/discovery/udp.go:104`).
  2. When `local.Discovery.Enabled` is true and `axis config discover`, `render`, `diff`, or `apply`
     needs live beacon observations, query `daemon.FetchBeacons(ctx, addr)` (`GET /v2/beacons` backed
     by `(d *Daemon).BeaconsStatus()` returning `{listening bool, udp_port int, beacons
     []config.NodeConfig}`, with `udp_port` normalized via the `42424` default at
     `internal/discovery/udp.go:250-253` and never exposing `discovery.secret`) first.
  3. Fall back to a standalone `discovery.WatchBeaconChanges` scan whenever the local daemon is
     unreachable, `!status.Listening`, or `effectiveUDPPort(status.UDPPort) !=
     effectiveUDPPort(local.Discovery.UDPPort)` (since the daemon is not holding
     `local.Discovery.UDPPort`, there is no `SO_REUSEPORT` datagram split).
  4. When `status.Listening && effectiveUDPPort(status.UDPPort) ==
     effectiveUDPPort(local.Discovery.UDPPort)`: compare `local.Discovery.Secret` against on-disk
     `nodes.yaml`'s `discovery.secret` (the config `WatchDiscovery` loads in `daemon.go:475`). Never
     open a competing socket on that same port and never silently trust beacons authenticated with a
     different secret: if the secrets differ, emit an explicit drift warning in `discover`, and fail
     with a named error if any unpinned `roster.yaml` node requires a live address during
     `render`/`diff`/`apply` (`daemon holds discovery on port <p> with a different nodes.yaml secret;
     pin addresses in roster.yaml or apply the secret change first`). Otherwise use `status.Beacons`.
  5. Live beacons match by HMAC-authenticated `StableID` (`internal/discovery/udp.go:56-82`).
     Beacons whose `StableID` is in `allow_stable_ids` and in `roster.yaml` refresh unpinned
     entries; beacons whose `StableID` is in `allow_stable_ids` but **absent** from `roster.yaml`
     are surfaced by `axis config discover` as allowlisted pending peers (showing observed name,
     `stable_id`, and IP so the operator can add them to `roster.yaml` and supply `ssh_user` in
     `local.yaml`); `apply` does not silently invent `roster.yaml` entries or render nodes lacking
     `ssh_user`.
  6. Pinned `roster.yaml` / `--roster` entries (including off-subnet or cross-site peers) can be
     optionally corroborated by `axis config discover` via `axis`'s existing `known_hosts`-verified
     SSH transport (`internal/transport/ssh.go`) and a narrow exported wrapper
     `facts.RemoteNodeIdentity(ctx context.Context, exec transport.Executor) *models.NodeIdentity`
     (which wraps `transport.WithBashForced`, probes `uname -s`, and delegates to existing
     `detectRemoteNodeIdentity` at `internal/facts/identity.go:59-76`). No new SSH signing protocol
     and no change to `internal/discovery` or fact schemas/collector output are added.
- A peer that beacons but is not allowlisted is reported by `discover` and excluded from renders.
- `--roster PATH` is an authoring override input (for `discover`, `render`, `diff`, `apply`), not a
  second membership authority: every `stable_id` in the active roster (`~/.axis/roster.yaml` or
  `--roster`) must also appear in `bootstrap.allow_stable_ids`, and `render`/`apply` refuse any
  unallowlisted entry with a named error.

Migration seeds a complete `~/.axis/roster.yaml` from the current `nodes.yaml`, including offline
nodes and their `hostname`/`endpoints`, before the first render.

### Migration

`axis config apply --migrate` requires default `--scope all` (rejecting explicit `--scope nodes` or
`--scope ai` with `--migrate requires --scope all`) and bridges hand-maintained configs in six steps:

1. **Pre-flight & intent-file safety checks (no backups or writes yet):** Reject explicit partial
   `--scope`, inspect whether `~/.axis/nodes.yaml` (and `~/.axis/ai.yaml` when present on disk)
   begin with `# Code generated by axis config render.`, and check whether any intent file
   (`bootstrap.yaml`, `roster.yaml`, `local.yaml`) already exists on disk:
   - **Branch 1 (already migrated):** If **every** active generated file on disk (`nodes.yaml`, plus
     `ai.yaml` when present on disk) begins with `# Code generated by axis config render.` AND **all
     three** intent files (`bootstrap.yaml`, `roster.yaml`, `local.yaml`) already exist on disk:
     check whether re-rendering from those intent files matches on-disk generated file(s)
     byte-for-byte (`Changed=false`). If byte-identical, return a clean idempotent no-op
     (`Changed=false`, zero new `.bak-*` files); otherwise **refuse `--migrate`** with `"already
     migrated; run axis config apply without --migrate"` so `--migrate` never overwrites edited
     intent files from stale generated configs.
   - **Branch 2 (unconditional `else` pre-existing-intent guard):** Whenever Branch 1 does **not**
     handle the run (including legacy headerless configs, partial 1-of-3 / 2-of-3 intent files, and
     mixed headered `nodes.yaml` + headerless `ai.yaml` states) AND **any** of `bootstrap.yaml`,
     `roster.yaml`, or `local.yaml` already exists on disk: **refuse `--migrate`** before touching
     anything (`intent file <path> already exists; move or remove it before --migrate`).
   - Load legacy `nodes.yaml` via `config.Parse` and optional `ai.yaml` via `LoadAIOrEmpty` (checking
     whether `ai.yaml` actually exists on disk). Reject missing/empty `discovery.secret` and any node
     missing `stable_id` (a `name` is not a stable identity).
2. **Resolve hub and AI migration state (when `ai.yaml` exists on disk):**
   - Explicitly enforce `len(ai.Roles) > 0` before proceeding (an empty `roles: {}` validates under
     `LoadAI`/`ParseAI` and must be rejected explicitly by migration).
   - Identify an optional cluster hub backend in existing `ai.yaml`:
     - (a) an entry literally named `hub` (case-insensitive): require `kind == "openai-compatible"`,
       `b.IsEnabled()`, `node == ""`, and `advertise_url == ""`, failing with a named migration error
       if any precondition is violated (never silently enabling or stripping `node`/`advertise_url`/
       `kind`). If `--hub-url` is also supplied and its normalized URL (`strings.TrimRight(..., "/")`)
       differs from that backend's normalized `base_url`, fail with a named conflict error
       (`--hub-url conflicts with existing hub backend base_url`);
     - (b) otherwise, when `--hub-url != ""`: match an enabled (`b.IsEnabled()`), unbound
       (`node == ""` AND `advertise_url == ""`) `kind: openai-compatible` backend referenced by any
       role whose normalized `base_url` matches normalized `--hub-url`. If none matches AND one or
       more eligible enabled unbound `openai-compatible` role-referenced backends exist in `ai.yaml`,
       fail with a named mismatch error (`--hub-url does not match existing openai-compatible
       backend(s): <names>`) rather than silently dropping `--hub-url` or emitting an unused
       `hub_url`; if zero eligible candidates exist (for example an Ollama-only or node-bound
       `ai.yaml`), seed `bootstrap.hub_url` from `--hub-url` and keep local backends intact;
     - (c) otherwise (`--hub-url == ""` and no literal `hub`): match the sole enabled
       (`b.IsEnabled()`), unbound (`node == ""` AND `advertise_url == ""`) `kind: openai-compatible`
       backend referenced by roles (for example `local-hub` in `ai.example.yaml:19-24`). If two or
       more eligible candidates exist, fail with a named ambiguity error listing their names; if zero
       exist, migrate `ai.yaml` as a hubless local-only AI config (`bootstrap.hub_url` empty).
   - Disabled (`enabled: false`) or node-bound `openai-compatible` engines (where `!b.IsEnabled()` or
     `node != ""` or `advertise_url != ""`, such as a local per-node `llama.cpp`/`mlx`/`vLLM`
     endpoint) are **never** promoted to `hub` by 2(b)/(c) and stay in `local.yaml` `ai.backends`
     with `node`, `advertise_url`, and `enabled` intact.
   - If a hub backend is detected (or `--hub-url` is passed), seed `bootstrap.hub_url` from it; if a
     role literally references `hub` without any resolvable `hub_url`, fail with a named error.
   - When `ai.yaml` does **not** exist on disk (`os.IsNotExist`): preserve `nodes.yaml`-only state!
     Seed `bootstrap.hub_url` from `--hub-url` if provided (else leave empty), omit the `ai:` block
     (or leave `ai.roles` empty) in `local.yaml`, and generate/verify only `nodes.yaml` without
     fabricating or requiring `ai.yaml`.
3. **Construct in-memory `bootstrap.yaml`, `roster.yaml`, and `local.yaml`:**
   - `bootstrap.yaml`: `allow_stable_ids` from every existing node's `stable_id` (plus `hub_url` when
     present).
   - `roster.yaml`: every existing node's `name`, `stable_id`, `hostname`, `endpoints` (preserving
     offline nodes without requiring any live peer).
   - `local.yaml`: `discovery`, top-level local sections (`agent`, `chat`, `ai_providers`, `inference`,
     `mcp_servers`, `webhooks`, `allowed_internal_hosts`), per-node `ssh_user`, `role`, `ssh_port`,
     `timeout_sec`, `dial_timeout_sec`, `collect_timeout_sec`, `system_reserve_mb`, `labels`; and,
     when `ai.yaml` existed, optional `ai.hub` (`api_key_env`, `api_key_file` from the detected hub),
     all remaining non-hub `ai.backends` (preserving `kind`, `base_url`, `advertise_url`, `node`,
     `enabled`, `api_key_env`, `api_key_file`), and all `ai.roles` (preserving `model`, `require_arch`,
     and rewriting any detected hub backend name in `prefer` to reserved `hub`).
4. **Render, probe, and validate in memory BEFORE creating any backups or writing files:**
   - Render `nodes.yaml` bytes and validate via `config.Parse` (`nodes` + `discovery` + optional
     sections); if `ai.yaml` exists (or `local.yaml` defines `ai.roles`), resolve credentials, run
     required AI probes, render `ai.yaml` bytes, validate via `config.ParseAI`, and explicitly prove
     `len(renderedAI.Roles) > 0`. Any validation or probe failure aborts before creating any `.bak-*`
     backups or writing intent/config files.
5. **Confirm, write intent files, then replace generated config(s):**
   - Prompt for confirmation (unless `--yes`; non-TTY without `--yes` fails cleanly before any write).
   - Atomically write new intent files (`bootstrap.yaml`, `roster.yaml`, `local.yaml`, `0600`) first;
     if writing any intent file fails, remove any newly written intent files and abort with `nodes.yaml`
     and `ai.yaml` untouched and zero `.bak-*` files.
   - Replace `nodes.yaml` (and `ai.yaml` when generated) via `WriteGenerated` (creating 0600
     `<path>.bak-<UTC>` backups via `nextBackupPath`). If replacing a generated file fails after
     creating intent files (or swapping the first generated file), restore replaced generated files
     from their `.bak-*` backups (or remove if newly created) and remove newly created migration intent
     files so `~/.axis/` returns cleanly to its pre-migration state.
6. A second `--migrate` run is a clean idempotent no-op (`Changed=false`, zero new backups).

### AI source

When `local.yaml` defines no `ai.roles`: default `--scope all` skips AI rendering and probes
(`nodes.yaml`-only mode; `LoadAIOrEmpty` returns an empty valid AI config), while explicit
`--scope ai` fails with a named error (`no ai.roles configured in local.yaml`). Otherwise (`len(local.AI.Roles) > 0`),
AI render enforces `len(rendered.Roles) > 0` explicitly alongside `config.ParseAI` (since `ParseAI`
keeps `LoadAI`'s allowance for empty `roles: {}`), and renders `ai.yaml` from `local.yaml` backends,
`ai.roles`, and optional `bootstrap.hub_url`:

- Backend name `hub` is reserved. When any role in `local.yaml` references `hub` in `prefer`,
  `bootstrap.hub_url` must be non-empty and the generated `hub` entry is `{name: hub, kind:
  openai-compatible, base_url: bootstrap.hub_url}` with optional `api_key_env`/`api_key_file` copied
  from `local.yaml` `ai.hub` (omitting `hub` cleanly for hubless local-only AI configs even under
  `--scope ai`). Do **not** emit an `auth:` key (unsupported by `AIBackendConfig`,
  `internal/config/ai.go:24-51`, and rejected by `decodeStrictAI` `KnownFields(true)` at
  `ai.go:118-120`); an unauthenticated hub simply omits `api_key_env` and `api_key_file`. Do **not**
  add a `node:` binding to `hub`: a router serving multiple nodes is not local to one worker, and
  `node:` alters `BackendIsLocal` (`internal/llmrouter/probe.go:194-205`) and placement hints.
- If `local.yaml` `ai.hub` (or any enabled local backend) sets `api_key_env` and/or `api_key_file`,
  `cmd/axis/config.go` must resolve it before probing via `secrets.Resolve`
  (`internal/secrets/secrets.go:27`); an `ErrNotFound` or unreadable file error is a named render
  error (never swallowed via `ResolveOrEmpty`).
- Every backend name in `ai.roles[].prefer` must be either `hub` or defined in `local.yaml`
  `ai.backends`; any unknown backend reference is a named render error.
- Probe `hub` (when referenced by any role) and each local backend in `local.yaml` via
  `llmrouter.ProbeBackendForNodes(ctx, b, client, nodeCtx)` (`internal/llmrouter/probe.go:49`),
  which respects `enabled: false` (`OK: false, Message: "disabled"`), selects `AdvertiseURL` for
  non-local peer nodes (`internal/llmrouter/probe.go:178-189`), and normalizes OpenAI-compatible
  URLs via `probeURL` (`internal/llmrouter/probe.go:228-247`: trim trailing `/`; append `/models` if
  `base` ends in `/v1`, else `/v1/models`). Never blindly concatenate `hub_url + "/v1/models"`.
  - **Node-context resolution under decoupled `--scope ai`:** When `renderedNodes` was already
    computed (`--scope all`), pass it as `nodeCtx`. When `--scope ai` runs without rendering
    `nodes.yaml`, resolve `nodeCtx` offline in order: (1) build static `*config.Config` from
    `bootstrap.yaml` + `roster.yaml` (honoring `--roster`) + `local.yaml` without UDP/beacon scans
    if those files exist; (2) else `config.Load(config.DefaultConfigPath())` if `~/.axis/nodes.yaml`
    exists on disk; (3) else `nil` (matching `cmd/axis/ai.go:280`). This preserves `AdvertiseURL`
    locality when `roster.yaml`/`discovery.secret` are absent without coupling `--scope ai` to
    discovery.
- **Guarding 2xx unparseable `/models` responses:** `ProbeBackendForNodes` returns `OK: true,
  Models: nil` (and `"reachable; model list parse incomplete: ..."`) when an HTTP 2xx body fails JSON
  parsing (for example a reverse-proxy HTML error/login page), whereas valid empty JSON `{"data":[]}`
  or `{"models":[]}` yields non-nil `[]string{}` (`internal/llmrouter/probe.go:105-115, 249-284`).
  `cmd/axis/config.go` must treat **`!probe.OK || probe.Models == nil`** as a probe failure:
  - When `hub` is referenced and `!probe.OK || probe.Models == nil`, abort render with a named
    hub-probe/parse error and write nothing (never misreporting a broken `/models` endpoint as
    "model absent").
  - For non-hub local backends, include a backend in generated `ai.yaml` **only** when
    `probe.OK && probe.Models != nil`; treat disabled, unreachable, or unparseable backends as
    unhealthy, omit them from `ai.yaml`, and remove them from every role's `prefer` list. If any
    role's `prefer` becomes empty, fail render with a named error rather than emitting a file
    rejected by `LoadAI`/`ParseAI` (`internal/config/ai.go:201-208`). Preserve `require_arch` on
    roles.
- **Exact model ID validation:** For each role, match `role.Model` using exact string equality against
  reported non-nil `probe.Models` (never `llmrouter.ModelListed`'s tag-tolerant match at
  `internal/llmrouter/probe.go:289-303`). If `hub` remains in `role.prefer`, verify exact `role.Model`
  is in `hub`'s reported `Models`; for local-only roles (for example `prefer: [local-ollama]`), verify
  exact `role.Model` appears in at least one surviving healthy preferred backend's reported `Models`.

### Writer

The prior spec rejected `SaveAtomic` because it strips comments. Generated files do not need
hand-edited comment preservation because their body is wholly rendered. Add two small helpers in
`internal/config`:

```go
// ParseAI reads, normalizes, and strictly validates raw ai.yaml bytes,
// matching LoadAI validation rules identically (including empty roles: {}).
// Migration and AI render enforce len(roles) > 0 explicitly at the config command layer.
func ParseAI(data []byte) (*AIConfig, error)

// WriteGenerated writes header + body to path atomically with mode 0600,
// skipping disk writes and backups only when existing on-disk bytes already
// match the exact rendered bytes (preserving existing backups through nextBackupPath).
func WriteGenerated(path string, header string, body []byte) (SaveResult, error)
```

Unlike `SaveAtomic`'s struct `reflect.DeepEqual` no-op check (`internal/config/save.go:40-45`),
`WriteGenerated` compares exact on-disk file bytes (`bytes.Equal`) against `rendered = header + body`:
if identical, it ensures `0600` mode and returns `SaveResult{Changed: false}` (never creating
duplicate secret-bearing backups); if different — including a legacy headerless `nodes.yaml` whose
decoded struct matches — it writes via `0600` temp file, creates a `0600` backup via
`nextBackupPath`/`copyFileSecure` (`internal/config/save.go:80-146`) when a prior file existed,
and atomically renames into place. `axis init` checks whether an existing target `nodes.yaml` begins
with `# Code generated by axis config render.`; if so, it refuses to overwrite and points the
operator to `axis config apply`, `roster.yaml`, and `local.yaml`.

`apply` renders the scoped file(s) in memory, validates them with `config.Parse` and `config.ParseAI`,
and writes both temp files before renaming when `--scope all` touches both files (skipping `ai.yaml`
when optional/unconfigured). If writing or renaming the second file fails after the first file was
replaced (`firstResult.Changed == true`), `apply` rolls back the first file: restoring from
`firstResult.BackupPath` if non-empty, or removing the newly created first file (`os.Remove`) when
`firstResult.BackupPath == ""`, and names both the rollback outcome and any backup paths in the error.
A hub failure or validation failure writes nothing. If discovery or membership changed in `nodes.yaml`,
`apply` prints the same daemon-restart instruction `axis doctor` emits for mesh settings
(`cmd/axis/doctor.go:229-242`).

### Verification

Two per-file SHA-256 digests, computed over the exact rendered YAML bytes immediately following each
file's generated header:

- `roster_sha256` (in `nodes.yaml`): digest over the contiguous `nodes` + `discovery` YAML prefix,
  ending immediately before the first post-`discovery` key (`agent`, `chat`, `ai_providers`,
  `inference`, `mcp_servers`, `webhooks`, `allowed_internal_hosts`), pinned by deterministic field
  order in `config.Config` (`internal/config/config.go:352-369`) and covered by a boundary test.
- `model_sha256` (in `ai.yaml`): digest over `backends` + `roles` (`ai.yaml`'s entire body).

The name `roster_sha256` is deliberate: `Config.MembershipFingerprint()` already exists
(`internal/config/config.go:211`) and is a different 8-byte `name`/`role`/`ssh_user` hash. The two
must not be confused in `axis doctor` output.

`axis config verify` runs completely offline (zero discovery, SSH, or HTTP probes) and parses each
scoped file's header, schema (`config.Parse` / `config.ParseAI`), and digest (`roster_sha256` /
`model_sha256`):
- A legacy headerless file is a named `verify` error (`not generated; run axis config apply --migrate`),
  and `axis doctor` surfaces it as a warning rather than silently passing.
- Hand edits to `nodes`, `discovery`, `backends`, or `roles` inside the generated files invalidate
  the corresponding digest, so `verify` fails and `doctor` warns. **Only** edits to top-level
  `nodes.yaml` sections serialized after `discovery` (`agent`, `chat`, `ai_providers`, `inference`,
  `mcp_servers`, `webhooks`, `allowed_internal_hosts`) sit outside `roster_sha256` (`diff` reports
  them; `doctor` does not warn).
- **Optional `ai.yaml` rule:** Consistent with `LoadAIOrEmpty` (`internal/config/ai.go:103`), if
  `ai.yaml` is absent on disk AND `local.yaml` defines no `ai.roles` (or does not exist yet on a
  `nodes.yaml`-only host), default `--scope all` `verify` checks only `nodes.yaml` and `axis doctor`
  stays silent on absent `ai.yaml`. Requesting `--scope ai` explicitly when `ai.yaml` is missing fails
  with a named error, and `local.yaml` defining `ai.roles` while generated `ai.yaml` is absent fails
  `verify` and warns in `doctor`.
- `axis config diff` performs live discovery/HTTP rendering and reports per-file drift; `axis doctor`
  warns only on offline `verify` mismatches, not live `diff`, so transient peer outages do not alert.

### Commands

Add one command group, `axis config`, in `cmd/axis/config.go`:

```text
axis config discover [--roster PATH] [--json]                                  read-only: peers, allowlist state, SSH/beacon identity, drift
axis config render [--scope nodes|ai|all] [--roster PATH] [--out PATH]          print rendered config(s); --out DIR required for multi-file --scope all
axis config diff [--scope nodes|ai|all] [--roster PATH] [--json]                live re-render compared with files on disk
axis config verify [--scope nodes|ai|all]                                       offline schema/header/digest check
axis config apply [--scope nodes|ai|all] [--roster PATH] [--migrate] [--hub-url URL] [--yes]  atomic write with backups
```

Rules:

- `--scope` defaults to `all` on `render`, `diff`, `verify`, and `apply`.
- `discover`, `render`, `diff`, and `verify` never modify `~/.axis/nodes.yaml`, `ai.yaml`,
  `bootstrap.yaml`, `roster.yaml`, or `local.yaml`.
- `render` is deterministic: nodes sorted by name, backends and roles sorted by name, no timestamps
  inside rendered bodies or digests.
- When `render --scope all` writes to stdout without `--out`, emit separated YAML documents (`---`
  boundary with `# file: nodes.yaml` and `# file: ai.yaml` when applicable). With `--out PATH`: for
  `--scope nodes` or `--scope ai` (or `--scope all` when only `nodes.yaml` applies and target is a
  non-directory path? No: keep rule simple — for `--scope all`, `--out` MUST be a directory and a
  file path is a named error; for `--scope nodes|ai`, `--out` writes that single file path).
- `apply` is the only command that writes live configs. Without `--yes`, it prints exact target
  paths and prompts cleanly; non-TTY stdin without `--yes` fails with a named refusal, never hangs.
  Without `--migrate`, `apply` refuses to overwrite an existing headerless file (`not generated; run
  axis config apply --migrate`) and leaves it byte-identical. `--migrate` requires `--scope all` and
  refuses explicit `--scope nodes` or `--scope ai` (`--migrate requires --scope all`).
- `diff` returns `0` equal, `1` drift, `2` config/probe error (never placement exit codes).
- `verify` returns non-zero on a bad header, schema error, digest mismatch, or missing scoped file.
- `axis init` refuses generated files as above.
- Adding `axis config` makes 31 top-level commands, and `GET /v2/beacons` adds one read-only HTTP v2
  route. Update `AGENTS.md §4/§5`, the §5 table row, `§6`'s state-file inventory (`bootstrap.yaml`,
  `roster.yaml`, `local.yaml`), and `docs/current-state.md`, then run `./hack/verify-doc-facts.sh` and
  `./hack/verify-repo-truth.sh`.

### Where the code lives

- `internal/config` owns `ParseAI`, `WriteGenerated`, the pure render/digest/migration helpers, and
  strict schema decoders/validators for `bootstrap.yaml`, `roster.yaml`, and `local.yaml` (`config`
  is Support/Stable and must **not** import `internal/secrets`, which is Experimental per
  `hack/lifecycle-check.go:56`).
- `internal/facts` exports only the narrow helper `RemoteNodeIdentity(ctx, exec)` wrapping existing
  `detectRemoteNodeIdentity` (`internal/facts/identity.go:59-76`) with `uname -s` /
  `transport.WithBashForced` (no fact schema or snapshot contract changes).
- `internal/daemon` adds `(d *Daemon) BeaconsStatus() BeaconsStatus` (returning `{listening,
  udp_port, beacons}` from `d.beaconRegistry.Snapshot()` and `WatchDiscovery`'s active listener
  state, safe when `nil`, normalizing `udp_port` via `42424` default) and `daemon.FetchBeacons(ctx,
  addr)` in `internal/daemon/client.go`; `internal/api` registers read-only `GET /v2/beacons` under
  `withAuthSource` via an optional `beaconStatusSource` interface on `snapshotCache`
  (`internal/api/v2.go`) so existing mocks stay unchanged.
- `cmd/axis/config.go` skips all beacon lookups when `local.Discovery == nil ||
  !local.Discovery.Enabled`, otherwise queries `daemon.FetchBeacons` first and falls back to
  `discovery.WatchBeaconChanges` only when the daemon is unreachable, `!status.Listening`, or
  listening on a different UDP port (enforcing the same-port `nodes.yaml` vs `local.yaml`
  `discovery.secret` check without leaking secrets over HTTP), composes optional SSH identity reads
  (`internal/transport` + `facts.RemoteNodeIdentity`), validates secrets (`internal/secrets`
  `secrets.Resolve`), probes AI backends (`internal/llmrouter` `ProbeBackendForNodes`), and calls
  `internal/config` pure helpers.
- `internal/discovery` and `internal/llmrouter` remain unchanged consumers; zero imports from agent,
  MCP, or HTTP layers.
- No new package is needed for phase 1. A new package would need an import-tier justification and
  would duplicate `config` validation.

### Security and trust

- `allow_stable_ids` is required for a node to enter or stay in the roster. Holding the shared
  discovery secret is enough to beacon, so a beacon alone must not grant membership.
- Neither `roster.yaml` nor `--roster` can bypass `bootstrap.allow_stable_ids`.
- A missing or empty `discovery.secret` is a render/migration error; open-mode beacons are not an
  accepted migration outcome.
- `discovery.secret` remains a literal in the generated 0600 file and its 0600 backups. This is the
  current loader contract, not a new secret store.
- `apply` refuses to overwrite a file with an unknown schema or a non-generated headerless file
  unless `--migrate` is used.
- `axis agent` and MCP get no write path; they may read `discover`/`diff` output only.

### Phase 2 boundary

Once phase 1 is deployed, the daemon can replace the generated roster with a live membership view
and replace the generated role table with a catalog view. `nodes.yaml` and `ai.yaml` then become
optional caches for offline boot. That is a separate spec; phase 1 must not pretend it already
resolves at runtime.

## Tests

- `internal/config`: `WriteGenerated` emits single-file header + body, reloads through
  `Load`/`Parse`/`LoadAI`/`ParseAI`, keeps `0600`; a second byte-identical write is `Changed=false`
  with zero backup; a structurally equal legacy headerless `nodes.yaml` still writes to attach its
  generated header (`bytes.Equal` check); one changed byte inside `nodes`, `discovery`, `backends`,
  or `roles` fails `verify`; `roster_sha256` boundary test edits `hostname` and detects it while an
  edit to post-`discovery` `agent` stays outside `roster_sha256`; legacy headerless file yields a
  named `verify` error.
- `internal/config`: render is byte-deterministic (nodes, backends, roles sorted); strict decoders
  reject unknown keys in `bootstrap.yaml`/`roster.yaml`/`local.yaml`, reject `name`/`hostname`/
  `endpoints`/`stable_id` inside `local.yaml.nodes.<name>`, and reject any backend named `hub`
  (case-insensitive) inside `local.yaml.ai.backends` even when no role references `hub`; `local.yaml`
  per-node overrides (`ssh_user`, `role`, timeouts, `labels`) apply cleanly while duplicate
  `roster.yaml` `name` (case-insensitive) or `stable_id` entries and `local.yaml.nodes` keys absent
  from `roster.yaml` fail; an endpoints-only pinned node (`hostname == ""` with non-empty
  `endpoints`) preserves `hostname == ""` across migration and render even when a live beacon
  arrives for its `stable_id`; missing `allow_stable_ids` or missing/empty `discovery.secret` fails;
  unallowlisted `roster.yaml`/`--roster` entry fails; a beacon whose `b.Name` differs from
  `roster.yaml`'s operator alias still matches by `stable_id` without failing `render`; generated `hub`
  omits `auth: none` and preserves `ai.hub` `api_key_*`; `local.yaml` backends round-trip
  `advertise_url`, `enabled`, `require_arch`; `probeURL` semantics handle `hub_url` with and
  without `/v1` (`https://models.example.com/v1` queries `/v1/models`); exact model matching rejects
  `coder` against catalog `coder:latest` while local-only roles (`prefer: [local-ollama]`) validate
  against `local-ollama` models; disabled/dead backends are omitted and pruned from `prefer`, failing
  cleanly if a role becomes empty.
- `internal/facts`: `RemoteNodeIdentity` forces non-login bash (`transport.WithBashForced`), detects
  remote OS via `uname -s`, and returns the Linux/Darwin stable ID via `detectRemoteNodeIdentity`
  without mutating fact schemas or full collector behavior.
- `internal/api` & `internal/daemon`: `GET /v2/beacons` enforces `withAuthSource` and returns
  `d.BeaconsStatus()` (`listening`, normalized `udp_port`, `beacons` without `discovery.secret`), and
  `daemon.FetchBeacons` decodes it cleanly.
- `internal/config` & migration: migrating a `nodes.yaml`-only fixture with absent `ai.yaml` and all
  peers offline succeeds cleanly preserving `labels`, `endpoints`, `webhooks`,
  `allowed_internal_hosts`, `ssh_port`, timeouts, and `system_reserve_mb` in `roster.yaml`/`local.yaml`
  without fabricating `ai.yaml` (and round-tripping endpoints-only nodes with `hostname == ""`); a
  node lacking `stable_id` fails; migrating `ai.yaml` with `local-hub`, `advertise_url`, `enabled:
  false`, and `require_arch` rewrites `prefer` cleanly to `hub`, while multiple enabled unbound
  `openai-compatible` candidates without `--hub-url` fail with a named ambiguity error; invalid
  literal `hub` backends (disabled, non-`openai-compatible`, `node != ""`, or `advertise_url != ""`)
  and `--hub-url` mismatches against eligible `local-hub` fail cleanly (whereas `--hub-url` with a
  purely Ollama/node-bound `ai.yaml` seeds `bootstrap.hub_url`); disabled (`enabled: false`)
  `openai-compatible` backends are excluded from fallback hub auto-detection `2(b)/(c)`, and a
  `--hub-url` differing from an existing literal `hub` backend's `base_url` fails with a conflict
  error; migrating an `ai.yaml` whose sole `openai-compatible` backend is node-bound (`node: node-a`,
  `advertise_url: ...`) preserves it in `local.yaml` `ai.backends` with `node`/`advertise_url` intact
  and leaves `bootstrap.hub_url` empty rather than promoting it to `hub`; migrating an `ai.yaml` with
  `roles: {}` is rejected explicitly; a failing migration validation/probe writes zero `.bak-*` files;
  an intent-file write error removes newly written intent files before touching `nodes.yaml`/`ai.yaml`;
  re-running `--migrate` in drifted already-migrated state, partial-intent state (`nodes.yaml`
  headered + 1–2 intent files), mixed-header state (`nodes.yaml` headered + headerless `ai.yaml`),
  or legacy state with pre-existing intent files refuses cleanly without overwriting intent files,
  whereas a clean second `--migrate` run with unchanged output is an idempotent no-op (`Changed=false`).
- `cmd/axis`: `local.Discovery.Enabled == false` skips both `daemon.FetchBeacons` and
  `WatchBeaconChanges`; when enabled and a stub daemon reports `listening: true` on the matching port
  (with matching secret), `discover`/`render`/`diff`/`apply` read `FetchBeacons` without calling
  `WatchBeaconChanges`; when the daemon is unreachable, reports `listening: false`, or listens on a
  different `udp_port`, CLI falls back to standalone `WatchBeaconChanges`; same-port secret mismatch
  against on-disk `nodes.yaml` warns in `discover` and fails unpinned `render`; `discover --roster`
  honors alternate roster files, reports SSH `RemoteNodeIdentity` vs `roster.yaml.stable_id`
  mismatches, and surfaces allowlisted beacon peers absent from `roster.yaml` as pending without
  `apply` fabricating nodes; `discover` and `diff` leave live files byte-identical; `--scope ai`
  resolves `AdvertiseURL` node locality from static intent files or on-disk `nodes.yaml` even when
  `roster.yaml`/`discovery.secret` are absent; `render --scope ai` with hubless local-only roles
  succeeds while `render --scope ai` with no `ai.roles` fails cleanly; `render --out` with a file path
  for `--scope all` fails cleanly and with a directory writes both files; unresolvable `ai.hub`
  `api_key_env`/`api_key_file` fails cleanly before write; `apply --migrate --scope nodes` is refused;
  `apply` without `--migrate` refuses a headerless existing file; `apply` lists backups, prints the
  restart pointer when discovery/membership changes, and refuses without `--yes` on non-TTY stdin;
  `apply --roster` refuses unallowlisted entries.
- `cmd/axis`: a HTTP 200 non-JSON/HTML `/models` response on `hub` (`OK: true, Models: nil`) aborts
  render with a hub-probe/parse error instead of falling through to "model absent"; an HTTP 200
  non-JSON response on a non-hub local backend omits it and prunes `prefer` (failing cleanly if empty);
  failed second rename restores the first file byte-for-byte when `BackupPath != ""` and deletes the
  newly created first file when `BackupPath == ""`; a hub 503 leaves both live files byte-identical; an
  unallowlisted beacon peer is excluded; `axis init` refuses a generated file and leaves it byte-identical.
- `cmd/axis`: `verify` runs offline with discovery/hub unreachable; `ai.yaml` absent when `local.yaml`
  has no `ai.roles` passes default `--scope all` and keeps `doctor` silent, while `--scope ai` fails
  and missing `ai.yaml` with non-empty `ai.roles` fails `verify` and warns in `doctor`; `doctor`
  warns on digest mismatch or legacy headerless `nodes.yaml`; `diff` returns `0` equal, `1` drift,
  `2` error.
- Golden files for a four-node fixture and a three-role fixture using RFC 5737/RFC 2606 addresses and
  no real hostnames, model IDs, or private roles.
- Failing checks: remove allowlist check → unallowlisted `--roster`/beacon tests fail; remove `/v1`
  normalization → `/v1` `hub_url` test fails; remove exact matching → `coder`/`coder:latest` test
  fails; remove `prefer` pruning → empty-role test fails; remove `WriteGenerated` `bytes.Equal` →
  headerless-struct-equal test fails; remove rollback → second-rename tests fail; remove `--yes` /
  non-generated guards → refusal tests fail.

## Alternatives

1. **Keep the `axis init` wizard as the only writer.** Rejected: it is per-node, interactive and
   has no diff or fleet check. It produced the drift.
2. **Ship an external shell renderer now.** Works as an immediate stopgap and is already the
   operator's fallback, but it cannot enforce schema, allowlist membership, atomic backups or a
   doctor-visible digest. This spec keeps that option as the migration path, not the product.
3. **Runtime-only resolver first.** The right end state, but it changes the loader and daemon
   contract before the operator has a generated artifact to compare. Phase 1 is a safe, testable
   intermediate.
4. **One canonical file copied to every node.** Rejected: per-node addresses/settings, local
   sections, and AI inventory vary across nodes, and a copy still has no drift check.
5. **Add a `discovery.secret` loader indirection now.** Rejected for phase 1: it changes the
   secret contract and mesh startup path, and needs its own spec. Keeping the literal in a 0600
   generated file matches the current loader.

## Research

- Prior writing decision and its comment-preservation research:
  `docs/superpowers/specs/2026-10-31-nodes-yaml-generate-design.md`.
- Ansible dynamic inventory separates inventory sources from host facts:
  https://docs.ansible.com/ansible/latest/inventory_guide/intro_inventory.html
- Consul's catalog treats membership as a registry fact rather than a per-host file:
  https://developer.hashicorp.com/consul/docs/architecture/catalog
- Kubernetes node objects and leases are the runtime membership model this design moves toward:
  https://kubernetes.io/docs/concepts/architecture/nodes/
- Tailscale ACLs and tags establish identity and reachability, not application membership:
  https://tailscale.com/kb/1018/acls

Measurement: no performance claim applies; this is a correctness feature. The private investigation
is the motivation, but its counts and host detail cannot enter this public repo. The measurable
properties are tested locally: a generated file reloads through `Load`/`LoadAIOrEmpty`, two renders
of the same fixture are byte-identical, `verify` detects a one-byte change without network access,
and a failed second rename leaves the first file byte-identical.

## Review

Kind: spec
Reviewer: fresh-context subagent, five inputs only
Model: DeepSeek
Base: 4465fa4a
Head: 8d6e4fc1
Date: 2026-11-17
Verdict: revise

1. The generated nodes file cannot be secret-free: the loader has no secret reference and an absent secret silently enables open-mode beacons. The spec now requires a non-empty secret, writes it into the 0600 generated file and its 0600 backups, and refuses migration without one. fixed in 94878a2d.
2. Private model and role names were in the public spec. They are removed; only the runbook's `default`, `fast` and `long` remain. fixed in 94878a2d.
3. `axis init` could still overwrite a generated file and strip its header. The spec now makes the wizard refuse generated files and point at `axis config apply`. fixed in 94878a2d.
4. The design named `SaveAtomic` but no writer could emit the header. `WriteGenerated`, its header contract, and its no-op/backup rules are now specified. fixed in 94878a2d.
5. Omitting a dead backend could leave a dangling `prefer`. The spec now prunes it from every role and fails if a role becomes empty. fixed in 94878a2d.
6. The field ownership table omitted `labels`, webhooks, `allowed_internal_hosts`, ports and timeouts. The table is now complete and migration round-trips them. fixed in 94878a2d.
7. Migration had no membership source. It now seeds the allowlist and a complete roster from existing stable IDs and preserves offline nodes. fixed in 94878a2d.
8. `verify` had no defined digest input or offline behavior. It now hashes the exact bytes below the header, runs offline, and treats legacy files as named errors. fixed in 94878a2d.
9. Catalog matching was tag-tolerant. The spec now requires exact model IDs and tests `coder` against `coder:latest`. fixed in 94878a2d.
10. Hub outage and cross-file atomicity were undefined. The spec now fails without writing and restores the first file if the second rename fails. fixed in 94878a2d.
11. The new command and state files were missing from the doc-facts bookkeeping. The work list now includes the 30-command count, AGENTS.md sections, current-state and both gates. fixed in 94878a2d.

Questions:
- Does any public measurement apply? No performance measurement applies; the private fleet counts are the motivation, and the local correctness probes are stated in Research. verified: no public inventory is needed.
- Can `--roster` bypass the allowlist? It is an authoring input and cannot bypass `allow_stable_ids`. verified: the spec says `apply` refuses unallowlisted entries.
- Does the loader parse `bootstrap.yaml`? `bootstrap.yaml` is parsed only by `axis config`; the loader never reads it. verified: the file layout says it is hand-written intent.
- Does `cluster:` have a consumer? The unused field was removed from `bootstrap.yaml`. verified: no consumer remains.

Kind: spec
Reviewer: fresh-context subagent, five inputs only
Model: DeepSeek
Base: 4465fa4a
Head: 94878a2d
Date: 2026-11-17
Verdict: revise

1. The review record was missing. This block and the first review block above are now recorded. fixed in 67a3c3e3.
2. `ssh_user` and `role` were owned by both the roster and `local.yaml`. They now belong to `local.yaml`; the roster carries only identity and optional address pins. fixed in 67a3c3e3.
3. Membership was described as an intersection while offline nodes had to survive. The allowlist is now the authority; discovery only refreshes addresses for new or seen nodes. fixed in 67a3c3e3.
4. The local backend inventory had no input schema. `local.yaml` now carries `ai.backends` with name, kind, base URL, optional node and key references; a two-backend test is required. fixed in 67a3c3e3.
5. Migration ignored an existing `ai.yaml` and had no `hub_url` source. It now backs up `ai.yaml`, seeds roles/backends from it, and requires `--hub-url` or a hub already present. fixed in 67a3c3e3.
6. The tests omitted rollback and `diff` exit codes. Both are now explicit tests and failing checks. fixed in 67a3c3e3.
7. The secret rationale was wrong: the loader does not require it, but beacon HMAC does. A missing or empty secret is now a named error. fixed in 67a3c3e3.
8. `apply` now prints the daemon-restart instruction when mesh/discovery settings change. fixed in 67a3c3e3.
9. `WriteGenerated` now reuses the no-op and backup semantics of `SaveAtomic`; repeated applies do not rewrite the secret-bearing file. fixed in 67a3c3e3.
10. `--yes` and `render --out` semantics are defined, including non-TTY refusal and directory-only `--out` for `--scope all`. fixed in 67a3c3e3.

Questions:
- Does the roster digest boundary stay safe if the struct order changes? It is pinned to the `nodes` + `discovery` prefix and tested with a `hostname` edit. verified: the writer fixes the field order.
- What happens on a legacy headerless file before migration? It is a `verify` error and a doctor warning. verified: the spec names both paths.
- Are hand edits to local-owned sections detected? They are outside both digests; `diff` reports them and doctor does not. verified: the verification section says so.
- Does `roster_sha256` collide with `MembershipFingerprint()`? The header digest is renamed `roster_sha256` so it cannot be confused with `Config.MembershipFingerprint()`. verified: the two names differ.

Note: The earlier delegated third-review attempt against 30d596c4 failed on provider billing before returning a verdict; the completed third fresh-context spec review on the rebased main candidate (`85063ae0`) is recorded below.

Kind: spec
Reviewer: fresh-context subagent da2ef279-a243-4c97-8291-6fab8c292e74
Model: gemini
Base: b55931b81192ec3d4d39541dfb498257ca12864a
Head: 85063ae0545ea11cbdb965f8c7320192858b7f95
Date: 2026-11-19
Verdict: revise

1. Major: `hub_url + /v1/models` breaks when `hub_url` already ends in `/v1`. Spec now normalizes OpenAI-compatible URLs through `llmrouter.ProbeBackendForNodes` / `probeURL` (trim `/`, append `/models` if ending in `/v1`, else `/v1/models`). fixed in ee06d641a41365364e3667176e751d18c33e33ff.
2. Major: generated `hub` backend specified `auth: none` and an undefined `local.yaml` auth key. Spec drops `auth: none`, defines `local.yaml` `ai.hub` (`{api_key_env?, api_key_file?}`), and validates hub credentials in `cmd/axis/config.go` via `secrets.Resolve`. fixed in ee06d641a41365364e3667176e751d18c33e33ff.
3. Major: persisted roster file path and `--roster` / `--hub-url` CLI flags were omitted. Spec adds `~/.axis/roster.yaml` (`0600`) with `endpoints`, includes it in `AGENTS.md §6`, and documents `--roster` and `--hub-url` in Commands. fixed in ee06d641a41365364e3667176e751d18c33e33ff.
4. Major: roster onboarding relied on a nonexistent "signed SSH descriptor". Spec replaces it with authenticated UDP discovery beacons plus `roster.yaml`/`--roster` pins optionally corroborated by `known_hosts` SSH + `detectRemoteNodeIdentity`. fixed in ee06d641a41365364e3667176e751d18c33e33ff.
5. Major: `local.yaml` AI inventory omitted `advertise_url` and `enabled`, and `ai.roles` omitted `require_arch`. Spec adds those fields and specifies probing through `ProbeBackendForNodes`. fixed in ee06d641a41365364e3667176e751d18c33e33ff.
6. Major: migration failed without pre-existing `ai.yaml`, and hub-only catalog matching rejected local-only roles. Spec preserves `nodes.yaml`-only migration when `ai.yaml` is absent, specifies `local-hub`-to-`hub` migration rules with ambiguity errors, and validates local-only roles against their healthy preferred backends. fixed in ee06d641a41365364e3667176e751d18c33e33ff.
7. Major: combined dual-digest header conflicted with per-file `--scope` renders and no-churn `discovery.secret` backups. Spec splits headers per file (`nodes.yaml` carries `# roster_sha256`, `ai.yaml` carries `# model_sha256`). fixed in ee06d641a41365364e3667176e751d18c33e33ff.
8. Minor: `WriteGenerated` no-op check, `apply` byte validation/rollback, and `--migrate` backup timing diverged from existing helpers. Spec adds `config.ParseAI`, uses raw `bytes.Equal` in `WriteGenerated`, probes/validates before creating `--migrate` backups, and deletes newly created first files on rollback when `BackupPath == ""`. fixed in ee06d641a41365364e3667176e751d18c33e33ff.
9. Minor: claim that local-owned sections sit outside both digests conflicted with `roster_sha256` and `model_sha256` boundaries. Spec clarifies only post-`discovery` top-level `nodes.yaml` sections sit outside `roster_sha256`. fixed in ee06d641a41365364e3667176e751d18c33e33ff.
10. Minor: Commands table and `## Tests` omitted `--scope` defaults/stdout format and required refusal/idempotency tests. Spec defines `--scope` defaults (`all`), multi-document stdout format, and adds every missing test and failing check. fixed in ee06d641a41365364e3667176e751d18c33e33ff.

Questions:
- May `local.yaml` override `nodes[].hostname` and `nodes[].endpoints`, or are those fields owned exclusively by discovery and roster pins? `roster.yaml` pins and live discovery are the sole owners; strict `local.yaml` schema decoding rejects `hostname` and `endpoints` inside `local.yaml.nodes[]`. verified: field ownership table and strict schema align without contradiction.
- When `ai.yaml` is absent on disk (valid under `LoadAIOrEmpty`), should `axis doctor` stay silent on `ai.yaml` while `axis config verify` checks only `nodes.yaml` unless `--scope ai` or `ai.yaml` exists? Yes, `verify --scope all` checks only `nodes.yaml` and `doctor` stays silent on absent `ai.yaml` unless `local.yaml` defines `ai.roles`, while `--scope ai` fails on missing `ai.yaml`. verified: `### Verification` specifies this contract.

Note: Original revise verdict against `85063ae0` retained honestly; re-review of `ee06d641` + this review record follows before any code starts.

Kind: spec
Reviewer: fresh-context subagent 6d0467c9-4154-4100-909d-56414b60d267
Model: gemini
Base: b55931b81192ec3d4d39541dfb498257ca12864a
Head: c3a188e2e7af941fde27b1ecac4a6dc8b4122cf5
Date: 2026-11-19
Verdict: revise

1. Major: unexported `facts.detectRemoteNodeIdentity` could not be called from `cmd/axis` under the no-fact-plane-change non-goal. Spec now adds `internal/facts` to Owning area, narrows the non-goal to keep fact schemas/collector output unchanged, exports a narrow `facts.RemoteNodeIdentity(ctx, exec)` wrapper over `detectRemoteNodeIdentity` (`transport.WithBashForced` + `uname -s`), and adds a test. fixed in 2b5f03b30d84221343c1105c8ae32f85d972180b.
2. Major: `ProbeBackendForNodes` returns `OK=true, Models=nil` on HTTP 2xx unparseable `/models` responses, defeating `!probe.OK`. Spec now treats `!probe.OK || probe.Models == nil` as failure (aborting render for `hub`, omitting/pruning for local backends, and distinguishing `nil` from empty JSON `[]`), with explicit 200 non-JSON `/models` tests. fixed in 2b5f03b30d84221343c1105c8ae32f85d972180b.
3. Major: `bootstrap.hub_url` was required unconditionally for `--scope ai` despite local-only roles, and `ParseAI` alone allowed `roles: {}`. Spec requires `hub_url` only when `ai.roles` references `hub`, fails `--scope ai` cleanly when no `ai.roles` exist, keeps `ParseAI` identical to `LoadAI`, and enforces `len(roles) > 0` explicitly in migration and AI render checks. fixed in 2b5f03b30d84221343c1105c8ae32f85d972180b.
4. Major: migration fallback rule 2(c) misclassified a sole node-bound `openai-compatible` backend (`node != ""` / `advertise_url != ""`) as cluster `hub` and dropped `node` and `advertise_url`. Spec restricts fallback 2(c) to unbound backends with empty `node` and `advertise_url` (and `node == ""` under 2(b)), preserving hubless node-bound local AI configs and adding a regression fixture. fixed in 2b5f03b30d84221343c1105c8ae32f85d972180b.

Questions:
- What happens when an allowlisted beacon peer (`stable_id` in `bootstrap.allow_stable_ids`) is absent from `roster.yaml`? `roster.yaml` must pre-declare every rendered node (beacons update addresses only for nodes already in `roster.yaml`, while allowlisted beacons absent from `roster.yaml` are surfaced by `discover` as pending peers for manual addition alongside `ssh_user`, never auto-invented on `apply`). verified: `### Roster source` and `## Tests` define this explicitly.
- How does `apply --migrate` handle explicit `--scope nodes`/`--scope ai`, and how are intent-file write failures handled after modifying `nodes.yaml`/`ai.yaml`? `--migrate` rejects partial `--scope` (`--migrate requires --scope all`), writes `bootstrap.yaml`/`roster.yaml`/`local.yaml` atomically first with cleanup on failure before touching `nodes.yaml`/`ai.yaml`, and rolls back both generated and newly created intent files if later replacement fails. verified: `### Migration`, `### Commands`, and `## Tests` define and cover both rules.

Note: Original revise verdict against `c3a188e2` retained honestly; final fresh-context re-review against `2b5f03b3` + this review record follows before any code starts.

Kind: spec
Reviewer: fresh-context subagent b122fe31-8a66-498b-ac7e-aba29d40adb4
Model: gemini
Base: b55931b81192ec3d4d39541dfb498257ca12864a
Head: f0cef23e355ae076309fdc77c9fd27663ffc2017
Date: 2026-11-19
Verdict: revise

1. Major: `--migrate` could overwrite existing or edited intent files (`bootstrap.yaml`, `roster.yaml`, `local.yaml`) without backups when Step 1's no-op check returned false. Migration Step 1 now checks intent-file existence explicitly and refuses `--migrate` whenever generated headers + intent files drifted or any intent file already exists beside a legacy config, with regression tests added. fixed in 660413e98ec2094eba1da78dc6f61b463065d6e8.
2. Major: unpinned roster address resolution via `WatchBeaconChanges` hits `SO_REUSEPORT` UDP datagram splitting when `axis serve` holds `:42424` (`s1=10 s2=0` verified). Spec now adds `(d *Daemon).Beacons()`, read-only `GET /v2/beacons` under `withAuthSource`, and `daemon.FetchBeacons`, reading the daemon's `BeaconRegistry` when reachable and falling back to standalone `WatchBeaconChanges` only when unreachable, with tests added. fixed in 660413e98ec2094eba1da78dc6f61b463065d6e8.
3. Minor: `"roster name/stable_id mismatches fail"` had no defined comparison in Design. Roster source and Tests now explicitly reject duplicate `roster.yaml` names (case-insensitive) or `stable_id`s and unknown `local.yaml.nodes` keys, flag SSH `RemoteNodeIdentity` mismatches in `discover`, and preserve operator aliases when `beacon.Name != roster.Name`. fixed in 660413e98ec2094eba1da78dc6f61b463065d6e8.
4. Minor: source of `renderedNodes` under decoupled `--scope ai` was unspecified. AI source and Tests now define offline resolution order (static intent files without UDP scans -> on-disk `nodes.yaml` via `config.Load` -> `nil`), preserving `AdvertiseURL` locality. fixed in 660413e98ec2094eba1da78dc6f61b463065d6e8.

Questions:
- Should `local.yaml` reject any backend named `hub` (case-insensitive) even without role references, exclude disabled (`enabled: false`) `openai-compatible` backends from fallback 2(b)/(c), and reject `--hub-url` conflicts against a literal `hub` backend in 2(a)? Yes, strict validation rejects `hub` in `local.yaml.ai.backends` unconditionally, disabled backends are excluded from fallback 2(b)/(c), and normalized URL mismatches between `--hub-url` and literal `hub` fail with a conflict error. verified: Field ownership, Migration Step 2, and Tests define and cover all three rules.

Note: Original revise verdict against `f0cef23e` retained honestly; fresh-context re-review against `660413e9` + this review record follows before any code starts.
