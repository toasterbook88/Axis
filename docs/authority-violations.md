# AUTH-11: Authority Inversion Detection

## 1. Definition

**Authority inversion** occurs when a derived or secondary system influences the canonical authority without using an approved write path.

In AXIS, the canonical authorities are:

- **Reservation authority**: `internal/reservation/ledger.go` / `~/.axis/ledger.json`
- **Snapshot authority**: `internal/snapshot/` (built from live probes)
- **Freshness authority**: `internal/daemon/daemon.go` (wall-clock + discovery window)
- **State authority**: `internal/state/state.go` / `~/.axis/state.json` (placement memory, failures, observations)

Inversion happens when a mirror, cache, test, CLI heuristic, or observability surface mutates canonical state, backfills canonical fields from derived sources, or reclaims resources outside the ledger.

---

## 2. Prohibited Patterns

### 2.1 Bypass Writes

Writing reservation or state data to `~/.axis/state.json` when the canonical write path is `~/.axis/ledger.json`.

**Resolved AXIS example:**

The former `state.Load()` path performed ongoing cleanup and rewrote
`state.json`. Current `Load()` reads state and persists only a pending one-time
schema migration. Ongoing cleanup is explicit through `Maintain()`; daemon
refresh persists it through `state.Update()`.

### 2.2 Heuristic Freshness Overrides

Guessing or overriding freshness metadata instead of using the daemon's canonical evaluator.

**Resolved AXIS example:**

The former cached client backfilled `snap.Freshness` from `meta.Freshness` when the snapshot body lacked one:

```go
if snap.Freshness == nil && meta.Freshness != nil {
    freshness := *meta.Freshness
    snap.Freshness = &freshness
}
```

The snapshot body and the metadata endpoint are separate reads. Injecting metadata freshness into an older snapshot body creates a mixed-epoch view where the freshness claim is newer than the node facts it describes. The client now rejects mismatched publication IDs and returns snapshot-native freshness without this backfill.

### 2.3 Repair-on-Read Mutation

Mutating persisted state during a `Load()` call before returning it to the caller.

**Resolved AXIS example:**

Ongoing cleanup was removed from `state.Load()`. Selected CLI context reads may
maintain their returned object in memory, but they do not persist it. Daemon
refresh is the explicit persisted maintenance owner.

### 2.4 Mirror-to-Canonical Reconciliation Without Event Emission

Reconciling a mirror back into canonical form without emitting events or warnings that operators can observe.

**Resolved AXIS examples:**

1. State cleanup produces an aggregate typed receipt for each changed node
record and for expired failure-record cleanup. Daemon refresh emits those
receipts only after `state.Update()` succeeds.

```go
maintenance = state.MaintainWithReport(latest)
// after state.Update succeeds:
repairs.EmitAll(logger, maintenance.Receipts)
```

2. Ledger startup and explicit reconciliation emit one typed receipt per
reclaimed reservation after the cleaned ledger persists.

```go
reclaimed, receipts := l.reclaimInMemoryLocked()
// after writeSnapshot succeeds:
repairs.EmitAll(l.logger, receipts)
```

These receipts are advisory structured logs, not authority and not snapshot
warnings. Failed persistence emits an error and no success receipt.

### 2.5 Observability-Derived State Mutation

Using metrics, metadata, or derived summaries to trigger or influence reservation changes.

**Current AXIS example:**

`Daemon.Meta()` reads `ReservedMB` from the ledger when one is configured, but
falls back to summing `state.json` when `d.ledger == nil`. Metrics and doctor
consumers can therefore receive a mirror-derived reservation total when the
canonical authority is unavailable.

---

## 3. Current Violations Found in Code

### 3.1 `state.go` reclaiming stale reservations (should be ledger-only)

```go
internal/state/state.go:388-416   reclaimStaleReservation()
internal/state/state.go:425-510   reclaimHeartbeatStaleExecutions()
internal/state/state.go:512-606   reclaimDeadOwnerExecutions()
```

These functions independently prune reservations using rules (45 min legacy, 2 min heartbeat, PID death detection) that differ from the ledger's 2-minute heartbeat window. The state file should not reclaim reservations; the ledger owns all reclamation.

### 3.2 Repair-on-read (resolved)

`state.Load()` performs no ongoing maintenance. Normalization and stale-entry
cleanup live behind explicit `Maintain()` calls; daemon refresh owns persisted
maintenance and emits receipts after a successful transaction.

### 3.3 Direct `ReservedMB` writes outside `internal/reservation/`

**Writes to `NodeState.ReservedMB` (state authority, not ledger):**

