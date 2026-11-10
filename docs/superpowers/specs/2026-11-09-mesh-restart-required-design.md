# Report a mesh restart-required config once, and show it in status and doctor

Date: 2026-11-09. Base: `origin/main` e49d8aba. Status: implemented in this PR; spec review 1 approved with changes, findings resolved.

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

1. `applyMeshConfig` returns errors wrapping a new sentinel `errMeshRestartRequired` only when
   mesh transport, enablement or local identity changed (message text unchanged, so the existing
   `restart required` assertions hold). A beacon/gossip port collision is checked first and is a
   plain config error without the sentinel: a daemon restarted on that config disables the mesh,
   so status and doctor must not advise a restart for it.
2. `watchMeshConfig` remembers the fingerprint it last reported. An error is reported only when
   the fingerprint differs from the last reported one; a successful apply, or a return to the
   applied fingerprint, clears it, so returning to the same bad version later reports again. The
   watcher still retries the apply on every tick, as today; only the report is deduplicated.
   Unreadable files use the same fingerprint rule. `TestMeshReloadInvalidatesSnapshotForRejectedVersion`,
   which waited for 3 reports of one unchanged version, now asserts exactly 1 report and no extra
   refresh over 3 poll intervals.
3. The daemon keeps the current restart reason in an `atomic.Pointer[string]`. It is set when the
   last evaluated readable version failed with `errMeshRestartRequired`, and cleared when a
   readable version applies, matches the applied one, or fails for another reason (a parse error
   is reported as such, not as a pending restart). `Metadata` gains
   `mesh_restart_required` (bool, omitempty) and `mesh_restart_reason` (string, omitempty).
4. `axis daemon status` adds a `mesh_restart_required` warning with the reason and the fix
   `axis daemon restart`. `axis doctor`, when the daemon is reachable, fetches metadata and adds a
   `Mesh config` warn check with the same fix. So that fix works on an unsupervised daemon,
   `daemonIsServingCurrent` and `daemonIsStartingUp` (`cmd/axis/daemon.go:579-588`) also require
   `!MeshRestartRequired`; otherwise `axis daemon restart` reports "already fresh" and does nothing.
5. `ReconcileSeeds` returns `(ReconcileCounts, error)` with `Added`, `Updated`, `Removed` counts of
   configured peers. The watcher logs `slog.Info("daemon: mesh config applied", added, updated,
   removed)` after a successful reload; the first apply at watcher start logs only if a count is
   non-zero. Prior configured peers are matched by case-insensitive name, so a changed
   `stable_id` (a new peer key) counts as one update, not an add plus a remove.

## Alternatives

- Stop retrying the apply for a rejected fingerprint. Equivalent in effect (the outcome depends
  only on the bytes and the startup settings), but it changes the watcher's control flow; keeping
  the retry and deduplicating the report is the smaller change.
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
- `ReconcileSeeds` counts for add, update, remove and unchanged, and one update for a changed
  `stable_id`.
- `axis daemon status` warning and doctor check from a stubbed metadata fetch.
- `axis daemon restart` terminates and respawns a serving daemon with the flag set.

## Review

Kind: spec
Reviewer: new session (Antigravity CLI, plan mode)
Model: Google Gemini (Gemini 4 Argon high; cross-family to the author; Claude Opus quota exhausted)
Base: e49d8aba
Head: 7de3feb2
Date: 2026-11-09
Verdict: approve-with-changes

1. Major: the printed fix `axis daemon restart` short-circuits as "already fresh" while the flag
   is set, because the freshness predicates ignore it. Fixed: Design 4; both predicates require
   `!MeshRestartRequired`; `TestDaemonRestartRestartsWhenMeshRestartRequired`.
2. Minor: deduplicating reports breaks the 3-report wait in
   `TestMeshReloadInvalidatesSnapshotForRejectedVersion`. Fixed: Design 2 says how the test now
   asserts one report and no extra refresh.
3. Minor: update counting was underspecified when `stable_id` changes the peer key. Fixed: Design 5
   matches by name; the existing reconcile test asserts one update.
