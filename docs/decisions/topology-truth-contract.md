# Decision: Topology Truth Contract

**Date:** 2026-07-25
**Scope:** `internal/models` (snapshot vantage metadata), `internal/snapshot`, `cmd/axis/dashboard.go`, `cmd/axis/summary_test.go`
**Decision:** RENDER VANTAGE-LABELED ROUTES ONLY — AXIS does not claim node-to-node edges
**Evidence:** `docs/evaluations/2026-07-25-truth-integrity-audit.md` (C1, A2, and "What AXIS does not currently possess")

> **Prior-record note.** The pairwise topology block has no decision record. `docs/decisions/dashboard-command.md` (2026-05-17) enumerates the dashboard's contents in §1 and does not list it, so the block postdates that record. That §1 inventory is stale and should be refreshed when this contract lands.

---

## 1. Context

`axis summary` renders a "CLUSTER TOPOLOGY" block that draws links between node pairs. It infers a link when two nodes report an identical CIDR string, and labels that link with the better of the two nodes' NIC `speed_class`.

Subnet equality is not reachability. On the audited grid, two nodes at physically distinct sites both report the same RFC1918 /24; one is reachable only via relay, and its LAN address was verified unreachable from the observing host. Ten edges were rendered without valid edge evidence; four are demonstrably false.

The same inference silently omits nodes: every Darwin node lacks a CIDR (A2), so the pairing loop skips them entirely. Two omitted nodes are genuinely LAN-local.

The deeper problem is that **AXIS possesses no node-to-node edge data at all.** What it holds is:

- `NetworkClass` — classification of the path *from the observing host* to a node
- `SSHHandshakeLatencyMs` — SSH handshake duration *from the observing host*, not a network round-trip
- `Addresses[].SpeedClass` — a property of a NIC on one node, not of any link

All three are either vantage-relative or single-node attributes. None describes a link between two other machines. Any rendering that presents them as edges asserts something the substrate cannot observe, which is the failure the Truth Rule exists to prevent.

## 2. Decision

AXIS renders **routes**, labeled with the vantage point from which they were observed. It does not render edges.

1. A **vantage point** is the node that performed discovery for a given snapshot. It is recorded in the snapshot at assembly time.
2. A **route** is an observed `vantage → node` path, carrying network class, SSH handshake duration, source, and observation time.
3. An **edge** is a link between two nodes, at least one of which is not the vantage. **AXIS does not render edges, and no surface may synthesize one.**
4. **Node attributes** (NIC speed class, interface name, subnet) may be rendered against the node they belong to. They may never be used as, or promoted to, link properties.

## 3. Schema

Most required data already exists on `NodeFacts`. One addition is needed.

### 3.1 Snapshot vantage (new)

`ClusterSnapshot` currently has no origin field, so the observing host is implicit. This is incorrect for cached reads: `axis status --cached` may return a snapshot assembled by a daemon running on a different machine, and rendering it as though the CLI host were the vantage would misattribute every route.

Add to `ClusterSnapshot`:

```go
// Vantage identifies the node that performed discovery for this snapshot.
// Routes in this snapshot describe paths observed FROM this node.
Vantage *VantageInfo `json:"vantage,omitempty" yaml:"vantage,omitempty"`
```

```go
type VantageInfo struct {
    NodeName  string    `json:"node_name" yaml:"node_name"`
    StableID  string    `json:"stable_id,omitempty" yaml:"stable_id,omitempty"`
    ObservedAt time.Time `json:"observed_at" yaml:"observed_at"`
}
```

Populated by `snapshot.Build` from the collecting host's identity, using the existing stable-identity resolution. Never inferred at render time.

### 3.2 Route fields (existing, re-contracted)

No new fields. `NetworkClass` and `SSHHandshakeLatencyMs` on `NodeFacts` are hereby defined as **vantage-relative route properties**. Any surface rendering them must name the vantage.

A node whose route was never observed carries no class and must be rendered as unknown, not omitted.

## 4. Rendering contract

Binding on every surface (CLI, dashboard, HTTP, MCP, agent, chat).

