# AXIS RAM Balancing Research

Last reviewed: 2026-03-19

## Question

What is the proper way for AXIS to treat RAM balancing across cluster nodes if
the intent is for the cluster to share and balance memory pressure?

## Short Answer

For AXIS, the proper model is:

- treat RAM as a shared cluster scheduling resource
- use per-node allocatable memory, not raw total memory
- reserve memory for the OS and safety buffers first
- place work using a soft request plus an optional burst ceiling
- use live pressure signals to avoid overloaded nodes
- rebalance using leases, expiry, and pressure-aware de-preference

This is how mature schedulers approach memory sharing across nodes.

It is not the same thing as literal distributed shared memory or remote memory
pooling.

## Important Distinction

There are two very different meanings of "nodes share RAM":

### 1. Shared Cluster RAM via Scheduling

This is the practical and common interpretation.

Each node keeps its own physical memory, but the scheduler treats cluster memory
as a pooled capacity and balances work so one node does not thrash while others
sit idle.

This is how systems like Kubernetes, Nomad, and Mesos think about memory.

### 2. Literal Remote Shared Memory

This is memory disaggregation or far-memory architecture.

In that model, one node can directly access memory located on another machine or
on a remote memory pool. That requires a very different stack: RDMA, CXL, remote
page handling, coherence or indirection strategy, failure handling, and careful
latency management.

That is not what AXIS currently implements.

## What Mature Systems Actually Do

### 1. Schedule Against Requests, Not Just Live Free RAM

Kubernetes uses memory requests mainly for scheduling and does not consider
usage above the request when deciding if another workload can fit on a node.

Source:

- [Kubernetes: Resource Management for Pods and Containers](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/)

Implication for AXIS:

- every task should have a memory request used for placement
- raw `RAMFreeMB` should not be the only placement signal

### 2. Reserve Memory for the System Before Offering It to Workloads

Kubernetes explicitly models `kubeReserved`, `systemReserved`, and node
allocatable memory so workloads do not consume memory needed by the OS or node
services.

Sources:

