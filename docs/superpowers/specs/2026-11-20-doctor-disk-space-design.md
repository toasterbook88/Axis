# `axis doctor`: warn when a node's root volume is nearly full

Date: 2026-11-20. Status: proposed; spec review approve-with-changes, its findings resolved below.
No code yet.

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

1. A node has disk facts when its resources report `disk_total_gb > 0`; an unreachable node, and a
   root under 1 GiB, have none. The collectors run `df -kP /` and round both fields down to whole
   GiB, so each can be up to 1 GiB below the real value.
2. A node is low when less than 10% of its root volume is free, and critically low when less than
   3% is free, judged so that rounding cannot raise a false warning: low only when
   `(free+1)*100 < 10*total`, critically low only when `(free+1)*100 < 3*total`. The real share is
   below `(free+1)/total`, so a listed node is below the threshold; a small volume just under it may
   go unlisted.
3. With no low node, the status is `pass` and the message says how many nodes were checked and
   names the nodes without disk facts, for example `10 of 11 nodes have at least 10% free on their
   root volume; no disk facts for node-c`.
4. With low nodes, the status is `warn`. The message lists each low node, lowest free share first,
   as `<node> <free> of <total> GB free (<p>%)`, adding `, critically low` below 3%, and names the
   nodes without disk facts; nodes with the same free share are listed by name. The fix line says `free space on the root volume of the listed nodes;
   axis status --format json lists each node's volumes`.
5. With no node that has disk facts, the status is `warn` with `no disk facts in the snapshot`,
   since the check could not run.

The check reads only the cached snapshot, so it reflects facts as of the last daemon refresh.
`docs/current-state.md` records it.

Tests (`cmd/axis/doctor_disk_test.go`), table-driven on `diskSpaceDoctorCheck`:

- every node at or above 10% free passes, and the message counts them;
- a node whose share is below 10% even after rounding warns, one at the edge of rounding
  (total 15, free 1, which may be 13% free) passes, and one exactly at 10% passes;
- a node below 3% after rounding is called critically low, and one at 3% is not;
- several low nodes are listed lowest free share first, and two with the same share by name;
- nodes with no resources, or a zero total, are named as having no disk facts and do not fail the
  check by themselves;
- no node with disk facts warns with "no disk facts in the snapshot";
- `runDoctor`, through the existing `fetchStatusSnapshot` seam, includes the check when the snapshot
  has nodes, omits it when the daemon is unreachable or the snapshot has no nodes, and with a node at
  2% free reports the check as `warn` while `--strict` still returns no error when nothing else
  fails.

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

Kind: spec
Reviewer: fresh-context session (Claude Code `claude -p`, new session on another machine, given only `docs/superpowers/review-prompt.md` and its five inputs)
Model: Anthropic Claude (same family as the author, so a fresh top-tier session rather than a cross-family review)
Base: 79d66eb8534026af6872c121da5fa707e359a8a9
Head: e83d5f00af2197dc79fde025b7a51211c91ed6c0
Date: 2026-11-20
Verdict: approve-with-changes

1. Minor: the "exact" comparison runs on values the collectors already rounded down to whole GiB (`df -kP /` integer-divided), so a small root volume could be listed as low or critically low while above the threshold. Fixed in f84a0bc2: Design steps 1 and 2 state the rounding and judge a node low only when `(free+1)*100 < 10*total` (critically low with 3), which no rounding can make false; the test list adds the edge case total 15, free 1.

2. Minor: the test list missed the reachable 0-node snapshot, where the check must be omitted, and that a warning leaves `--strict` returning no error. Fixed in f84a0bc2: both are in the `runDoctor` test item.

Questions:

- How are low nodes with the same free share ordered? Became part of the design: Design step 4 orders them by name, and the test list covers two equal shares.
- Do the cited thresholds match upstream? Verified: on 2026-11-20 the Kubernetes node-pressure eviction page listed `nodefs.available<10%` among the kubelet's default hard eviction thresholds, and node_exporter's `docs/node-mixin/config.libsonnet` on master set `fsSpaceAvailableWarningThreshold: 5` and `fsSpaceAvailableCriticalThreshold: 3`.
- Does the live measurement hold, including under the rounding rule? Verified: `axis status --format json` on 2026-11-20 gave two root volumes at 5 of 228 GB and 11 of 460 GB free, both critically low under the rounding-safe rule (600 < 684 and 1200 < 1380), one at 52 of 453 GB that is not low (5300 is not below 4530), and one node with no disk facts.