```go
internal/state/state.go:381     ns.ReservedMB = reservedSum
internal/state/state.go:414     ns.ReservedMB = capMB
internal/state/state.go:482     ns.ReservedMB = 0
internal/state/state.go:508     ns.ReservedMB = sumExecReservations(reservations)
internal/state/state.go:578     ns.ReservedMB = 0
internal/state/state.go:604     ns.ReservedMB = sumExecReservations(reservations)
```

**Test-only writes (acceptable in tests, but documented for completeness):**

```go
internal/daemon/daemon_test.go:502     ns.ReservedMB = 512
```

**Derived-view writes (overlaying snapshot, not canonical):**

```go
internal/snapshotview/overlay.go:88     node.RAMReservedMB = reserved
internal/runtimectx/context_test.go:46     nodes[0].RAMReservedMB = 512
internal/daemon/daemon_test.go:835     snap.Nodes[0].RAMReservedMB = 2048
internal/api/server_test.go:1487     summary.TotalReservedMB += node.RAMReservedMB
internal/placement/placement_test.go:*  numerous RAMReservedMB assignments in test data
```

The overlay assignment in `snapshotview/overlay.go:88` is expected behavior for a derived read surface, but it is included here to show the complete boundary of where `RAMReservedMB` is assigned.

---

## 4. Detection Invariants (Grep Commands Operators Can Run)

```bash
# 1. Detect state-file reclamation (should not happen; ledger should own it)
grep -rn 'reclaimStaleReservation\|reclaimDeadOwnerExecutions\|reclaimHeartbeatStaleExecutions' internal/ --include='*.go' | grep -v '_test.go'

# 2. Inspect Load() for ongoing maintenance (none expected)
sed -n '/^func Load() (\*ClusterState, error) {/,/^}/p' internal/state/state.go

# 3. Detect direct ReservedMB writes outside internal/reservation/ (production)
grep -rn '\.ReservedMB\s*=' internal/ --include='*.go' | grep -v '_test.go' | grep -v 'internal/reservation/'

# 4. Detect direct RAMReservedMB writes outside internal/snapshotview/ (production)
grep -rn '\.RAMReservedMB\s*=' internal/ --include='*.go' | grep -v '_test.go' | grep -v 'internal/snapshotview/'

# 5. Detect state.Save() calls outside internal/state/ and internal/execution/
grep -rn '\.Save()' internal/ --include='*.go' | grep -v '_test.go' | grep -v 'internal/state/' | grep -v 'internal/execution/' | grep -v 'ledger\|skills\|config\|persist'

# 6. Detect freshness backfill from metadata into snapshot
grep -rn 'meta\.Freshness.*snap\.Freshness\|snap\.Freshness.*meta\.Freshness' internal/ --include='*.go' | grep -v '_test.go'

# 7. Detect ledger fallback in metadata / metrics
grep -rn 'state\.Load().*ReservedMB\|ReservedMB.*state\.Load' internal/ --include='*.go' | grep -v '_test.go'
```

---

## 5. Remediation Priority

| Priority | Violation | Location | Impact | Suggested Fix |
|----------|-----------|----------|--------|---------------|
| **Resolved** | Repair-on-read mutation | `internal/state/state.go` | Reads previously rewrote state | Ongoing cleanup moved to explicit maintenance; daemon owns persistence |
| **P0** | Dual reclamation | `internal/state/state.go:388-606` | State and ledger prune independently with different rules | Delete state-file reclamation entirely. Rely on ledger `Reclaim()` as the single source of truth |
| **Resolved** | Heuristic freshness override | `internal/daemon/client.go` | Mixed-epoch freshness backfill | Removed; the client returns snapshot-native freshness and metadata remains separately queryable |
| **P1** | Ledger fallback in metadata | `internal/daemon/daemon.go` | Metrics derive from the state mirror when the ledger is absent | Remove the `state.Load()` fallback or make unavailability explicit |
| **P2** | Test-only ReservedMB writes | `internal/daemon/daemon_test.go:502`, etc. | Tests build invalid state | Ensure test helpers use ledger APIs or document that they intentionally simulate legacy state |
| **Resolved** | Normalization during Load | `internal/state/state.go` | Exec maps were rewritten on read | Normalization now runs only through explicit maintenance |

---

## 6. Summary

- **Canonical reservation authority** is `internal/reservation/ledger.go`.
- **State file** (`~/.axis/state.json`) acts as a legacy mirror that still reclaims and normalizes reservations independently.
- **Repair-on-read** is resolved; `state.Load()` does not perform ongoing cleanup.
- **Freshness backfill** has been removed from `daemon/client.go`; snapshot freshness remains native to its publication.
- **Ledger fallback** remains in `daemon.Meta()` when no ledger is configured.
- **Remaining remediation** is the deliberate removal plan for legacy state
  reservation fields and the no-ledger compatibility path.