| Rule | Requirement |
| --- | --- |
| **R1** | Any rendering of route data must name its vantage node. |
| **R2** | No connector may be drawn between two nodes when either is not the vantage. |
| **R3** | NIC properties (`speed_class`, `interface`, `subnet`) render as node attributes only. Never as link labels. |
| **R4** | A node with no route observation is rendered present with an explicit unknown state. Omission is forbidden. |
| **R5** | When the snapshot is cached or stale, route rendering must show observation age. |
| **R6** | Subnet equality must not be used as evidence for any rendered claim. |
| **R7** | The vantage node renders as self/local, not as a route to itself. |
| **R8** | SSH handshake duration must not be labeled "latency", "RTT", or "ping". It is a handshake duration and must be named as such. |

### 4.1 Replacement rendering for `axis summary`

The pairwise topology block is removed. In its place, a reachability section:

```
  REACHABILITY (observed from <vantage>, <age> ago)
  ─────────────────────────────────────────────────
  <node>        direct-lan    handshake  38ms
  <node>        direct-lan    handshake 125ms   thunderbolt iface present
  <node>        relayed       handshake 212ms
  <node>        unknown       no route observed
  <vantage>     local         —
```

This is honest, decision-relevant, and strictly more informative than the block it replaces: it covers all nodes rather than a subset, and it distinguishes relayed from LAN paths, which the previous rendering actively concealed.

## 5. Rejected alternative: pairwise probing

Measuring true edges requires each node to probe every other node — N×(N−1) connections, initiated remotely.

Rejected for this contract because it would require AXIS to originate node-to-node execution as a background activity, which contradicts `docs/doctrine.md` ("agentless by default", "not a background scheduler", "no hidden agent runtime") and would expand the fact plane from *observe the grid* to *drive the grid*.

If verified edges are wanted later, they require their own decision record covering execution authority, failure semantics, cost at N nodes, and operator consent. This contract does not prejudge that; it forbids **synthesizing** edges, not measuring them under a future explicit mandate.

## 6. Relationship to A2 (Darwin CIDR)

The audit recorded an ordering constraint: fixing A2 before C1 would let Darwin nodes join the unsupported mesh.

**This contract dissolves that constraint.** Once subnet equality is no longer evidence for anything rendered (R6), populating Darwin CIDRs cannot produce false edges. A2 becomes an independent fact-plane correctness fix — worth doing, since `subnet` should be accurate regardless — and may land in any order after this contract.

## 7. Non-goals

- No node-to-node probing, and no measurement of true edges
- No RTT, bandwidth, or throughput measurement
- No change to placement scoring or ranking (see the separate placement selection ADR for B2/B3/B4)
- No mesh/gossip-derived edge inference
- No graphical topology rendering

## 8. Acceptance criteria

| ID | Test | Pass condition |
| --- | --- | --- |
| **T1** | Two nodes reporting an identical CIDR, one relayed | Zero connectors rendered between them |
| **T2** | Node with `subnet: null` (Darwin) | Present in reachability output with its route |
| **T3** | Cached snapshot assembled by a non-local daemon | Vantage names the daemon host, not the CLI host |
| **T4** | Node with no route observation | Present with explicit unknown; not omitted |
| **T5** | Golden output across a mixed fixture | No line contains a pairwise connector glyph |
| **T6** | Any surface rendering route data | Vantage label present (R1) |
| **T7** | Snapshot with stale freshness | Observation age rendered (R5) |

T1 and T2 are regressions of the exact live failures in the audit and should use fixtures derived from them: two same-CIDR nodes at distinct sites, and a Darwin node with populated `speed_class` but null `subnet`.

## 9. Consequences

**Removed.** The pairwise link-inference loop and its `speedPriority` helper in `cmd/axis/dashboard.go`. The visual mesh diagram is lost.

**Gained.** A reachability view covering every node, that distinguishes relayed from LAN paths and never claims an unobserved link. Existing tests asserting the old topology block must be replaced, not adjusted — they encode the rejected inference.

**Operator-visible change.** Operators accustomed to the mesh diagram will see a list instead. The diagram conveyed a cluster picture that was partly false and silently incomplete; the list conveys less, and all of it is observed. This is the intended trade.

**Cost.** One schema field, one rendering replacement, no new probes, no added latency.

## 10. Public repository boundary

Test fixtures and documentation use symbolic node names and RFC 5737 documentation addresses. No grid hostnames, private or relay addresses, operator home paths, or model catalogs, consistent with `docs/decisions/inference-roles-and-backends.md` §3.

The shared-subnet regression fixture (T1) must be expressed with documentation ranges, not the operator's actual private range.
