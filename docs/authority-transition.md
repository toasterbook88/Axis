# AUTH-10: Authority Transition Protocol

## 1. Purpose

This document defines the safe-transition lifecycle for moving canonical authority from one subsystem (or storage file) to another without breaking operator trust or introducing split-brain state.

Concrete AXIS examples covered:

- `internal/state/state.go` (`~/.axis/state.json`) → `internal/reservation/ledger.go` (`~/.axis/ledger.json`) for reservation authority.
- `internal/daemon/client.go` freshness backfill → `ClusterSnapshot.Timestamp` as the single freshness source.
- Future transitions (e.g., `state.json` `NodeState` exec tracking → ledger-only execution records).

---

## 2. Transition Lifecycle Phases

All authority moves must follow the six-phase sequence below. Skipping a phase is a protocol violation.

| Phase | Name | Goal | Operator Impact |
|-------|------|------|-----------------|
| 1 | **Dual-write** | New authority receives every write that goes to the old authority. | None — old authority is still read. |
| 2 | **Shadow-read** | New authority is read in parallel with the old authority; results are compared but the old authority still wins. | Observability only — divergence is logged/warned. |
| 3 | **Read-cutover** | New authority becomes the primary read source; old authority is read only as a fallback (zero-value fallback). | Reads now reflect new authority. |
| 4 | **Snapshot-demotion** | Old authority stops being included in `ClusterSnapshot` and API responses. | CLI/API no longer expose old fields. |
| 5 | **Mutation-prohibition** | Writes to the old authority are rejected or redirected to the new authority. | Old paths are hard-deprecated. |
| 6 | **Legacy-removal** | Old code and files are deleted after a deprecation window. | Zero footprint. |

### 2.1 Phase 1 — Dual-write

Both authorities must receive identical mutations. The new authority must not miss any writes that the old authority accepts.

**Concrete AXIS gap today:**

`internal/execution/guarded.go` writes reservations to the ledger but does not mirror the same reservation metadata into `state.json` `NodeState` fields (`ActiveExecs`, `ExecReservationsMB`, etc.). This means `state.json` does not receive the write, violating dual-write.

**Required invariant:**

```bash
# Every ledger.Reserve / ledger.Release / ledger.Heartbeat should have
# a corresponding state.json write (or a documented exception)
grep -rn 'ledger\.Reserve\|ledger\.Release\|ledger\.Heartbeat' internal/ --include='*.go' | grep -v '_test.go'
```

### 2.2 Phase 2 — Shadow-read

The new authority is queried alongside the old authority. If the answers differ, a warning is emitted but the old authority's answer is still returned.

**Concrete AXIS example:**

`internal/snapshotview/overlay.go` already performs a shadow-read for reservations:

```go
reserved := int64(0)
if ledger != nil {
    reserved = ledger.NodeSummaryFor(node.Name).ReservedRAMMB
}
if reserved <= 0 && st != nil && st.Nodes != nil {
    if ns, ok := st.Nodes[node.Name]; ok {
        reserved = ns.ReservedMB
    }
}
```

This is a **read-cutover** (phase 3), not a shadow-read, because there is no divergence warning. A proper shadow-read would log when `ledger.ReservedRAMMB != state.ReservedMB`.

### 2.3 Phase 3 — Read-cutover

The new authority becomes the primary source. The old authority is consulted only when the new authority returns a zero value.

**Current AXIS state:**

- `Ledger.AllocatableRAM()` is authoritative for placement.
- `state.json` `ReservedMB` is a fallback when the ledger value is zero.
- This means the state fallback can mask ledger under-reporting.

**Cutover checklist:**

- [ ] All placement callers use `ledger.AllocatableRAM()`.
- [ ] All status display callers use `ledger.NodeSummaryFor()`.
- [ ] State fallback is retained only for backward compatibility and is scheduled for removal.

### 2.4 Phase 4 — Snapshot-demotion

Old-authority fields are removed from `ClusterSnapshot` and API responses. Consumers that previously read the old field must migrate to the new surface.

**In AXIS today:**

`ClusterSnapshot` does not embed `ReservedMB` directly; it is applied via `snapshotview.ApplyReservationView()`. Demotion here means removing the state-file fallback inside `overlay.go` so only the ledger contributes to `RAMReservedMB`.

### 2.5 Phase 5 — Mutation-prohibition

Writes to the old authority are rejected or redirected.

**Concrete target:**

`internal/state/state.go:Load()` must stop calling `runMaintenance()`, which mutates `state.json` on every read. Maintenance should be moved to an explicit `Maintain()` or deleted entirely if the ledger already performs the same reclamation.

### 2.6 Phase 6 — Legacy-removal

After a deprecation window (minimum one minor release), the old code and files are removed.

**Deprecation schedule:**

| Milestone | Action |
|-----------|--------|
| v0.11.x | Dual-write enabled; shadow-read warnings emitted. |
| v0.12.0 | Read-cutover: ledger wins; state fallback logs a warning. |
| v0.13.0 | Snapshot-demotion: state fallback removed from overlay. |
| v0.14.0 | Mutation-prohibition: `state.json` maintenance deleted; state writes rejected. |
| v0.15.0 | Legacy-removal: `NodeState.ReservedMB`, `ActiveExecs`, and related fields deleted. |

---

