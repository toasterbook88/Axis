# AXIS Future Roadmap

Last reviewed: 2026-04-01 EDT

## Why This Document Exists

AXIS now has enough code and enough possible directions that it needs an explicit
future-facing strategy.

This document answers three questions:

1. What paths are available from the current codebase?
2. Which paths fit AXIS's intended purpose?
3. In what order should those paths be pursued?

This roadmap should be read alongside:

- [Current State](current-state.md)
- [Doctrine](doctrine.md)
- [RAM Balancing Research](ram-balancing-research.md)
- [White Paper v1](white_paper_v1.md)

## Starting Position

The strongest part of AXIS today is still the fact plane:

`config -> discovery -> facts -> snapshot -> placement`

That is the product's clearest identity and the least crowded part of the
market.

The repo also now contains chat, execution, scripts, skills, MCP, an HTTP API,
and local state. Those can be useful, but they only strengthen AXIS if they stay
subordinate to the fact plane.

AXIS also has a natural adjacent strength in Git-aware operator support:
understanding repository state, surfacing meaningful diffs, and helping route
code-review and repo-analysis work to the right node without turning into a
general-purpose forge.

## Strategic Options

### Path 1: Fact Plane Excellence

Make AXIS the best small-cluster source of truth.

Focus areas:

- stronger local and remote fact collection
- faster and more reliable SSH transport
- better network/topology visibility
- better snapshot warnings and degraded-state reporting
- stronger placement reasoning
- much better test coverage for runtime-critical packages
- **empirical feedback loops**: measure actual peak RAM/VRAM and execution
  time during `axis task run`, persist to `state.json`, use observed history
  to self-correct placement heuristics over time (replaces keyword guessing
  with measured reality)
- **warm cache awareness**: detect currently loaded models (e.g. `ollama ps`,
  MLX processes) and score nodes higher when the requested model is already
  resident in GPU memory, eliminating unnecessary model load times
- **granular GPU hashing**: replace `len(GPUs) > 0` with `GPUInfo` structs
  carrying `Vendor`, `Model`, `VRAM_MB`, and `Capabilities` (`cuda`,
  `metal`, `vulkan`); probe via `nvidia-smi` / `system_profiler` so
  placement can cross-reference `PreferredBackends` with actual capabilities
- **I/O tiering**: detect storage class (`nvme`, `ssd`, `hdd`) via
  `/sys/block/*/queue/rotational` on Linux and `diskutil info` on macOS;
  penalize HDD nodes for heavy model loading or large data tasks
- **thermal and power probing**: read `pmset -g batt`/`pmset -g therm` on
  macOS, `/sys/class/power_supply/` and `/sys/class/thermal/` on Linux;
  disqualify nodes that are battery-critical or thermally throttled
- **tombstone immune system**: track task-hash → node failure history in
  `state.json`; if a specific task pattern repeatedly OOM-kills or crashes
  on a node, automatically exclude that node for that task class with an
  expiring blacklist entry

Why it fits:

- directly aligned with AXIS's purpose
- preserves the single-binary, local-first shape
- improves trust rather than adding surface area

Risk:

- less flashy than execution features
- requires discipline to avoid piling on adjacent ideas too early

Verdict:

**Primary path.**

### Path 2: MCP-Native Cluster Context Server

Turn AXIS into a great MCP server for cluster state and read-only diagnostics.

Focus areas:

- stable snapshot/resource exposure via MCP
- placement as a tool surface
- read-only system and network diagnostics
- compact, LLM-friendly output contracts
- snapshot caching for MCP sessions

Why it fits:

- extends the fact plane without changing the product's identity
- makes AXIS more useful to external agents and editors
- keeps execution optional

Risk:

- transport and schema quality must stay high
- easy to over-expose tools before they are hardened

Verdict:

**Best extension path after fact-plane hardening.**

### Path 3: Network-Aware Cluster Intelligence

Make AXIS aware of connectivity quality and cluster topology, not just hardware.

Focus areas:

- Tailscale awareness
- SSH reachability quality
- interface labeling and route hints
- local/remote/link speed awareness
- transport and peer diagnostics
- **overlay network routing**: recognize overlay subnets (100.x for
  Tailscale, 10.x for WireGuard) and apply latency penalties so heavy
  data tasks prefer direct links while lightweight API calls can use
  overlays
- **compute pairs**: detect nodes sharing high-speed links (Thunderbolt
  bridge / 10GbE) via subnet analysis and prefer co-placing distributed
  workloads on the fast pair
- **secure service routing (tunneling)**: use existing SSH sessions to
  create ephemeral `LocalForward`/`RemoteForward` tunnels so placed tasks
  expose services on `localhost` without managing IPs or firewall rules;
  agents request `axis task run --expose-port <remote>:<local>` and get a
  zero-trust, zero-config tunnel torn down when the task finishes

