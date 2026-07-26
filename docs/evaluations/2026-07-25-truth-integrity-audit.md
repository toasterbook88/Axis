# Truth-Integrity Audit — 2026-07-25

**Status:** Non-canonical audit. Observational only. Does not override `docs/current-state.md`, `docs/invariants.md`, or `docs/doctrine.md`.

**Tested binary:** `0.14.7`, commit `86b2335`, `linux/amd64`, Go 1.26.5
**Repo HEAD at audit time:** `d594757` (delta from tested binary is docs-only: 2 lines in `docs/current-state.md`)
**Method:** Live CLI invocation against an 8-node operator grid, plus targeted source reads and isolated shell reproduction. No repository files were modified during evidence gathering.

**Sanitization:** Node names are symbolic. Addresses, hostnames, usernames, relay endpoints, and model catalogs are omitted per the public-repository boundary in `docs/decisions/inference-roles-and-backends.md`.

| Symbol | Description |
| --- | --- |
| `node-hub` | Linux x86, the vantage point where `axis` was invoked |
| `node-big` | Linux x86, largest RAM + discrete GPU, reached via relay, physically remote |
| `node-lin-2`, `node-lin-3` | Linux x86, LAN-local |
| `node-arm` | ARM SBC, LAN-local |
| `node-mac-1`, `node-mac-2`, `node-mac-3` | Apple Silicon |

---

## Thesis

Every defect below has one shape: **a surface asserts something that the layer beneath it contradicts, or the fact plane asserts something the host contradicts.**

This is the failure mode the Truth Rule exists to prevent. It is not confined to the advisory LLM helpers, where such risk is usually assumed to live: it appears in the diagnostic and presentation surfaces operators treat as reliable (C1, C4), in the fact plane itself (A1, A2), and also in an advisory surface (C2).

---

## Findings

### A. Fact plane (Layer 1)

#### A1 — `ollama.running` reports `false` on a running server

`internal/facts/tools.go` (`OllamaDiscoveryScript`).

Two compounding defects:

1. `pgrep -f "$OLLAMA_BIN"` matches the probe's *own* shell, because the script text literally contains the binary path in its fallback candidate list. The probe detects itself.
2. The resulting multi-line PID value is consumed by `[ -n \"$PGREP\" ]` inside a nested command substitution. Word-splitting produces a malformed `test` invocation, which errors and yields `false`.

Live output from a single probe invocation:

```json
{"installed":true, "running":false, "listening":true, "port":11434, "models":[<25 entries>]}
```

`running:false` alongside `listening:true` and 25 enumerated models — models obtainable only from a live server. The object contradicts itself.

Instrumented value of `PGREP` during a real probe: three PIDs, of which one is the server and two are the probe's own processes.

**Consumers:** `ollamaIsReady()` (`internal/placement/ranker.go:860`), used as a filter in `explain.go`, `ranker.go`, `selector.go`; and `axis doctor`.

#### A2 — Darwin nodes carry `subnet: null`

All three Apple Silicon nodes report addresses with populated `speed_class` (including `thunderbolt`) but no CIDR.

Root cause is the **remote** collection path. `internal/facts/remote_bundle.go` and `internal/facts/remote.go` select between two branches:

- Linux takes `ip -o addr show scope global` → address arrives with prefix (`.../24`)
- macOS falls back to `ifconfig … awk '… {print iface, $2}'` → `$2` is the bare address; `netmask 0xffffff00` sits at `$3 $4` and is discarded

The local collector populates CIDR correctly; only remote Darwin collection drops it. Existing Darwin tests inject Linux-style CIDR output, so the `ifconfig` branch is not exercised.

#### A3 — `os_version` carries two incompatible semantics

Darwin returns `sw_vers -productVersion` (a product version). Linux returns `uname -r` (a kernel version). One field, two meanings, no discriminator. Observed live: Darwin nodes reporting `26.x`/`27.0`; Linux nodes reporting kernel strings.

#### A4 — `parseVersionString` emits malformed versions

GCC's first output line yields a version with a trailing `)` retained. `TrimRight` handles `,` and `;` but not `)`. A clean token appears later on the same line and is never selected. Observed on multiple live nodes.

---

### B. Decision plane (Layer 3)

#### B1 — Workload taxonomy lacks Rust

Compositional matching works. `analyze this git repository` correctly classifies as `repo-analysis`.

