# Maintenance Receipts

Status: implemented for persisted state maintenance and ledger reconciliation.

## Decision

Automatic cleanup emits a typed `repairs.RepairEvent` as a structured
`maintenance receipt` log record only after the cleaned authority file is
persisted successfully.

- State cleanup aggregates changes into one receipt per node record, plus an
  aggregate receipt for expired failure records.
- Ledger startup reconciliation and explicit reclaim emit one receipt per
  removed reservation.
- Failed persistence produces an error log and no success receipt.
- In-memory-only maintenance previews emit nothing.

Receipts are advisory observability. They are not persisted separately, read
back, replayed, or used by placement, reservation, or reconciliation logic.
This preserves the authority boundary while making automatic deletion visible
to operators through normal structured service logs.