Why it fits:

- directly improves placement and operator decision-making
- strengthens "cluster truth" rather than replacing it

Risk:

- can turn into ad hoc networking sprawl without a clear model

Verdict:

**High-value secondary path.**

### Path 4: Local/Cloud Model Routing

Let AXIS choose the best available model surface for operator tasks.

Focus areas:

- best local model defaults
- cloud model options when local models are too weak
- clear UX for model selection and failover
- explicit separation between local and cloud execution

Why it fits:

- complements the existing chat surface
- matches current Ollama capabilities
- helps AXIS act as an operator console for local + cloud AI

Risk:

- can slowly turn AXIS into "chat tool first, cluster tool second"
- tempting to chase model churn instead of product identity

Verdict:

**Useful, but should remain supportive rather than central.**

### Path 5: Git-Aware Operator Intelligence

Make AXIS excellent at repository-aware assistance as well as networking and
cluster state.

Focus areas:

- strong Git state understanding from live working trees
- better repo-analysis and review-oriented task inference
- compact, truthful repo context for chat, MCP, and execution prompts
- Git-aware scripts and runbooks that stay explicit and operator-driven
- safer handling of dirty trees, branch state, and verification workflows

Why it fits:

- Git is already one of the most important operator tools AXIS detects and uses
- it complements cluster placement instead of competing with it
- it makes AXIS more useful for real coding and review workflows

Risk:

- can sprawl into generic developer-assistant behavior if not tied to live repo truth
- must stay grounded in explicit Git state, not vague summarization

Verdict:

**High-value support path, alongside MCP and network intelligence.**

### Path 6: Explicit Supervised Execution

Allow operators to use AXIS to run tasks on the selected node, but only with
strong consent and safety.

Focus areas:

- strict confirmation model
- clearer execution plans
- better state lifecycle and reservation accounting
- per-run context instead of shared temp files
- hardened safety and auditability

Why it fits:

- builds naturally on placement
- can be genuinely useful for small clusters

Risk:

- execution complexity can quickly outrun the fact plane
- hidden automation or broad script matching would damage trust

Verdict:

**Allowed path, but only if execution stays explicit and conservative.**

### Path 7: Lightweight Snapshot Cache or `axisd`

Introduce a lightweight background helper only after the fact plane is mature.

Focus areas:

- cached snapshots
- periodic collection
- lower-latency MCP and placement responses
- optional background collection rather than mandatory runtime architecture

Why it fits:

- solves real performance problems once discovery is trustworthy

Risk:

- can accidentally invert the architecture and make AXIS daemon-first

Verdict:

**Deferred path. Only after earlier phases are boringly reliable.**

### Path 8: Full Orchestrator / Scheduler

AXIS could theoretically evolve toward a richer scheduler or orchestration
system.

Examples this would resemble conceptually:

- Nomad-style workload placement
- Ray-style distributed execution
- Ansible-style fleet execution and automation

Why it does not fit well today:

- it changes the product identity
- it enters crowded, mature markets
- it makes the current single-binary local-first value proposition much weaker

Verdict:

**Do not pursue unless the project is deliberately reframed.**

## Recommended Strategic Direction

The best future for AXIS is:

1. **fact plane first**
2. **MCP-native cluster context second**
3. **network-aware cluster intelligence third**
4. **Git-aware operator intelligence as a support layer**
5. **local/cloud model routing as a support layer**
6. **explicit execution only when trust is high**

The best future for AXIS is **not** "become a general orchestrator."

AXIS should aim to become the cluster truth and tool surface that other agents
use, rather than the heavyweight runtime that tries to replace them.

## 6-Month Recommended Roadmap

### Phase A: Trust and Foundations

Goal:

Make the fact plane boringly reliable.

Priority work:

- harden `internal/facts`
- harden `internal/transport`
- improve discovery timing and semantics
- fix state accounting drift in placement
- turn the RAM-balancing design in `ram-balancing-research.md` into an implementation plan
- add tests for discovery, transport, safety, state, skills, and MCP
- align `README.md` and design docs with the live command surface
- add post-execution resource measurement to `task run` (peak RAM/VRAM, wall
  time) and persist observations to `state.json` for empirical placement
- extend tool probes to detect currently-loaded models (`ollama ps`) and
  score placement higher when the needed model is already warm in GPU memory
- upgrade GPU discovery from `len(GPUs) > 0` to structured `GPUInfo` with
  vendor, model, VRAM, and capabilities (`cuda`/`metal`/`vulkan`)
