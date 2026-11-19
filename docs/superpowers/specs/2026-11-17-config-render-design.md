# `axis config`: rendered fleet config with drift checks

Date: 2026-11-17; revised: 2026-11-19
Status: draft v4 (addressing third §8 spec review findings; awaiting fresh re-review; no code until approved)
Owning area: `cmd/axis`, `internal/config`, `internal/discovery`, `internal/llmrouter`

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
- No change to the fact plane, snapshot contract or daemon wire protocol in phase 1, and no
  change to `internal/discovery` beyond read-only inspection.
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

`bootstrap.yaml` is deliberately tiny (`hub_url` is required when `local.yaml` defines `ai.roles`
referencing `hub` or when `--scope ai` is used, and optional when operating `nodes.yaml`-only):

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
`roster.yaml`, `local.yaml`) decode strictly (`KnownFields(true)`). Putting `name`, `stable_id`,
`hostname`, or `endpoints` inside `local.yaml.nodes.<name>` is rejected by strict decoding.

| Field | Owner |
|---|---|
| `nodes[].name`, `nodes[].stable_id` | `roster.yaml` (`--roster` input; immutable; mismatch fails render) |
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

- An allowlisted node in `~/.axis/roster.yaml` (or `--roster PATH`) stays in the roster even when
  it is offline. The roster supplies its `name`, `stable_id`, and optional pinned `hostname` /
  `endpoints`; for unpinned entries, live discovery supplies addresses and observed facts. Precedence
  for `hostname`/`endpoints` is therefore: non-empty roster pin, then authenticated live discovery;
  if neither provides `hostname` or `endpoints[].hostname`, `Config.Validate` rejects the entry
  (`internal/config/config.go:552-554`).
- An allowlisted peer joins/updates addresses from either:
  1. An HMAC-authenticated UDP discovery beacon (`internal/discovery/udp.go:56-82`) whose `StableID`
     is in `allow_stable_ids`, or
  2. A pinned `roster.yaml` / `--roster` entry (including off-subnet or cross-site peers), which
     `axis config discover` can optionally corroborate via `axis`'s existing `known_hosts`-verified
     SSH transport (`internal/transport/ssh.go`) and remote stable-ID probe (`detectRemoteNodeIdentity`,
     `internal/facts/identity.go:59-76`). No new SSH signing protocol or change to `internal/discovery`
     is added.
- A peer that beacons but is not allowlisted is reported by `discover` and excluded from renders.
- `--roster PATH` is an authoring override input (for `render`, `diff`, `apply`), not a second
  membership authority: every `stable_id` in the active roster (`~/.axis/roster.yaml` or `--roster`)
  must also appear in `bootstrap.allow_stable_ids`, and `render`/`apply` refuse any unallowlisted
  entry with a named error.

Migration seeds a complete `~/.axis/roster.yaml` from the current `nodes.yaml`, including offline
nodes and their `hostname`/`endpoints`, before the first render.

### Migration

`axis config apply --migrate` is the one-time bridge from hand-maintained configs:

1. **Pre-flight checks (no backups or writes yet):** Load `nodes.yaml` via `config.Parse` and
   optional `ai.yaml` via `LoadAIOrEmpty` (checking whether `ai.yaml` actually exists on disk).
   - If the active generated file(s) already begin with `# Code generated by axis config render.` AND
     `bootstrap.yaml`/`roster.yaml`/`local.yaml` already exist matching disk (or would produce
     byte-identical generated output with valid headers), `--migrate` is an idempotent no-op
     (`Changed=false`, no new `.bak-*` files).
   - Reject missing/empty `discovery.secret` and any node missing `stable_id` (a `name` is not a
     stable identity).
2. **Resolve hub and AI migration state (when `ai.yaml` exists on disk):**
   - Require `ai.yaml`'s migrated `roles` map to be non-empty.
   - Identify the hub backend in existing `ai.yaml`: (a) a backend literally named `hub`, else (b) the
     unique `kind: openai-compatible` backend referenced by any role whose `base_url` matches
     `--hub-url` (if supplied), else (c) the sole `kind: openai-compatible` backend referenced by
     roles (for example `local-hub` in `ai.example.yaml:19-24`) when unambiguous. If multiple
     `openai-compatible` candidates exist without `--hub-url` disambiguation, fail with a named
     ambiguity error listing candidate backend names.
   - If `ai.yaml` has non-empty roles referencing a hub (or `--hub-url` is passed), seed
     `bootstrap.hub_url` from the detected hub's `base_url` or `--hub-url` (named error if neither is
     available when roles reference `hub`).
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
     when `ai.yaml` existed, `ai.hub` (`api_key_env`, `api_key_file` from the detected hub), all
     remaining non-hub `ai.backends` (preserving `kind`, `base_url`, `advertise_url`, `node`,
     `enabled`, `api_key_env`, `api_key_file`), and all `ai.roles` (preserving `model`, `require_arch`,
     and rewriting the detected hub backend name in `prefer` to reserved `hub`).
