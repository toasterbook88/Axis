# AXIS Development Process

## PR Types

### Truth Plane
Must include evidence and degraded-state impact.

### Placement
Must include before/after reasoning and score deltas.

### Surface / Adapter
Must prove it consumes existing contracts.

## Proof Block (Required in PRs)
- What changed
- What invariant is preserved
- Evidence (tests / output)
- Drift impact (release vs main)

## Merge Gate
No feature may widen divergence from the placement contract.

## Principle
Substrate first. Surfaces second.