- add storage-class detection (NVMe vs SSD vs HDD) and I/O-tier penalties
- add thermal/power probing (battery %, throttle state) for mobile nodes
- implement tombstone blacklisting: task-hash → node failure history in
  `state.json` with expiring entries to prevent OOM crash loops

Exit criteria:

- facts, transport, and placement are trustworthy
- docs match live behavior
- critical runtime packages are no longer untested
- placement can use empirical history when available, falling back to
  heuristics when not
- warm-model scoring visibly improves placement for repeated inference tasks
- GPU placement distinguishes real compute GPUs from integrated graphics
- HDD nodes penalized for large model loads
- repeated crash patterns auto-excluded via tombstone

### Phase B: MCP First-Class

Goal:

Make AXIS a strong MCP-native cluster context provider.

Priority work:

- stabilize `axis mcp serve`
- cache snapshots per MCP request/session
- make resource/tool schemas clearer
- ensure all exposed tools are read-only by default
- add integration tests around MCP behavior

Exit criteria:

- MCP is stable enough for editor/agent use
- no repeated full-cluster rediscovery for every small tool call

### Phase C: Git Intelligence

Goal:

Make AXIS excellent at live-repo reasoning and Git-aware operator workflows.

Priority work:

- improve repo-aware task inference and script routing
- add compact Git context surfaces for prompts and MCP
- harden dirty-tree, branch, and diff handling
- add explicit Git runbooks and verification flows

Exit criteria:

- AXIS can explain repo state clearly from live Git data
- Git-oriented tasks feel first-class without turning AXIS into a generic forge

### Phase D: Network-Aware Placement

Goal:

Make placement understand more than RAM and tool presence.

Priority work:

- Tailscale status integration
- stronger SSH reachability diagnostics
- richer address/interface metadata (subnet, interface name, speed class)
- optional locality and route-quality hints
- overlay subnet detection (Tailscale 100.x, WireGuard 10.x) with
  latency-aware placement penalties
- compute-pair identification via shared high-speed subnets
- ephemeral SSH port forwarding (`LocalForward`/`RemoteForward`) for
  secure service routing of placed tasks

Exit criteria:

- network context improves placement and operator reasoning
- AXIS can explain not just "what node," but "why that path is viable"

### Phase E: Model Routing Layer

Goal:

Turn AXIS chat into a clean operator surface for local and cloud models.

Priority work:

- best local model auto-selection
- explicit cloud model catalog and switching
- clearer missing-model / timeout / daemon diagnostics
- optional routing rules based on cluster capability

Exit criteria:

- chat is useful without becoming the product center
- model routing stays explicit and understandable

### Phase F: Explicit Execution Hardening

Goal:

Keep execution useful without damaging trust.

Priority work:

- make all execution surfaces explicit and consistent
- eliminate shared temp-file contracts
- tighten script matching and prerequisites
- improve state lifecycle and rollback behavior
- make destructive actions require stronger confirmation

Exit criteria:

- operators can predict what AXIS will do before it runs
- execution no longer outruns the fact plane

### Phase G: Optional Cache Daemon

Goal:

Add performance without changing the product's identity.

Priority work:

- cached snapshots
- optional background refresh
- minimal daemon shape if still justified

Exit criteria:

- `axisd` remains optional
- AXIS still makes sense as a local-first single-binary tool

## Strategic Guardrails

### Do More Of

- explicit contracts
- read-only surfaces
- truthful degraded-state reporting
- deterministic placement
- operator-visible reasoning
- runtime verification and runbooks

### Avoid

- implicit execution
- hidden downloads
- brittle temp-file conventions
- background complexity before trust is earned
- scheduler ambition without a product reset
- feature growth that makes AXIS less legible

## Decision Gates For Future Features

Before landing a feature, ask:

1. Does this improve fact quality, placement quality, or cluster truth?
2. Does it preserve AXIS as a single-binary, local-first tool?
3. Does it keep execution explicit rather than hidden?
4. Does it make the system more understandable to operators?
5. Does it add testable contracts rather than folklore?

If the answer is "no" to the first three, the feature should probably not ship.

## Adjacent Ecosystem Notes

The broader ecosystem matters because it clarifies where AXIS should and should
not compete.

- MCP is a strong adjacent standard and an opportunity for AXIS.
- Ollama cloud models make local/cloud routing practical without inventing a new
  runtime shape.
- Tailscale SSH is a natural complement to cluster-aware operations.
- Nomad, Ray, and Ansible-style execution ecosystems are useful reference points,
  but they are not the lane AXIS should copy by default.

## Bottom Line

The best future for AXIS is to become:

- a reliable cluster fact plane
- a deterministic placement advisor
- a strong MCP-native context server
- a careful operator-facing tool for model and task routing

The wrong future is to become a vague orchestration platform before the current
truth plane is fully hardened.