The gap is coverage, not matching: `compile a rust project`, `build a rust binary`, and `cargo build` all return `unknown`. Rust is absent from the taxonomy in `docs/substrate-roadmap.md` §1.

#### B2 — Allocatable RAM is the primary ranking key for every workload

`RankCandidates` (`internal/placement/ranker.go`) documents its own priority order:

```
1. Highest allocatable RAM
2. Best exact-scope empirical observation (fresh only)
3. Resident model locality for the requested runtime
4. Preferred backend rank
5. GPU score
...
```

Keys 2–5 are reachable only on **exact** allocatable-RAM ties, which are vanishingly rare across heterogeneous hardware. Consequence: task-specific signal rarely influences selection, and this holds whether classification succeeds or fails.

Verified: a correctly classified `repo-analysis` task and an unclassified task selected the same largest-RAM node, with identical ordering across four distinct task descriptions.

#### B3 — The displayed score does not drive selection

Live output selects `node-big` at fit 60/100 while naming `node-hub` as runner-up at 100/100.

Ordering is by allocatable RAM (B2); the displayed score is a separate composite that incorporates network class, locality bonus, and GPU. Network and Thunderbolt bonuses move the *displayed* number but not the *decision*.

This is in tension with `docs/future-roadmap.md`, which lists operator-visible reasoning under "Do More Of." It is **not** a breach of the *Single Placement Contract* invariant, which governs decision-model consistency **across** surfaces; no cross-surface divergence was observed. The defect is that a single surface displays a number that does not explain its own choice.

#### B4 — No OS or architecture constraint exists

`TaskRequirements` (`internal/models/types.go`) carries no OS or arch field. Verified live: a macOS-only command was placed on a Linux node.

---

### C. Presentation and CLI surfaces

#### C1 — `axis summary` topology renders unsupported edges and omits nodes

`cmd/axis/dashboard.go` infers a link when `addrA.Subnet == addrB.Subnet` — string equality of CIDR notation, treated as evidence of a physical link.

Both failure directions occur simultaneously on the live grid:

- **Unsupported edges, at least four proven false:** `node-big` is at a different physical site that happens to use the same RFC1918 /24 as the LAN nodes. It is rendered as a "Gigabit LAN: 1 Gbps" peer of four nodes. Its LAN address was verified unreachable from `node-hub` by direct TCP probe; only its relay path connects. Those four edges are demonstrably false. The remaining six edges join nodes that are in fact LAN-local, so they may well be true — but they rest on the same invalid inference and are unverified. **Ten edges were rendered without valid edge evidence; four are proven false.**
- **Omitted:** every Apple node is excluded, because A2 leaves their `subnet` empty and the pairing loop skips empty subnets. Two of them are genuinely LAN-local, and one carries a Thunderbolt interface.

The same snapshot classifies `node-big` as `relayed`, and its network class and Thunderbolt signals move the **displayed** FitScore. Per B2 and B3 they do not influence selection, so this contradiction is one of operator-facing reasoning, not of the placement decision itself.

**Ordering constraint:** fixing A2 *before* C1 would make the three Darwin nodes string-match the shared /24 and join the unsupported mesh, expanding the defect. C1 may land first and independently; A2 must not precede it. This is an ordering requirement, not a requirement of atomic delivery.

#### C2 — `axis llm` reports maximum confidence on a non-classification

Live output pairs `Class: unknown` with `Confidence: 1.00 [reflex fallback]`, and surfaces a raw upstream error string (`ollama status 404`) as user-facing text. Reflex determinism is being represented as classification certainty. The default requested classifier model is not present locally.

#### C3 — Test execution writes into canonical operator state

`TestRunRemoteUsesVariableBasedTrap` (`internal/execution/guarded_test.go`) does not isolate `HOME`, while ten sibling tests in the same file do. The exercised path reaches `recordExecutionOutcome` (`internal/execution/guarded.go`), which persists to the real `~/.axis/state.json`.

Measured contamination on the audited host:

| Store | Contamination |
| --- | --- |
| `task_history` | 70 of 71 rows are the fixture node |
| `observations` | fixture scope holds 70 samples; the single genuine observation holds 1 |
| `state.json` (raw) | 141 occurrences of the fixture node name |
| `axis skills` | advertises the fixture node as `preferred_node` |

**Blast radius, stated precisely:** the fixture node is absent from `nodes.yaml`, so it is never a placement candidate. This does **not** misroute work today. The damage is (a) integrity of the empirical observation store, and (b) operator-visible advice recommending a node that does not exist.

