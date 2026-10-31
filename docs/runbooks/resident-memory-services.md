# Resident memory services

A resident memory service is a long-running process on a node that holds RAM up to a cap it sets
itself: an in-memory cache, a scratch blob store, a vector index, or a model another tool keeps
warm. This runbook explains how to keep AXIS placement from offering that memory to tasks.

## The problem

Placement sees the RAM that is free when the node is probed. A service below its cap leaves part
of the cap free, and it can take that memory back at any time without telling AXIS. Unless you
tell AXIS about the cap, it can place a task in memory the service is about to use.

## How AXIS sizes a node

For each node, placement computes a reservable budget:

```text
reservable = min(ram_free_mb, ram_total_mb - system_reserve_mb)
```

`ram_total_mb - system_reserve_mb` is a ceiling on everything AXIS reserves on the node. It is not
subtracted from free RAM: when free RAM is below the ceiling, the budget is free RAM. A task is
eligible when the RAM AXIS has already reserved there plus the task's request fits in
`reservable`; otherwise `axis placement explain` excludes the node with
`reservation cap exceeded: need …MB, have …MB reserved of …MB reservable`.

Placement checks a snapshot, so two tasks placed at the same moment can both pass it. The ceiling
is checked again when a task acquires its lease: the reservation ledger refuses a lease that would
take the node's reservations over `ram_total_mb - system_reserve_mb`. It counts reservations
against that ceiling, not free RAM.

Leases are granted one at a time under a file lock shared by every AXIS process on the machine,
and each lease re-reads the ledger file under that lock, so a lease granted by the daemon, a CLI
run or the MCP tool a moment earlier counts against the ceiling.

Ranking uses the same budget minus AXIS's reservations. The `resources.ram_eviction_reserved_mb`
and `resources.ram_allocatable_mb` fields in `axis status` JSON also subtract an eviction floor of
512 MB or 5 % of total RAM; they are reported figures, and placement does not apply that floor.

`system_reserve_mb` comes from the node's entry in `~/.axis/nodes.yaml`. When it is unset or 0,
AXIS keeps 1024 MB back (`models.MinimumSystemReserveMB`). On Linux, `ram_free_mb` is
`MemAvailable`.

## Hold the cap out

Because the reserve is a ceiling on AXIS's share, it has to cover everything that is not an AXIS
task at its peak, not only operating-system headroom. Measure with no AXIS tasks running:

```text
system_reserve_mb = (ram_total_mb - ram_free_mb)   # everything in use now
                  - service_now_mb                 # the service's current size
                  + service_cap_mb                 # the size it may grow to
                  + margin_mb                      # growth of everything else
```

For a 16384 MB node with 14000 MB free while a cache with a 3072 MB cap is empty, other use is
2384 MB. With a 512 MB margin the reserve is 5968 MB:

```yaml
nodes:
  - name: node-a
    hostname: node-a.example.com
    ssh_user: user
    system_reserve_mb: 5968   # 2384 other use + 3072 cache cap + 512 margin
```

AXIS then admits at most 10416 MB of reservations on the node. With the largest task it accepts, the cache at its cap and other use unchanged, the node
holds 2384 + 10416 + 3072 = 15872 MB, under its 16384 MB.

Reserving only 1024 MB of OS headroom plus the cap (4096 MB) is not enough here: AXIS would accept
a 12288 MB task, and with the cache full the node would need 17744 MB.

Re-measure when the node's other workload changes. The margin covers its normal variation, not a
new service.

The change takes effect at the next snapshot: `axis status --live` reads `nodes.yaml` directly, and
the daemon reloads it on every refresh, so it needs no restart.

## Enforce the cap in the service too

`system_reserve_mb` only stops AXIS from promising the memory. It does not limit the service. If
the service can exceed its configured cap, for example because a write can grow a buffer, the
reserve is wrong and the node can still run out of memory. Enforce the cap in the service, and
under systemd also with `MemoryMax=` (a hard limit) and `MemoryHigh=` (a throttling point) on its
unit, set at or a little above the cap you reserved.

The reserve also bounds what AXIS reserves, not what a task uses: a task that uses more than its
request can still crowd the service.

## Check it

```bash
axis status --live --format json \
  | jq '.snapshot.nodes[] | {name, reserve: .resources.ram_system_reserved_mb, free: .resources.ram_free_mb, budget: .ram_allocatable_mb}'
axis placement explain "<task intent>" --live
```

The first shows the reserve AXIS applied to each node and the remaining budget placement ranks
on. The second lists excluded nodes with the reservable figure they were judged against.

## What AXIS does and does not know

AXIS knows only the number you configure. It does not observe the service, its cap or its current
use, so if the cap changes, update `nodes.yaml`. What the service stores is not cluster truth:
AXIS does not read it, and nothing in it can override `axis facts` or `axis status`.

Tests:
- `internal/placement/resident_service_test.go`: the largest accepted task leaves room for the cap;
  a reserve without other use overflows; the reserve is a ceiling, not a deduction; reservations
  share the ceiling.
- `TestApplyReservationEntriesRankingFigureIgnoresEvictionFloor` in
  `internal/snapshotview/overlay_test.go`: the ranking figure.
- `TestDiscoverCopiesSystemReserveIntoFacts` in `internal/discovery/discovery_test.go`: `nodes.yaml`
  to facts.
- `TestLoadLiveLedgerHonorsNodeSystemReserve`, `TestLoadCachedLedgerHonorsNodeSystemReserve` and
  `TestPopulateLedgerCapacityAppliesNodeSystemReserve`: a second 6000 MB lease over the ceiling is
  refused on the in-process and MCP ledgers, not only the daemon's.
- `internal/reservation/ledger_shared_test.go`: two ledgers on one file, standing in for two
  processes, see and keep each other's leases.
