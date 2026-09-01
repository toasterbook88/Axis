# Snapshot Publication Envelope

Status: implemented for live runtime and daemon snapshot assembly, including
daemon metadata binding.

## Decision

Every newly assembled `ClusterSnapshot` carries a `publication` object. The
object records which independently timed inputs produced that view:

- a unique `pub-*` publication ID;
- assembly source (`live-runtime` or `daemon-cache`) and UTC assembly time;
- cache age in seconds (zero at assembly, projected when the daemon serves a
  clone);
- facts observation time plus a SHA-256 discovery digest over the pre-overlay
  node facts and typed discovery freshness;
- a SHA-256 digest and entry count for the active reservation ledger;
- a SHA-256 digest, schema version, and `updated_at` value for `state.json`;
- explicit availability and warning fields for component authority.

Digests use the `sha256:<hex>` form over deterministic JSON. They identify the
component content used by one assembly; they are not signatures and do not
authenticate the source.

## Coherence boundary

The envelope makes mixed input epochs visible. It does not claim that fact
collection, ledger load, and state load happened in one cross-store
transaction.

The reservation entry set is frozen once per assembly. AXIS fingerprints that
slice and applies the reservation overlay from the same slice, so the ledger
digest and rendered allocatable capacity cannot describe different ledger
reads. State and facts are also local immutable inputs after they are captured
for the assembly.

Daemon content-change debounce ignores the publication ID, assembly time, facts
observation time, and read-time cache age. It retains component digests as
semantic content, so a changed authority input can still notify snapshot
subscribers.

## Metadata binding

`/snapshot` and `/snapshot/meta` remain separate reads. Daemon metadata captures
the current snapshot publication ID and its discovery freshness in one atomic
metadata state. The cached client requires both responses to carry the same
non-empty publication ID before it applies staleness warnings or freshness.

If a refresh lands between the metadata and snapshot requests, the IDs differ
and the read fails with an explicit retry error. A missing ID is treated as an
incompatible daemon/payload rather than silently accepting an unbound view.

The client still backfills missing discovery freshness from metadata after the
publication IDs match. Removing that compatibility behavior is the next
authority-coherence step.