- [Kubernetes: Reserve Compute Resources for System Daemons](https://kubernetes.io/docs/tasks/administer-cluster/reserve-compute-resources/)
- [Kubernetes: Node-pressure Eviction](https://kubernetes.io/docs/concepts/scheduling-eviction/node-pressure-eviction/)

Implication for AXIS:

- node capacity should be split into:
  - total memory
  - system reserve
  - eviction reserve / safety floor
  - allocatable memory for cluster work

### 3. Use Pressure-Aware Signals, Not Just "free -m"

Kubernetes documents `memory.available` semantics and also exposes PSI
(Pressure Stall Information) to identify real memory pressure. PSI distinguishes
between early contention (`some`) and severe stalls (`full`).

Sources:

- [Kubernetes: Node-pressure Eviction](https://kubernetes.io/docs/concepts/scheduling-eviction/node-pressure-eviction/)
- [Kubernetes: Understand PSI Metrics](https://kubernetes.io/docs/reference/instrumentation/understand-psi-metrics/)
- [Linux kernel PSI documentation](https://www.kernel.org/doc/html/latest/accounting/psi.html)

Implication for AXIS:

- prefer pressure-aware scoring over raw free-RAM sorting
- keep `RAMFreeMB`, but add:
  - `allocatable_ram_mb`
  - `memory_available_mb`
  - `memory_psi_some_avg10`
  - `memory_psi_full_avg10`

### 4. Separate Typical Reservation from Burst Ceiling

Nomad’s memory oversubscription model uses:

- a reserve limit for typical usage, used by the scheduler
- a max limit for burst usage

If pressure rises, Nomad can push workloads back toward their reserve and
reschedule them.

Sources:

- [Nomad: Oversubscribe memory](https://developer.hashicorp.com/nomad/tutorials/advanced-scheduling/memory-oversubscription)
- [Nomad: resources block in the job specification](https://developer.hashicorp.com/nomad/docs/job-specification/resources)

Implication for AXIS:

- each task should ideally have:
  - `memory_request_mb`
  - `memory_max_mb` optional
- placement should be based on `memory_request_mb`
- `memory_max_mb` should only be allowed if AXIS has a reclaim story

### 5. Make Fairness Cluster-Level, Not Just Per-Node

The DRF paper is the classic reference for fair allocation of multiple resource
types. It is about fairness across shared cluster resources, not just picking
the emptiest machine.

Sources:

- [Dominant Resource Fairness (Berkeley technical report)](https://www2.eecs.berkeley.edu/Pubs/TechRpts/2011/EECS-2011-18.html)
- [Apache Mesos allocation modules](https://mesos.apache.org/documentation/latest/allocation-module/)

Implication for AXIS:

- if AXIS grows beyond memory-only balancing, fairness should consider both CPU
  and memory together
- memory balancing should be framed as a cluster share problem, not just a
  single-node fit problem

### 6. Encode Spreading and Skew Explicitly

Kubernetes topology spread constraints use `maxSkew` to keep workloads evenly
distributed across nodes or zones.

Source:

- [Kubernetes: Pod Topology Spread Constraints](https://kubernetes.io/docs/concepts/scheduling-eviction/topology-spread-constraints/)

Implication for AXIS:

- if the product goal is balance, AXIS should measure and reason about skew
- repeated selection of the same high-memory node should count against that node
  unless the policy is intentionally binpack-oriented

## What AXIS Should Probably Do

### Resource Model

For each node, define:

- `ram_total_mb`
- `ram_system_reserved_mb`
- `ram_eviction_reserved_mb`
- `ram_allocatable_mb = total - system_reserved - eviction_reserved`
- `ram_requested_mb = sum of active task memory requests`
- `ram_leased_mb = sum of live AXIS soft claims`
- `ram_effective_mb = allocatable - max(requested_mb, leased_mb)` or another
  clearly defined combination
- live pressure fields from PSI / `memory.available`

### Task Model

For each task, define:

- `memory_request_mb`
- `memory_max_mb` optional
- `lease_ttl`
- `heartbeat_at`

### Placement Policy

Use a two-stage decision:

1. Filter:
   - node must have `ram_effective_mb >= memory_request_mb + safety_margin`
   - node must not be in severe memory pressure
2. Score:
   - prefer lower memory pressure first
   - then prefer nodes that reduce cluster skew
   - then apply locality / tool / GPU preferences

This is closer to "cluster RAM sharing" than sorting only by raw free RAM.

### Rebalancing Policy

Use soft leases with expiry:

- placement creates a lease
- execution heartbeats refresh it
- completion releases it
- stale leases expire automatically
- nodes under pressure are temporarily penalized or marked no-schedule

### Overcommit Policy

Only allow burst-over-request behavior if AXIS also has:

- eviction thresholds
- pressure monitoring
- reclaim behavior
- operator-visible warnings

Without those, burst memory should stay conservative.

## What AXIS Should Avoid

- Treating `RAMFreeMB` as the whole truth
- Assuming "reserved RAM" alone equals proper balancing
- Repeatedly picking the same node because it has the largest instantaneous free
  value
- Claiming to support memory sharing if the system does not implement pressure,
  reclaim, and skew-aware balancing
- Confusing cluster scheduling with true remote shared memory

## Recommended Direction For AXIS

The best next step is not remote memory pooling.

The best next step is to turn AXIS into a pressure-aware, lease-based cluster
memory balancer:

1. add allocatable and reserve concepts
2. upgrade state from simple reservation bookkeeping to leases with TTL
3. incorporate pressure-aware metrics into scoring
4. add skew-aware balancing to avoid repeatedly draining the same node
5. later, if needed, evolve toward DRF-style multi-resource fairness

That would match the stated purpose of helping nodes in the cluster share and
balance RAM.
