# AXIS Consistency Model

Status: **Design document** — describes current behavior, not future changes.

---

## Core Invariant

AXIS is a **single-operator, local-first cluster substrate**. It does not
attempt to provide distributed consensus, linearizability, or strong
consistency across nodes.

---

## Guarantees

### What AXIS Guarantees

1. **Deterministic placement**: For a given `ClusterSnapshot` and task
   requirements, `placement.RankNodes` returns the same ordering on every
   invocation (stable sort, no randomness).

2. **Local command atomicity**: A single `axis` CLI command reads a
   consistent snapshot of `state.json` and `ledger.json` at invocation time.

3. **Reservation monotonicity**: Once a reservation is released, it will never
   reappear in the ledger without an explicit new acquisition.

### What AXIS Does NOT Guarantee

1. **Cross-node consistency**: Snapshots on different operator machines are
   independent. There is no cluster-wide clock or ordering.

2. **Freshness boundedness**: Daemon cache freshness is best-effort. A node
   may be reported as "complete" despite having failed 30 seconds ago.

3. **Read-after-write visibility**: `axis task run` may complete before the
   daemon cache reflects the new reservation.

---

## Failure Modes

### Network Partitions

When SSH to a remote node fails:
- The node is marked `unreachable` in the next snapshot.
- Existing reservations on that node are **not** automatically released.
- Operator must explicitly inspect and release if desired.

### Clock Skew

- `ClusterSnapshot.Timestamp` is wall-clock UTC from the operator machine.
- Daemon staleness uses `time.Since(d.collectedAt)`, vulnerable to NTP jumps.
- No `SnapshotEpoch` semantics exist yet (see AUTH-3).

### Daemon Restart

- In-memory cache is lost; next read triggers a full refresh.
- `ledger.json` and `state.json` are preserved on disk.
- No recovery of in-flight execution heartbeats.

---

## Operator Expectations

| Surface | Consistency Level | Use Case |
|---------|-------------------|----------|
| `axis facts` | Live probe | Authoritative for the local node only |
| `axis status` | Cached snapshot | Best-effort cluster view; check `Freshness` |
| `axis task place` | Cached snapshot + ledger | Deterministic given same inputs |
| `axis task run` | Ledger-authoritative | Reservation is authoritative; execution is advisory |

---

## Comparison

AXIS is weaker than Kubernetes etcd but stronger than ad-hoc SSH scripts:

- No leader election, no quorum, no distributed transactions.
- Placement is deterministic and reproducible from serialized state.
- Reservations are local-file authoritative, not consensus-based.