## 3. Compatibility Guarantees During Transitions

### 3.1 Read Compatibility

- Old consumers must continue to see correct (or conservatively safe) data during phases 1–3.
- A consumer reading `state.json` directly must not see reservation data that contradicts the ledger after read-cutover.

### 3.2 Write Compatibility

- New consumers writing to the new authority must not break old consumers that read the old authority.
- If the old authority cannot receive the write, the operation must fail loud (not silently diverge).

### 3.3 Roll-forward Safety

- Daemon restart during any phase must not lose in-flight transitions.
- The on-disk representation of both authorities must be durable before the in-memory cutover is acknowledged.

---

## 4. Rollback Procedure

If a cutover causes production issues, rollback must be possible within one daemon restart or CLI invocation.

| Step | Action | Verification |
|------|--------|--------------|
| 1 | Stop new-authority writes (feature flag or code revert). | `grep` confirms no `ledger.Reserve` without state mirror. |
| 2 | Restore old-authority as primary read source. | `overlay.go` reverted to prefer state. |
| 3 | Replay any missed writes from new-authority back to old-authority. | Diff `ledger.json` against `state.json`; manual migration if needed. |
| 4 | Emit `RepairEvent` or structured log marking the rollback. | Visible in `axis doctor` and slog output. |
| 5 | Resume dual-write before attempting cutover again. | Phase 1 invariant passes. |

**Rollback time budget:** < 5 minutes for a running daemon; < 1 minute for a CLI invocation.

---

## 5. Divergence Detection

### 5.1 What Divergence Looks Like

| Scenario | Old Authority | New Authority | Risk |
|----------|---------------|---------------|------|
| Ledger reservation missing | `state.json` shows `ReservedMB = 512` | `ledger.json` shows no entry | Placement over-allocates because ledger says RAM is free. |
| State maintenance reclaimed early | `state.json` shows `ReservedMB = 0` | `ledger.json` shows active entry | Placement under-allocates because overlay falls back to zero. |
| Heartbeat desync | `state.json` `ExecHeartbeatAt` is fresh | `ledger.json` `LastHeartbeat` is stale | State hides a stale ledger entry that placement should ignore. |

### 5.2 Detection Invariants

```bash
# 1. Detect reservation divergence between ledger and state
diff <(jq -S '.entries | map({node, ram_mb})' ~/.axis/ledger.json) \
     <(jq -S '.nodes | to_entries | map({node: .key, ram_mb: .value.reserved_mb})' ~/.axis/state.json)

# 2. Detect overlay fallback usage (shadow-read without warning)
grep -n 'ReservedMB\|reserved' internal/snapshotview/overlay.go

# 3. Detect state-only writes that bypass the ledger
grep -rn '\.ReservedMB\s*=' internal/ --include='*.go' | grep -v '_test.go' | grep -v 'internal/reservation/'

# 4. Detect Load() mutations that should be ledger-only
grep -n 'runMaintenance\|Save()' internal/state/state.go
```

### 5.3 Automated Checks

A transition is not complete until the following automated checks pass for 7 days:

1. Zero divergence warnings in daemon logs.
2. Zero `state.json` writes triggered by read paths.
3. `ledger.json` and `state.json` reservation totals match within 1 MB.

---

## 6. Cutover Checkpoints

Before advancing from one phase to the next, the following checkpoints must be met:

### Phase 1 → 2 (Dual-write → Shadow-read)

- [ ] Every write path updates both authorities.
- [ ] Unit tests assert that `ledger.Save()` and `state.Save()` are both called.
- [ ] Shadow-read comparison function exists and emits structured logs.

### Phase 2 → 3 (Shadow-read → Read-cutover)

- [ ] 7 days of production logs show zero divergence warnings.
- [ ] `axis doctor` includes a divergence check.
- [ ] Documentation updated to tell operators the new authority is primary.

### Phase 3 → 4 (Read-cutover → Snapshot-demotion)

- [ ] All internal callers migrated to new authority.
- [ ] API/MCP surfaces return new-authority fields exclusively.
- [ ] No external consumers known to read old-authority fields.

### Phase 4 → 5 (Snapshot-demotion → Mutation-prohibition)

- [ ] Old-authority write paths are gated behind a `deprecated` guard that returns an error.
- [ ] Migration guide published for operators with custom scripts reading old files.
- [ ] Backup of old-authority data taken before prohibition.

### Phase 5 → 6 (Mutation-prohibition → Legacy-removal)

- [ ] One full minor release has passed since prohibition.
- [ ] No bug reports linked to the removed authority.
- [ ] Code size reduction is measured and documented in release notes.

---

## 7. Summary Table

| Property | Requirement |
|----------|-------------|
| Phase sequence | 1→2→3→4→5→6; no skips |
| Dual-write duration | Minimum 1 release cycle |
| Shadow-read duration | Minimum 7 days of zero divergence |
| Read-cutover fallback | Zero-value fallback only; no semantic fallback |
| Snapshot demotion | Remove old field from all API/CLI/MCP surfaces |
| Mutation prohibition | Return error on old-authority write; do not silently redirect |
| Legacy removal | Minimum 1 minor release after prohibition |
| Rollback time | < 5 minutes for daemon; < 1 minute for CLI |
| Divergence detection | Structured log + `axis doctor` check + automated diff |
