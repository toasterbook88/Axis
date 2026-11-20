# `axis doctor`: warn when a node's root volume is nearly full

Date: 2026-11-20. Status: proposed; spec review pending. No code yet.

## Problem

AXIS collects each node's root-volume size and free space (`resources.disk_total_gb` and
`resources.disk_free_gb` in the snapshot), and `axis doctor` already loads the daemon's snapshot for
its "Daemon Cache" check, but no check reads those fields. On 2026-11-20 a live cluster of eleven
nodes had two root volumes at 98% used (5 GB and 11 GB free) while `axis doctor` reported "Core checks
passed" with no warning. Over the previous week the operator's own notes record root volumes at 99%,
97% and 93% found by separate agent sweeps, not by AXIS. A machine whose root volume fills up stops
writing logs, state and caches, and its builds and services fail in ways that do not name the disk.

## Non-goals

- No new probe, SSH call or network request: the check reads the snapshot doctor already fetched.
- No check of other volumes (external drives, network mounts, archives) and no inode check; only
  the root volume the snapshot reports as `disk_total_gb`.
- No failure status and no change to `--strict`: the check warns, like the other advisory checks,
  and never changes doctor's exit code.
- No trend or fill-rate prediction, no configurable threshold, no cleanup or repair action.
- No change to `axis status`, the snapshot schema or the facts collectors.

## Design

In `runDoctor` (`cmd/axis/doctor.go`), after the "Daemon Cache" check and only when the snapshot was
fetched with at least one node, doctor adds one "Disk space" check built by a new function
`diskSpaceDoctorCheck(nodes []models.NodeFacts) DoctorCheck`:

1. A node has disk facts when its resources report `disk_total_gb > 0`. For each such node the free
   share is `disk_free_gb / disk_total_gb`.
2. A node is low when less than 10% of its root volume is free, and critically low when less than
   3% is free. The comparisons are exact (`free*100 < 10*total`), not on a rounded percentage.
3. With no low node, the status is `pass` and the message says how many nodes were checked and
   names the nodes without disk facts, for example `10 of 11 nodes have at least 10% free on their
   root volume; no disk facts for node-c`.
4. With low nodes, the status is `warn`. The message lists each low node, lowest free share first,
   as `<node> <free> of <total> GB free (<p>%)`, adding `, critically low` below 3%, and names the
   nodes without disk facts. The fix line says `free space on the root volume of the listed nodes;
   axis status --format json lists each node's volumes`.
5. With no node that has disk facts, the status is `warn` with `no disk facts in the snapshot`,
   since the check could not run.

The check reads only the cached snapshot, so it reflects facts as of the last daemon refresh.
`docs/current-state.md` records it.

Tests (`cmd/axis/doctor_disk_test.go`), table-driven on `diskSpaceDoctorCheck`:

- every node at or above 10% free passes, and the message counts them;
- a node just below 10% warns and one exactly at 10% passes;
- a node below 3% is called critically low, and one exactly at 3% is not;
- several low nodes are listed lowest free share first;
- nodes with no resources, or a zero total, are named as having no disk facts and do not fail the
  check by themselves;
- no node with disk facts warns with "no disk facts in the snapshot";
- `runDoctor` includes the check when the daemon snapshot has nodes and omits it when the daemon is
  unreachable (through the existing daemon fetch seam).

## Alternatives

- **One check line per node.** Clearer per machine, but it adds a line for every node on every run
  when most are fine; doctor already prints per-node SSH lines. One summary line keeps the report
  short and still names every low node.
- **Fail below 3%.** A nearly full root volume is a real fault, but a failure changes doctor's exit
  code, and keeping this a warning leaves that exit code meaning what it means today. A later change
  can make it count under `--strict`.
- **An absolute floor (for example 10 GB).** Simple for small disks but wrong for large ones; the
  prior art below uses a share of the volume.
- **Leave it to agent sweeps.** That is the status quo, and the sweeps found these disks; AXIS
  already has the facts and is the cluster's health view.

## Research

- Kubernetes' kubelet evicts pods by default when `nodefs.available<10%`
  ([node-pressure eviction](https://kubernetes.io/docs/concepts/scheduling-eviction/node-pressure-eviction/)),
  the threshold at which a node's own filesystem is treated as under pressure. This check uses the
  same 10% for its warning.
- The Prometheus node_exporter mixin alerts `NodeFilesystemAlmostOutOfSpace` below 5% available
  (warning) and 3% (critical) (`fsSpaceAvailableWarningThreshold: 5`,
  `fsSpaceAvailableCriticalThreshold: 3` in
  [`docs/node-mixin/config.libsonnet`](https://github.com/prometheus/node_exporter/blob/master/docs/node-mixin/config.libsonnet)).
  This check calls a volume below 3% "critically low".
- Measured on the live cluster on 2026-11-20: of eleven nodes, two root volumes had 2% free, one
  had 11%, the rest more than 15%, and one node's snapshot had no disk facts. Under this design
  doctor would warn about the two, call both critically low, and name the node without facts.
- Optimization: none needed; the check iterates the snapshot already in memory.

## Review
