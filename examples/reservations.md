# AXIS Reservation Workflows

AXIS tracks resource reservations in two places:

- **`~/.axis/ledger.json`** — canonical reservation ledger (authoritative)
- **`~/.axis/state.json`** — legacy mirror with placement history and failure records

The snapshot overlay prefers the ledger and falls back to state when the
ledger has no entry for a node.

## How reservations are created

Reservations are created automatically by the guarded execution path
(`axis task run`). Before a task starts:

1. AXIS selects the best node via placement.
2. AXIS **reserves** RAM on that node in the ledger.
3. AXIS sends a **heartbeat** every few seconds while the task runs.
4. When the task finishes (success or failure), AXIS **releases** the reservation.

You do not manually create reservations for normal task execution.

## Inspect current reservations

The easiest way to see reservations is through the cluster snapshot:

```bash
axis status
```

Look for `RAMReservedMB` on each node. The allocatable RAM shown already
subtracts reserved amounts.

For a machine-readable view:

```bash
axis status --format json | jq '.nodes[] | {name, ram_reserved_mb}'
```

## Reclaim stale reservations

If a task crashes without releasing its reservation, the ledger reclaims
stale entries automatically:

- On **daemon startup**, the ledger drops entries whose last heartbeat is
  older than the heartbeat stale window (default 2 minutes).
- The state file also runs maintenance on every load, pruning heartbeat-stale
  executions and dead owner processes.

You can force a full refresh (which reloads the ledger and rebuilds the
snapshot) without restarting the daemon:

```bash
axis daemon refresh
```

## Clear all placement memory

To wipe the legacy state file (decisions, observations, and reservations):

```bash
axis context clear
```

This removes `~/.axis/state.json`. It does **not** affect the canonical
ledger (`~/.axis/ledger.json`), which is only mutated through reserve and
release operations.

## Daemon reservation surfaces

When the HTTP API is running (`axis serve`), the following endpoints expose
reservation state:

| Endpoint | Purpose |
|----------|---------|
| `GET /snapshot` | Full cluster snapshot with reservation overlay |
| `GET /v2/reservations` | Reservation summary per node |

## Reservations and cache staleness

Because the snapshot is built from live node probes plus the ledger overlay,
a cached snapshot may show reservations that were already released, or miss
new reservations made after the last refresh. The daemon metadata includes a
`Stale` flag when the snapshot is older than the staleness threshold
(default 5 minutes).

If you see unexpected reservation numbers, refresh the cache:

```bash
axis daemon refresh
axis status --cached
```

## Design notes

- The ledger is the single source of truth for "what is reserved where."
- Dual reclamation (ledger + state) exists for backward compatibility.
- There is no file watcher on `ledger.json`; other processes that modify it
  will not be detected until the daemon restarts or refreshes.
