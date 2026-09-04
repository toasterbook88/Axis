## Unreleased

### Features

* **Model placement planning:** Add `axis model plan <spec|weights>` to perform dry-run evaluation of cluster nodes for model placement without mutating services. Introduces canonical `ModelSpec` schema (`axis.model-spec/v1`) keeping format, weight size, context overhead, runtime overhead, and accelerator compatibility separate. Evaluates candidate nodes against available RAM, VRAM, and port availability; checks resident models for port collisions; scores eligible nodes deterministically with fit labels; provides reasoning; and supports `--format text|json|yaml`, `--port`, `--cache-addr`, and `--live`.
* **Model start:** Make `axis model start` cache-first by default, eliminating the 54-second full-cluster discovery sweep while supporting `--live` for explicit discovery. Add preflight port conflict verification against resident models. Emit typed `axis.model-operation/v1` lifecycle receipts with publication binding, supporting `--format text|json|yaml` and `--cache-addr` for local daemon authority.
* **Model stop:** Add generation-bound `axis model stop <generation-id>`. Resolves the model instance and host from the local daemon cache, revalidates process ownership, PID, executable, and process start token on the host before terminating, refuses to kill if the process generation changed, and writes a typed `axis.model-operation/v1` operation receipt with snapshot publication binding.
* **Model inventory:** Add a canonical read-only model-instance schema plus `axis model list` and `axis model inspect <instance-id>`. Inventory reads are daemon-cache-first for fast operator feedback; `--live` is an explicit fresh collection with no hidden fallback. Every instance preserves the observed node status and timestamp, while the inventory carries its snapshot source, publication ID, and warnings. Instance IDs are deterministic keys for the observed node/engine/model/port slot, not fabricated process IDs.

### Breaking

* **CLI exit contracts:** `axis doctor`, `axis daemon status`, and `axis model stop` now exit **4** when they report an unhealthy or no-op result, after writing their complete diagnostic output. They previously exited 0, so shell automation could not distinguish a healthy cluster from a broken one. There is no deprecation path for an exit code: **shell consumers that treat these commands as pass/fail must branch on exit 4.**
  * `axis doctor` exits 4 when any check is `fail`, in both bare and `--strict` mode. `--strict` is unchanged in meaning: it only promotes daemon-cache unavailability from `warn` to `fail`. Warning-only runs still exit 0, and the interactive setup-wizard path is unchanged — an accepted wizard's success and its error are both passed through.
  * `axis daemon status` exits 4 for `unavailable`, `stale`, `degraded`, and `incompatible` metadata. The machine-readable envelope is still written first and is unchanged; only the exit code is new. A failure to write that envelope still outranks the health disposition.
  * `axis model stop` now reports a typed disposition — `stopped`, `not_running`, `wrong_owner`, `inspection_unavailable`, or `generation_mismatch` — and exits 4 for everything except `stopped`. A port with no listener previously printed `stopped` and exited 0. Missing `fuser`/`lsof`/`ps` is reported as `inspection_unavailable`, never as `not_running`, because the port was never observed.

### Bug Fixes

* **Model truth:** Detect a running `llama-server` from process evidence even when its supervised absolute executable path is outside the collector's login `PATH`. Keep on-disk weight bytes, resident process RAM, and runtime-reported accelerator memory as separate facts (`weight_size_mb`, `size_ram_mb`, and `size_vram_mb`); llama.cpp model-file size and MLX RSS are no longer mislabeled as VRAM.
* **Daemon:** Make `axis daemon restart` readiness honest. Both the short-circuit and the post-restart poll now use one predicate requiring the exact current version, `Ready=true`, `Stale=false`, and an empty `LastError`; previously both checked only version and staleness, so a daemon that was not ready — or that was reporting a refresh error — was announced as "already fresh". A current-version daemon that is merely still starting gets one bounded 3-second grace period before at most one terminate/start cycle, so repeated restarts cannot churn a daemon that was about to come up. Wrong-version and stale daemons get no grace: exact version equality is an intentional split-binary guard for installations where the CLI and the supervised daemon run from different filesystem paths.

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