4. **Render, probe, and validate in memory BEFORE creating any `.bak-*` backup or writing files:**
   - Render `nodes.yaml` bytes and validate via `config.Parse` (`nodes` + `discovery` + optional
     sections); if `ai.yaml` exists (or `local.yaml` defines `ai.roles`), resolve credentials, run
     required AI probes, render `ai.yaml` bytes, and validate via `config.ParseAI` (proving `roles`
     is non-empty). Any validation or probe failure aborts before writing any backup or config file.
5. **Back up and atomically write:**
   - Prompt for confirmation (unless `--yes`; non-TTY without `--yes` fails cleanly before any write).
   - Write `nodes.yaml` (and `ai.yaml` when generated) via `WriteGenerated` (which creates 0600
     `<path>.bak-<UTC>` backups using `nextBackupPath` only when changing disk bytes), and atomically
     write `bootstrap.yaml`, `roster.yaml`, and `local.yaml` (`0600`).
6. A second `--migrate` run is a clean idempotent no-op (`Changed=false`, zero new backups).

### AI source

When `local.yaml` defines no `ai.roles` and `--scope ai` is not requested, AI rendering and probes
are skipped (`nodes.yaml`-only mode; `LoadAIOrEmpty` returns an empty valid AI config). Otherwise,
`ai.yaml` is rendered from `bootstrap.hub_url` (when `hub` is used), the `local.yaml` backend
inventory, and `ai.roles`:

- Backend name `hub` is reserved. When any role references `hub` in `prefer` (or `--scope ai` renders
  with `bootstrap.hub_url` set), the generated `hub` entry is `{name: hub, kind: openai-compatible,
  base_url: bootstrap.hub_url}` with optional `api_key_env`/`api_key_file` copied from `local.yaml`
  `ai.hub`. Do **not** emit an `auth:` key (unsupported by `AIBackendConfig`, `internal/config/ai.go:24-51`,
  and rejected by `decodeStrictAI` `KnownFields(true)` at `ai.go:118-120`); an unauthenticated hub
  simply omits `api_key_env` and `api_key_file`. Do **not** add a `node:` binding to `hub`: a router
  serving multiple nodes is not local to one worker, and `node:` alters `BackendIsLocal`
  (`internal/llmrouter/probe.go:194-205`) and placement hints.
- If `local.yaml` `ai.hub` (or any enabled local backend) sets `api_key_env` and/or `api_key_file`,
  `cmd/axis/config.go` must resolve it before probing via `secrets.Resolve`
  (`internal/secrets/secrets.go:27`); an `ErrNotFound` or unreadable file error is a named render
  error (never swallowed via `ResolveOrEmpty`).
- Every backend name in `ai.roles[].prefer` must be either `hub` or defined in `local.yaml`
  `ai.backends`; any unknown backend reference is a named render error.
- Probe `hub` (when referenced or configured) and each local backend in `local.yaml` via
  `llmrouter.ProbeBackendForNodes(ctx, b, client, renderedNodes)` (`internal/llmrouter/probe.go:49`),
  which respects `enabled: false` (`OK: false, Message: "disabled"`), selects `AdvertiseURL` for
  non-local peer nodes (`internal/llmrouter/probe.go:178-189`), and normalizes OpenAI-compatible
  URLs via `probeURL` (`internal/llmrouter/probe.go:228-247`: trim trailing `/`; append `/models` if
  `base` ends in `/v1`, else `/v1/models`). Never blindly concatenate `hub_url + "/v1/models"`.
- If `hub` is required by any role and its probe fails (`!probe.OK`, including connection error, 5xx,
  timeout, or `/models` list parse failure), abort render with a named error and write nothing (do not
  confuse hub unreachability with "model absent").
- Include each non-hub local backend in generated `ai.yaml` **only** when `probe.OK` is true (so
  disabled or unreachable local backends are omitted). Remove any omitted backend from every role's
  `prefer` list; if a role's `prefer` becomes empty, fail render with a named error rather than
  emitting a file rejected by `LoadAI`/`ParseAI` (`internal/config/ai.go:201-208`). Preserve
  `require_arch` on roles.
- **Exact model ID validation:** For each role, match `role.Model` using exact string equality against
  reported `probe.Models` (never `llmrouter.ModelListed`'s tag-tolerant match at
  `internal/llmrouter/probe.go:289-303`). If `hub` remains in `role.prefer`, verify exact `role.Model`
  is in `hub`'s reported `Models`; for local-only roles (for example `prefer: [local-ollama]`), verify
  exact `role.Model` appears in at least one surviving healthy preferred backend's reported `Models`
  (when that backend reports a parsed model list).

