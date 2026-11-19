# `axis config`: rendered fleet config with drift checks

Date: 2026-11-17
Status: draft v3 (rebased to b55931b8; awaiting third §8 spec review; no code until approved)
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
- No change to the fact plane, snapshot contract or daemon wire protocol in phase 1.
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

Add `~/.axis/local.yaml` for node-local intent and overrides, and make the existing files generated:

```text
~/.axis/bootstrap.yaml      hand-written: hub URL and membership allowlist
~/.axis/local.yaml          hand-written: discovery, local sections, node overrides, AI inventory
~/.axis/nodes.yaml          generated: nodes + discovery, with roster hash
~/.axis/ai.yaml             generated: hub/local backends + roles, with model hash
```

`bootstrap.yaml` is deliberately tiny:

```yaml
hub_url: https://models.example.com/v1
allow_stable_ids: [stable-id-a, stable-id-b]
```

`local.yaml` owns everything that cannot be discovered:

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
  backends:
    local-ollama: {kind: ollama, base_url: http://127.0.0.1:11434}
  roles:
    default: {model: model-id-for-default, prefer: [hub, local-ollama]}
    fast:    {model: model-id-for-fast, prefer: [hub]}
    long:    {model: model-id-for-long, prefer: [hub]}
```

#### Field ownership

Every `Config` and `NodeConfig` field has exactly one owner. Unknown keys fail validation, as they
already do (`internal/config/config.go:419-422`, `internal/config/ai.go:118-121`).

| Field | Owner |
|---|---|
| `nodes[].name`, `nodes[].stable_id` | roster (immutable) |
| `nodes[].hostname`, `nodes[].endpoints` | discovery; roster may pin an address |
| `nodes[].ssh_user`, `nodes[].role` | `local.yaml` |
| `nodes[].ssh_port`, `timeout_sec`, `dial_timeout_sec`, `collect_timeout_sec`, `system_reserve_mb`, `labels` | `local.yaml` |
| `discovery.*` (including `secret`) | `local.yaml` |
| `agent`, `chat`, `ai_providers`, `inference`, `mcp_servers`, `webhooks`, `allowed_internal_hosts` | `local.yaml` |
| `ai.backends` (local inventory), `ai.roles` | `local.yaml` |
| the generated `hub` backend | `bootstrap.hub_url` + `local.yaml` auth reference |

Precedence for a per-node field is: explicit `local.yaml` value, then roster value for
`hostname`/`endpoints`, then discovery. `name` and `stable_id` cannot be overridden; a mismatch is a
render error.

The generated files carry:

```yaml
# Code generated by axis config render. DO NOT EDIT.
# roster_sha256: <hex>
# model_sha256: <hex>
```

`discovery.secret` is present because beacon authentication needs it; an absent or empty value is a
named migration and render error. It is the same value the operator already keeps in `nodes.yaml`;
backups are 0600.

### Roster source

`allow_stable_ids` is the membership authority:

- A node in the allowlist stays in the roster even when it is offline. The roster supplies its
  `name`, `stable_id` and, when needed, a pinned `hostname`/`endpoints`; discovery only refreshes
  addresses and observed facts.
- A new node enters only when it is allowlisted and an authenticated discovery record or a signed
  SSH descriptor matches its stable ID.
- A peer that beacons but is not allowlisted is reported by `discover` and excluded.
- A `--roster` file is an authoring input, not a second authority: every stable ID in it must also
  be in `allow_stable_ids`, and `apply` refuses an unallowlisted entry.

`--roster` accepts a small private file:

```yaml
nodes:
  - name: node-a
    stable_id: stable-id-a
    hostname: node-a.example.com   # optional pin for an offline node
```

`discover` may fill `hostname`/`endpoints` from a live peer. If the peer is offline, the roster must
provide a `hostname` (or `endpoints`); otherwise `Config.Validate` rejects the entry for a missing
hostname (`internal/config/config.go:552-570`). Migration seeds a complete roster from the current
`nodes.yaml`, including offline nodes, before the first render.

### Migration

`axis config apply --migrate` is the one-time bridge:

1. Read `nodes.yaml` and `ai.yaml`; back each up as `<path>.bak-<UTC>` (0600), using the same
   `nextBackupPath` naming `SaveAtomic` already uses.
2. Seed `bootstrap.yaml`: `hub_url` comes from an existing `ai.yaml` hub backend when one exists,
   otherwise from a required `--hub-url` flag (named error if neither). `allow_stable_ids` comes from
   every existing node's `stable_id`. A node without a stable ID is a named error; a name is not a
   stable identity.
3. Write a roster containing every existing node: `name`, `stable_id`, `hostname`/`endpoints`.
   Offline nodes are preserved; no live peer is required.
4. Write `local.yaml`: `discovery`, the local top-level sections, per-node `ssh_user`/`role`/ports/
   timeouts/`system_reserve_mb`/`labels`, and the existing `ai.yaml` backends/roles as the starting
   AI inventory/policy. A missing or empty `discovery.secret` is a named error.
5. Render both files, validate them through `Load`/`LoadAIOrEmpty` **and** prove the role table is
   non-empty (an empty roles map validates, so the migration must not accept it as success), then
   write.
6. A second run is idempotent; `--migrate` is a no-op once the generated header is present.

A migration with every peer offline must still keep all existing nodes. A migration with an existing
hand-written `ai.yaml` must preserve its backend names and roles, not render an empty role table.

### AI source

`ai.yaml` is rendered from the hub, the local backend inventory and the role policy:

- The generated `hub` backend is `{name: hub, kind: openai-compatible, base_url: bootstrap.hub_url}`.
  Its auth is `auth: none` or a key reference from `local.yaml`; an unresolvable reference is a
  render error. The name `hub` is reserved for the generated backend.
- The local inventory maps backend name to `{kind, base_url, node?, api_key_*?}`. Every backend in
  `ai.roles[].prefer` must exist in the inventory or be `hub`; a missing name is a render error.
- Query `hub_url + /v1/models`; that list is the catalog. Match model IDs exactly; do not use
  `llmrouter.ModelListed`'s tag-tolerant matching (`internal/llmrouter/probe.go:289-303`).
- Include a local backend in the output only when its probe returns a 2xx. When a dead backend is
  omitted, remove it from every role's `prefer` list. If a role's `prefer` becomes empty, the render
  fails with a named error rather than emitting a file the loader rejects
  (`internal/config/ai.go:201-208`).
- Do not add a `node:` binding to the hub backend. A router that serves several nodes is not local
  to one worker, and the binding changes `BackendIsLocal` and placement hints.
- Hub unreachable (connection error, 5xx, timeout) is a named error and writes nothing. It is not
  the same as "model absent".

### Writer

The prior spec rejected `SaveAtomic` because it drops comments. Generated files do not need comment
preservation: they are not hand-edited. Add a small writer in `internal/config`:

```go
// WriteGenerated writes a generated preamble followed by yaml.Marshal(cfg),
// skips a semantically identical write, backs up an existing file through
// nextBackupPath, and renames a 0600 temp file into place.
func WriteGenerated(path string, header string, cfg any) (SaveResult, error)
```

It reuses `nextBackupPath` and the `Changed=false` no-op rule from `SaveAtomic`
(`internal/config/save.go:22,40-47,131-132`), so repeated applies do not rewrite the file or create
another copy of the secret-bearing backup. It is used for both `nodes.yaml` and `ai.yaml`; no yaml.v3
node-tree round trip is needed because the body is fully generated. `axis init` must refuse to write
a file whose first line is the generated header and point at `axis config apply`/`local.yaml`.

`apply` renders both files in memory, runs `Config.Validate` and `LoadAI` on the rendered bytes,
writes both temp files, then renames. If the second rename fails, the first file is restored from its
backup; both backup paths are named in the output. A hub failure or a validation failure writes
nothing. If discovery or membership changed, `apply` prints the same daemon-restart instruction
`axis doctor` already emits for mesh settings (`cmd/axis/doctor.go:229-242`).

### Verification

Two digests, computed over the exact bytes below the header:

- `roster_sha256`: the prefix containing `nodes` then `discovery`, ending immediately before the
  first non-roster key. The generated writer fixes that field order; a test pins the boundary.
- `model_sha256`: the `backends` + `roles` prefix of `ai.yaml`.

The name `roster_sha256` is deliberate: `Config.MembershipFingerprint()` already exists
(`internal/config/config.go:211`) and is a different, 8-byte name/role/ssh_user hash. The two must
not be confused in doctor output.

`axis config verify` recomputes both digests from the file and compares the header. It performs no
discovery, SSH or hub probe, so it works offline and in `axis doctor`. A legacy headerless file is a
named `verify` error ("not generated; run axis config apply --migrate"), and `doctor` reports it as
a warning rather than silently passing. `axis config diff` does the live re-render and reports what
would change; `doctor` warns on `verify` mismatch, not on `diff`, so a transient peer outage does not
page the operator. Hand edits to the local-owned sections inside the generated files are outside both
digests; `diff` reports them and `doctor` does not.

### Commands

Add one command group, `axis config`, in `cmd/axis/config.go`:

```text
axis config discover [--json]              read-only: peers, allowlist state, facts, drift summary
axis config render --scope nodes|ai|all    print desired files; --out PATH writes only there
axis config diff                           live re-render compared with the files on disk
axis config verify                         offline schema/header/digest check
axis config apply [--migrate] [--yes]      atomic write with backups; refuses unallowlisted peers
```

Rules:

- `discover`, `render` and `diff` never write a live config.
- `render` is deterministic: nodes sorted by name, roles sorted by name, no timestamp in a digest.
- `apply` is the only command that writes a live config. It prints the exact files and backup paths
  and asks for confirmation; without `--yes`, a non-TTY stdin is a named refusal, not a hang.
- `render --out` writes only the requested path. For `--scope all`, `--out` must be a directory and
  the two files are written as `nodes.yaml` and `ai.yaml`; a file path is a named error.
- `diff` returns 0 equal, 1 drift, 2 config error; it does not reuse placement exit codes.
- `verify` returns non-zero on a bad header, schema or digest.
- `axis init` refuses generated files as above.
- Adding `axis config` makes 31 top-level commands. Update AGENTS.md §4/§5, the §5 table row, §6's
  state-file inventory, and `docs/current-state.md`, then run `./hack/verify-doc-facts.sh` and
  `./hack/verify-repo-truth.sh`.

### Where the code lives

- `internal/config` owns `WriteGenerated`, the pure render functions and schema validation; it
  already owns `Config`, `AIConfig`, `Validate`, `SaveAtomic` and the strict loader.
- `cmd/axis/config.go` composes discovery, facts and model probes and calls those pure functions.
- `internal/discovery` and `internal/llmrouter` are unchanged consumers; no imports from agent, MCP
  or HTTP layers.
- No new package is needed for phase 1. A new package would need an import-tier justification and
  would duplicate `config` validation.

### Security and trust

- `allow_stable_ids` is required for a node to enter or stay in the roster. Holding the shared
  discovery secret is enough to beacon, so a beacon alone must not grant membership.
- `--roster` cannot bypass the allowlist.
- A missing or empty `discovery.secret` is a render/migration error; open-mode beacons are not an
  accepted migration outcome.
- `discovery.secret` remains a literal in the generated 0600 file and its 0600 backups. This is the
  current loader contract, not a new secret store.
- `apply` refuses to overwrite a file with an unknown schema or a non-generated file unless
  `--migrate` is used.
- `axis agent` and MCP get no write path; they may read `discover`/`diff` output only.

### Phase 2 boundary

Once phase 1 is deployed, the daemon can replace the generated roster with a live membership view
and replace the generated role table with a catalog view. `nodes.yaml` and `ai.yaml` then become
optional caches for offline boot. That is a separate spec; phase 1 must not pretend it already
resolves at runtime.

## Tests

- `internal/config`: `WriteGenerated` emits the header and body, re-loads through `Load`, and keeps
  0600; a second write of the same bytes is `Changed=false` with no new backup; one changed byte
  fails `verify`; the `roster_sha256` boundary test changes `hostname` and detects it; the legacy
  headerless file is a named error.
- `internal/config`: render is byte-deterministic; nodes and roles sorted; unknown override key
  rejected; missing `allow_stable_ids` rejected; missing/empty `discovery.secret` rejected; missing
  exact model ID rejected; `model: coder` does not match catalog `coder:latest`; a local backend
  inventory entry renders; dead backend omitted from every role's `prefer`; a role whose only
  backend is dead is a render error, not a file; an empty roles map is a migration error.
- `internal/config`: migrate a fixture with all peers offline reproduces every field, including
  `labels`, `endpoints`, `webhooks`, `allowed_internal_hosts`, `ssh_port`, timeouts and
  `system_reserve_mb`; a node without `stable_id` fails; an existing `ai.yaml` with local backends
  and roles is backed up and preserved; missing `--hub-url` with no hub in `ai.yaml` is a named
  error.
- `cmd/axis`: `discover` and `diff` leave live files byte-identical; `render --out` with a file path
  for `--scope all` is an error; with a directory it writes both files; `apply` writes both files,
  lists both backups, prints the restart pointer when discovery/membership changed, and refuses
  without `--yes` on non-TTY stdin.
- `cmd/axis`: a failed second rename restores the first file byte-for-byte and still prints both
  backup paths; a hub 503 during `apply` leaves both live files byte-identical; an unallowlisted
  peer is excluded; `axis init` on a generated file leaves it byte-identical.
- `cmd/axis`: `verify` passes offline with discovery and hub unreachable; `doctor` warns on a
  digest mismatch and names the file; a one-byte `hostname` edit is detected; a legacy headerless
  file is a doctor warning and a `verify` error.
- `cmd/axis`: `diff` returns 0 equal, 1 on drift, 2 on a config error.
- Golden files for a four-node fixture and a three-role fixture, with RFC 5737/RFC 2606 addresses
  and no real hostnames, model ids or roles.
- Failing checks: remove the allowlist filter → unallowlisted-peer test fails; remove exact model
  matching → `coder`/`coder:latest` test fails; remove prefer pruning → role-backend test fails;
  remove `WriteGenerated`'s header → `verify` test fails; remove rollback → second-rename test
  fails; remove `--yes` refusal → non-TTY test fails.

## Alternatives

1. **Keep the `axis init` wizard as the only writer.** Rejected: it is per-node, interactive and
   has no diff or fleet check. It produced the drift.
2. **Ship an external shell renderer now.** Works as an immediate stopgap and is already the
   operator's fallback, but it cannot enforce schema, allowlist membership, atomic backups or a
   doctor-visible digest. This spec keeps that option as the migration path, not the product.
3. **Runtime-only resolver first.** The right end state, but it changes the loader and daemon
   contract before the operator has a generated artifact to compare. Phase 1 is a safe, testable
   intermediate.
4. **One canonical file copied to every node.** Rejected: per-node endpoints, local sections and
   model overrides make byte-identical copies wrong, and a copy still has no drift check.
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
