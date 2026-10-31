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

A task is eligible on the node when the RAM already reserved by AXIS tasks plus the task's request
fits in `reservable`; otherwise `axis placement explain` excludes the node with
`reservation cap exceeded: need …MB, have …MB reserved of …MB reservable`.

`system_reserve_mb` comes from the node's entry in `~/.axis/nodes.yaml`. When it is unset or 0,
AXIS keeps 1024 MB back (`models.MinimumSystemReserveMB`). The ranking metric `allocatable_ram`
also subtracts the system reserve, plus an eviction floor of 512 MB or 5 % of total RAM,
whichever is larger.

## Hold the cap out

Set the node's `system_reserve_mb` to the headroom you want for the operating system plus the
service's cap:

```yaml
nodes:
  - name: node-a
    hostname: node-a.example.com
    ssh_user: user
    # 1024 MB for the OS + a 3072 MB cache cap
    system_reserve_mb: 4096
```

On a 16384 MB node with 14000 MB free, the reservable budget drops from 14000 MB to
12288 MB, so the cache's cap is never promised to a task.

The reserve does not count the service twice. When the cache fills its cap, that memory is no
longer free, and `min` picks the lower free figure: with 10928 MB free, the budget is 10928 MB,
not 10928 MB minus the cap again.

The change takes effect at the next snapshot: `axis status --live` reads `nodes.yaml` directly, and
the daemon reloads it on every refresh, so it needs no restart.

## Enforce the cap in the service too

`system_reserve_mb` only stops AXIS from promising the memory. It does not limit the service. If
the service can exceed its configured cap, for example because a write can grow a buffer, the
reserve is wrong and the node can still run out of memory. Enforce the cap in the service, and
under systemd also with `MemoryMax=` (a hard limit) and `MemoryHigh=` (a throttling point) on its
unit, set at or a little above the cap you reserved.

## Check it

```bash
axis status --live --format json \
  | jq '.snapshot.nodes[] | {name, reserve: .resources.ram_system_reserved_mb, free: .resources.ram_free_mb}'
axis placement explain "<task intent>" --live
```

The first shows the reserve AXIS applied to each node. The second lists excluded nodes with the
reservable figure they were judged against.

## What AXIS does and does not know

AXIS knows only the number you configure. It does not observe the service, its cap or its current
use, so if the cap changes, update `nodes.yaml`. What the service stores is not cluster truth:
AXIS does not read it, and nothing in it can override `axis facts` or `axis status`.

Tests: `internal/placement/resident_service_test.go` (held-out cap, no double count, reservation
budget) and `TestDiscoverCopiesSystemReserveIntoFacts` in `internal/discovery/discovery_test.go`
(`nodes.yaml` to facts).