Per `docs/authority-violations.md`, `~/.axis/state.json` is the **state authority**. A test writing into it is an authority-boundary violation, and every `make test` run deepens it.

#### C4 — `doctor` and `ai backends` disagree about the same service

`axis doctor` reports the local Ollama backend as "installed, not running." `axis ai backends` reports the same endpoint healthy with 25 models. Ground truth: process running, socket listening, systemd active.

`doctor` is not independently wrong — it faithfully renders A1. The disagreement exists because `ai backends` probes the HTTP endpoint directly while `doctor` consumes the fact plane.

---

## What AXIS does not currently possess

A correction to an earlier draft of this audit, recorded because the error is instructive.

AXIS has **no verified node-to-node edge data.**

- `network_class` and SSH handshake duration (`ssh_handshake_latency_ms`) describe **`node-hub` → node** routes. They are vantage-point-specific, not symmetric edge facts, and handshake duration is not a network round-trip measurement.
- The Thunderbolt signal detects the presence of an interface on a single node. It is not evidence of a link, still less of a link to any particular peer.

Any remediation that claims to "render the topology correctly from data AXIS already has" is unfounded. The honest near-term rendering is a **vantage-labeled reachability view** — "as observed from `node-hub`" — which is truthful and decision-relevant, while explicitly not claiming edges the substrate cannot see.

---

## Sequencing

Ordering is constrained by two facts: C3 worsens continuously until stopped, and A2 must not precede C1.

Note for anyone reproducing these findings: **do not run `make test` on a host with real operator state** until C3 is fixed. Doing so deepens the contamination documented in C3.

**Containment**

1. **C3** — isolate `HOME` in the offending test **and remediate existing state pollution**. Isolation alone stops the bleed but leaves 70 fixture rows and a 70-sample observation skewing the empirical store.
2. **A1 / C4** — exclude the probe's own processes from `pgrep`; repair the quoting in the `running` expression.
3. **C2** — decouple reflex determinism from confidence; stop surfacing raw upstream error strings.
4. **C1** — replace the pairwise topology block with a vantage-labeled reachability view.

**Spec 1 — Topology Truth Contract (C1 then A2)**

Define edge evidence, vantage point, source, freshness, and what may legally be rendered. Includes Darwin hex-netmask → CIDR conversion. C1 may ship ahead of A2; the reverse order is unsafe. This grid is its own regression fixture: two sites sharing an identical RFC1918 /24, where subnet equality is provably not reachability.

**Spec 2 — Placement selection ADR (B2 + B3 + B4)**

Reconcile RAM-primary ordering against the displayed composite score; define fallback behavior for `unknown`; add OS and architecture constraints to `TaskRequirements`.

Should precede taxonomy expansion. Classifier accuracy is independently measurable today — `axis profile match` can be scored against a labeled corpus without touching placement. What is not measurable is whether improved classification changes placement outcomes, because keys 2–5 are effectively unreachable (B2). Expanding the taxonomy first would therefore improve a number that cannot yet be shown to affect any decision.

**Then** — taxonomy coverage (Rust and peers, B1).

**Migration lane** — A3 `os_version` schema split, A4 version-string parsing.

---

## What this document is not

- Not a specification. No interface or schema is defined here.
- Not a statement of shipped behavior. It records observed defects in `0.14.7` at one point in time.
- Not canonical. Where it disagrees with live code, the code wins; where it disagrees with `docs/current-state.md`, that file wins.
- Not a claim that the advisory or LLM surfaces are the primary risk. The evidence points the other way.

## Method notes

Findings were established by live invocation first and source reading second.

Claims withdrawn from earlier drafts after review and direct probing contradicted them:

- that natural-language repository phrasing failed to classify (it classifies correctly; the gap is Rust taxonomy coverage)
- that correct topology data already existed in the substrate (it does not; no verified node-to-node edge data exists)
- that ten topology edges were fabricated (ten are unsupported; four are proven false)
- that network and Thunderbolt adjustments influence placement selection (they move the displayed FitScore only)
- that B3 breaches the Single Placement Contract (that invariant governs cross-surface consistency; no cross-surface divergence was observed)
- that classification quality is unmeasurable (it is directly measurable; its *effect on placement* is not)

Each had been inferred from adjacent evidence rather than measured — the same failure this audit documents elsewhere. They are listed rather than silently corrected so the correction record is auditable.
