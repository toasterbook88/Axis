# AXIS Development Process

> Base PR guidance (scope, tests, formatting) lives in [CONTRIBUTING.md](../CONTRIBUTING.md).
> This document extends that guidance for substrate-first architectural changes.

## PR Types

### Fact Plane
Must include evidence and degraded-state impact.

### Placement
Must include before/after reasoning and score deltas.

### Surface / Adapter
Must prove it consumes existing contracts (see [current state](./current-state.md) for canonical contracts).

## Proof Block (Required in PR description)
- What changed
- What invariant is preserved
- Evidence (tests / output)
- Drift impact (release vs main)

## Merge Gate
No feature may widen divergence from the Single Placement Contract.

## Principle
Substrate first. Surfaces second.
