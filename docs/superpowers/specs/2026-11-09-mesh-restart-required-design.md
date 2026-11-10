# Report a mesh restart-required config once, and show it in status and doctor

Date: 2026-11-09. Base: `origin/main` e49d8aba. Status: draft.

## Problem

Since #608, `watchMeshConfig` refuses to apply a `nodes.yaml` version that changes the gossip port,
the secret, mesh enablement or the local identity, and asks for a restart
(`internal/daemon/mesh_config.go:52-62`). Three gaps remain:

1. The refusal is retried and reported on every watcher tick. `applied` only advances on success
   (`mesh_config.go:92-98`), so the same rejected bytes are re-parsed and the error re-reported
   (`mesh_config.go:106-108`) every `watchConfigPollInterval` (500 ms, `daemon.go:191`) through
   `slog.Error` (`daemon.go:192-194`) until the daemon restarts. A throwaway probe measured 10
   reports in about 5 s after one gossip_port edit, roughly 7,200 per hour per daemon.
2. Nothing outside the log says a restart is pending: `daemon.Metadata` (`daemon.go:62-91`) has no
   field for it, so `axis daemon status` and `axis doctor` look healthy.
3. A successful reload logs nothing, so an operator cannot see what a config edit changed
   (`mesh_config.go:69`; `ReconcileSeeds` returns only an error, `internal/mesh/reconcile.go:12`).

## Non-goals

- No change to which settings need a restart, or to applying them live.
- No change to gossip signing, the secret's use, trust state or peer validation (the STANDING
  mesh-signing hold is unaffected). The secret is never logged or exposed.
- No change to the generic config watcher (`daemon.go:575-600`) or to the snapshot refresh logic.
- `axis daemon status` exit code and `status` value are unchanged; restart-required is a warning.

## Design

1. `applyMeshConfig` returns errors wrapping a new sentinel `errMeshRestartRequired` for the two
   restart cases (message text unchanged, so the existing `restart required` assertions hold).
2. `watchMeshConfig` remembers the fingerprint it last reported. An error is reported only when
   the fingerprint differs from the last reported one; a successful apply, or a return to the
   applied fingerprint, clears it, so returning to the same bad version later reports again. The
   watcher still retries the apply on every tick (cheap, and a retry is what lets a transient
   reconcile error recover); only the report is deduplicated. Unreadable files use the same
   fingerprint rule.
3. The daemon keeps the current restart reason in an `atomic.Pointer[string]`. It is set when the
   last evaluated readable version failed with `errMeshRestartRequired`, and cleared when a
   readable version applies, matches the applied one, or fails for another reason (a parse error
   is reported as such, not as a pending restart). `Metadata` gains
   `mesh_restart_required` (bool, omitempty) and `mesh_restart_reason` (string, omitempty).
4. `axis daemon status` adds a `mesh_restart_required` warning with the reason and the fix
   `axis daemon restart`. `axis doctor`, when the daemon is reachable, fetches metadata and adds a
   `Mesh config` warn check with the same fix.
5. `ReconcileSeeds` returns `(ReconcileCounts, error)` with `Added`, `Updated`, `Removed` counts of
   configured peers. The watcher logs `slog.Info("daemon: mesh config applied", added, updated,
   removed)` after a successful reload; the first apply at watcher start logs only if a count is
   non-zero.

## Alternatives

- Stop retrying the apply for a rejected fingerprint. Rejected: identical in effect for the
  restart case, but it would also freeze a transient `ReconcileSeeds` failure until the file
  changes.
- Rate-limit by time (one report per minute). Rejected: still unbounded over hours and hides a
  second, different bad edit inside the window.
- Expose the flag on `/mesh/peers` instead of metadata. Rejected: status and doctor already read
  metadata (`cmd/axis/daemon.go:44`), and the flag is daemon state, not peer state.

## Research

Prior art: systemd does not log a changed unit file repeatedly; it exposes the pending state as
the `NeedDaemonReload` unit property, which `systemctl status` turns into one warning
([org.freedesktop.systemd1](https://www.freedesktop.org/software/systemd/man/latest/org.freedesktop.systemd1.html)).
This design does the same: one log line per version, and a status field for the pending state.
Optimization measured: the report rate drops from about 2 per second (probe above) to 1 per
distinct rejected version; the new tests measure it.

Tests:
- Watcher: one rejected gossip_port version over at least 6 ticks yields exactly 1 report;
  writing a second different rejected version yields a second report.
- Metadata: restart flag and reason set after a gossip_port edit; cleared after reverting the file;
  not set for a parse error.
- `ReconcileSeeds` counts for add, update, remove and unchanged.
- `axis daemon status` warning and doctor check from a stubbed metadata fetch.

## Review
