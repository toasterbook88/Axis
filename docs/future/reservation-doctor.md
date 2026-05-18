# Future: `axis reservation doctor`

Status: **Design sketch** — not scheduled for implementation.

---

## Purpose

A focused diagnostic command that inspects the reservation authority
(`~/.axis/ledger.json`) and its mirrors for anomalies that the generic
`axis doctor` does not cover.

---

## What It Would Diagnose

| Check | Severity | Description |
|-------|----------|-------------|
| Stale reservations | warning | Heartbeat older than 2 min but not yet reclaimed |
| Orphaned reservations | error | Reservation exists but no matching execution process |
| Ledger/state drift | warning | `ledger.json` and `state.json` report different `ReservedMB` for same node |
| Reservation leak | error | Total reserved RAM > observed node RAM |
| Owner mismatch | warning | Reservation owner PID does not match running process |
| Tombstone accumulation | info | Large number of expired reservations in ledger history |

---

## How It Would Report

Uses the `internal/repairs/` event types already defined:

```go
RepairEvent{
    Severity:     SeverityWarning,
    Source:       "reservation-doctor",
    ObjectType:   "Reservation",
    ObjectID:     "<reservation-id>",
    OldValue:     "active, heartbeat 5m ago",
    NewValue:     "reclaimed",
    Reason:       "heartbeat stale beyond threshold",
}
```

Reports are printed as structured JSON or human-readable warnings, never
mutating state unless `--fix` is explicitly passed.

---

## Suggested Actions

| Finding | Suggested action |
|---------|-----------------|
| Stale heartbeat | `axis reservations release <id>` |
| Orphaned | `axis reservations release <id> --force` |
| Ledger/state drift | Manual inspection; do not auto-reconcile |
| Reservation leak | `axis doctor` + manual node restart |

---

## Difference from `axis doctor`

| | `axis doctor` | `axis reservation doctor` |
|---|---------------|---------------------------|
| Scope | Config, SSH, daemon health | Reservation ledger integrity |
| Mutates state | No | No (unless `--fix`) |
| Output | Warnings + exit code | Repair events + suggested actions |
| Authority | Config + transport | Ledger + execution observation |

---

## Open Questions

1. Should `--fix` automatically release stale reservations or only emit events?
2. Should the command require daemon to be running for live process validation?
3. Should findings be persisted to `skills.json` for pattern learning?
