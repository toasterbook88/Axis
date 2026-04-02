# AXIS Invariants

This document defines **non-negotiable system invariants**.

## Truth Boundary
Only `axis facts`, `axis status`, `axis task place`, and `axis task context` may emit authoritative truth.

## Single Placement Contract
All surfaces must use the same decision model.

## Cache Explicitness
No silent fallback from live to cached.

## Execution Subordination
Execution must consume placement output.

## Node Identity Stability
Identity must survive hostname/IP changes.

## Failure Memory Separation
Tombstones influence placement but are not truth.

## Single Source Per Concern
facts, placement, execution must not duplicate logic.

## Security Invariants
No untrusted execution, no secret leakage, no SSH bypass.
