# Decision: `/v2/reservations` Endpoint Fate

**Date:** 2026-05-17  
**Scope:** `internal/api/v2.go`, `cmd/axis/serve.go`, `internal/api/server_test.go`, `cmd/axis/dashboard.go`  
**Decision:** STABILIZE (read-only); remove the unimplemented POST contract

## 1. Current State

The `/v2/reservations` endpoint is registered in `internal/api/v2.go` (line 25) and handled by `v2Handlers.handleReservations` (lines 186–206).

- **`GET /v2/reservations`** — **Fully implemented and functional.** Returns a JSON object containing:
  - `cluster`: per-node and cluster-wide reservation summaries from the ledger (`ledger.Summary()`)
  - `reservations`: full list of ledger entries (`ledger.Entries()`), including ID, node, RAM, owner surface, heartbeat timestamps, and provenance
  - Requires a valid daemon cache and ledger; returns `503` if either is unavailable

- **`POST /v2/reservations`** — **Scaffolded but returns `501 Not Implemented`.** The handler emits the error message `"manual reservation creation pending ledger integration"`. No write path exists.

The endpoint is consumed by the `axis reservations` CLI command (`cmd/axis/dashboard.go`, lines 379–443), which performs an authenticated `GET` against the daemon and renders a terminal table of active reservations.

It is also documented in:
- `docs/reservations.md` (lines 26, 73, 145)
- `examples/reservations.md` (line 77)
- `docs/current-state.md` (line 133)

## 2. Usage by CLI / External Consumers

- **CLI consumer:** `axis reservations` depends on `GET /v2/reservations`. Removing the endpoint would break this command.
- **External consumers:** No known external consumers beyond the CLI. The endpoint is discoverable via `axis serve` and documented in operator-facing docs.
- **Alternative data path:** `GET /snapshot` returns the full cluster snapshot with reservation overlay, but does *not* expose the full ledger entry list (IDs, owners, heartbeats, provenance). `/v2/reservations` is the only HTTP surface that exposes this granularity.

## 3. Arguments for Keeping / Stabilizing

1. **Active CLI dependency.** `axis reservations` is a stable CLI command (registered in `main.go`, line 69). Removing the endpoint would require either removing the CLI command or rewiring it to parse `/snapshot`, which would lose ledger-granular fields.
2. **Focused, lightweight query surface.** `/snapshot` is heavy (full cluster state). `/v2/reservations` is a narrow, fast query for reservation bookkeeping. This separation of concerns is healthy.
3. **Ledger is canonical.** The endpoint reads from `internal/reservation/ledger.go`, which is the single source of truth for reservations. It is truth-backed and read-only.
4. **Already tested.** `server_test.go` validates the GET success path (`TestV2EndpointsReturnSuccess`, line 483) and the POST 501 path (`TestV2EndpointsReturnErrors`, line 392).

## 4. Arguments for Removing / Merging into v1

1. **v2 namespace has scaffolding.** Several `/v2/*` routes (`/v2/placement/dry-run`, `/v2/history`, `/v2/batch/place`) return 501. Maintaining a whole v2 namespace for one stable endpoint creates API-version confusion.
2. **POST is unimplemented.** A REST endpoint that advertises POST but returns 501 violates the principle of least surprise. Operators may attempt to create reservations via HTTP and hit a wall.
3. **Could merge into `/snapshot`.** The reservation entry list could be added as an optional query parameter on `/snapshot` (e.g., `?include=reservations`), collapsing the v2 surface.

## 5. Decision

**STABILIZE the GET endpoint as a read-only reservation query surface.**

Specifically:
- **Keep `GET /v2/reservations`.** It serves a real operator need and has a live CLI consumer.
- **Change `POST /v2/reservations` to return `405 Method Not Allowed`.** The endpoint is read-only; a 501 implies future implementation, but manual reservation creation is not on the near-term roadmap (see `docs/reservations.md`, section 10). Returning 405 makes the read-only contract explicit.
- **Do not merge into v1.** `/snapshot` is already a large payload. Adding the full ledger entry list would bloat it. A dedicated endpoint for reservation bookkeeping is cleaner.

## 6. Rationale

The endpoint is not scaffolding—it is a production read surface with a proven consumer. The v2 namespace's *other* scaffolded routes are a separate concern; they should be addressed independently (e.g., by hiding them behind an experimental flag or removing them). Judging `/v2/reservations` by the unimplemented neighbors is guilt by association.

The POST returning 501 is the real problem. Changing it to 405 removes the false promise of future write support and makes the API contract honest. When and if manual reservation creation is needed, a new design doc can propose a proper `POST` or `PUT` shape.

Retaining the endpoint strengthens the fact plane: operators can query reservation truth directly via HTTP or CLI without parsing a full cluster snapshot.