### Writer

The prior spec rejected `SaveAtomic` because it strips comments. Generated files do not need
hand-edited comment preservation because their body is wholly rendered. Add two small helpers in
`internal/config`:

```go
// ParseAI reads, normalizes, and strictly validates raw ai.yaml bytes.
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
axis config discover [--json]                                                   read-only: peers, allowlist state, SSH/beacon identity, drift
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
  axis config apply --migrate`) and leaves it byte-identical.
- `diff` returns `0` equal, `1` drift, `2` config/probe error (never placement exit codes).
- `verify` returns non-zero on a bad header, schema error, digest mismatch, or missing scoped file.
- `axis init` refuses generated files as above.
- Adding `axis config` makes 31 top-level commands. Update `AGENTS.md §4/§5`, the §5 table row, `§6`'s
  state-file inventory (`bootstrap.yaml`, `roster.yaml`, `local.yaml`), and `docs/current-state.md`,
  then run `./hack/verify-doc-facts.sh` and `./hack/verify-repo-truth.sh`.

### Where the code lives

- `internal/config` owns `ParseAI`, `WriteGenerated`, the pure render/digest/migration helpers, and
  strict schema decoders/validators for `bootstrap.yaml`, `roster.yaml`, and `local.yaml` (`config`
  is Support/Stable and must **not** import `internal/secrets`, which is Experimental per
  `hack/lifecycle-check.go:56`).
- `cmd/axis/config.go` composes `internal/discovery` beacons, optional SSH identity reads
  (`internal/transport` + `internal/facts`), secret validation (`internal/secrets` `secrets.Resolve`),
  AI probing (`internal/llmrouter` `ProbeBackendForNodes`), and calls `internal/config` pure helpers.
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
  reject unknown keys in `bootstrap.yaml`/`roster.yaml`/`local.yaml` and reject `hostname`/
  `endpoints`/`stable_id` inside `local.yaml.nodes[]`; `local.yaml` per-node overrides (`ssh_user`,
  `role`, timeouts, `labels`) apply cleanly while roster `name`/`stable_id` mismatches fail; missing
  `allow_stable_ids` or missing/empty `discovery.secret` fails; unallowlisted `roster.yaml`/`--roster`
  entry fails; generated `hub` omits `auth: none` and preserves `ai.hub` `api_key_*`; `local.yaml`
  backends round-trip `advertise_url`, `enabled`, `require_arch`; `probeURL` semantics handle
  `hub_url` with and without `/v1` (`https://models.example.com/v1` queries `/v1/models`); exact model
  matching rejects `coder` against catalog `coder:latest` while local-only roles (`prefer:
  [local-ollama]`) validate against `local-ollama` models; disabled/dead backends are omitted and
  pruned from `prefer`, failing cleanly if a role becomes empty.
- `internal/config` & migration: migrating a `nodes.yaml`-only fixture with absent `ai.yaml` and all
  peers offline succeeds cleanly preserving `labels`, `endpoints`, `webhooks`,
  `allowed_internal_hosts`, `ssh_port`, timeouts, and `system_reserve_mb` in `roster.yaml`/`local.yaml`
  without fabricating `ai.yaml`; a node lacking `stable_id` fails; migrating `ai.yaml` with `local-hub`,
  `advertise_url`, `enabled: false`, and `require_arch` rewrites `prefer` cleanly to `hub`, while
  multiple `openai-compatible` candidates without `--hub-url` fail with a named ambiguity error;
  a failing migration validation/probe writes zero `.bak-*` files; a second `--migrate` run is an
  idempotent no-op (`Changed=false`).
- `cmd/axis`: `discover` and `diff` leave live files byte-identical; `render --out` with a file path
  for `--scope all` fails cleanly and with a directory writes both files; unresolvable `ai.hub`
  `api_key_env`/`api_key_file` fails cleanly before write; `apply` without `--migrate` refuses a
  headerless existing file; `apply` lists backups, prints the restart pointer when
  discovery/membership changes, and refuses without `--yes` on non-TTY stdin; `apply --roster`
  refuses unallowlisted entries.
- `cmd/axis`: failed second rename restores the first file byte-for-byte when `BackupPath != ""` and
  deletes the newly created first file when `BackupPath == ""`; a hub 503 leaves both live files
  byte-identical; an unallowlisted beacon peer is excluded; `axis init` refuses a generated file and
  leaves it byte-identical.
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

Note: Third review is pending against commit 30d596c4. The delegated review was blocked by provider billing (Ollama 429; alternate Claude/Codex providers returned 402), so no verdict is recorded yet.
