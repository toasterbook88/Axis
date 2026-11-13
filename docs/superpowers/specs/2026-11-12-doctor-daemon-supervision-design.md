# Warn when a running daemon has no service to restart it

Date: 2026-11-12. Base: `origin/main` dec1d544 (rebased from f8c258d). Status: implemented in this
draft PR. Spec review recorded below; a diff review is still required before merge.

## Problem

`axis doctor` reports whether the AXIS-managed user service starts at boot
(`daemonBootStartCheck`, `cmd/axis/daemon_boot_start.go:21-36`), but that check returns nil when
no AXIS-managed service file exists (`:33-36`). A daemon started by hand (`axis daemon start` from
a shell, `nohup`, tmux, a terminal multiplexer pane) therefore gets no warning: the ownership checks
report it as one healthy daemon (`cmd/axis/doctor_ownership.go:369`, "one daemon serving ...").
Nothing restarts that daemon after a crash, and nothing starts it after a reboot. This was the open
part of backlog item (c), "reboot survival for daemons run outside systemd/launchd"; #626 covered
the installed-service half.

The kernel already records who started each process. On a systemd host, `/proc/<pid>/cgroup` holds
the unit: measured read-only on one private-fleet Linux host (2026-11-12), the supervised daemon's
entry is `0::/user.slice/user-1000.slice/user@1000.service/app.slice/axis.service`, while other
axis processes started from a terminal multiplexer sit in that multiplexer's own service
(`.../app.slice/<multiplexer>.service`) or in an editor's `.scope`.

## Non-goals

- Never changing the system: no stop, kill, install or restart. The check only reads procfs.
- macOS: process enumeration is Linux-only today (`listDaemonProcs`, `doctor_ownership.go:84-87`),
  so there is no launchd equivalent here.
- Restart-policy inspection (`Restart=`), and whether the unit itself starts at boot: the
  boot-start check covers the AXIS-managed unit.
- No new command, flag, config key or schema. One added doctor check (output only).

## Design

- `doctorDaemonProc` gains `Cgroup string`. `listDaemonProcs` reads `/proc/<pid>/cgroup` and keeps
  the systemd path (`parseSystemdCgroup`): the cgroup v2 `0::` entry, else the cgroup v1
  `name=systemd` entry, else "".
- `daemonServiceUnit(cgroup)` returns the cgroup leaf when it ends in `.service`, else "".
- `daemonSupervisionCheck(owners) (DoctorCheck, bool)`, pure, in a new file
  `cmd/axis/doctor_supervision.go`, called from `daemonOwnershipChecks` next to
  `daemonExecutableCheck` for the daemons serving this address:
  - skip owners whose cgroup is "" (unreadable) or "/" (container cgroup namespace hides the host
    unit); with none left, no check is added;
  - leaf is a service whose name contains `axis` (case-insensitive): supervised;
  - leaf is another service (for example `ssh.service`, a multiplexer's service): not supervised,
    reported as "inside <unit>", because the daemon stops with that service and is not restarted
    on its own;
  - leaf is not a service (`session-N.scope`, `init.scope`, any scope): not supervised;
  - any unsupervised owner: `warn` "Daemon supervision", message lists `pid N (<leaf>)`, fix
    "stop that daemon, then run axis daemon service install"; otherwise `pass` listing the units.
- Docs: `docs/daemon-service.md` (paragraph after the boot-start table), `docs/current-state.md`
  (doctor ownership line and the `axis doctor` table row), CHANGELOG (Unreleased, Features).

## Alternatives

- Parent PID is 1 or `systemd --user`. Rejected: a `nohup`ed daemon whose shell exited is
  reparented to PID 1 or the user manager (subreaper) and would look supervised.
- Ask systemd (`GetUnitByControlGroup`, `systemctl status <pid>`). Rejected: a bus dependency.
  The user manager cannot see a system unit's cgroup, and the system manager cannot see a user
  unit. One procfs read covers both, and it still works when the bus is down. Research records
  why the leaf parse stays despite the mapping not being a public API.
- Treat any `.service` leaf as supervised. Rejected by the measurement above: axis processes
  started from a multiplexer live in the multiplexer's service and would pass.
- Match only the AXIS-managed unit name `axis.service`. Rejected: system units and distribution
  modules may use another name (`axis-daemon.service`); requiring `axis` in the name keeps them.

## Research

- cgroup v2 `/proc/<pid>/cgroup` format (`0::<path>`) and v1 named hierarchies:
  [cgroups(7)](https://man7.org/linux/man-pages/man7/cgroups.7.html).
- systemd places each service in its own cgroup named after the unit, and login sessions in
  `session-N.scope`: [systemd.unit(5)](https://www.freedesktop.org/software/systemd/man/latest/systemd.unit.html),
  [systemd.scope(5)](https://www.freedesktop.org/software/systemd/man/latest/systemd.scope.html),
  [Control Group APIs and Delegation](https://systemd.io/CGROUP_DELEGATION/).
  That page also says the slice/scope/service-to-cgroup mapping is not a public API and may
  change, and that programs should call `GetUnitByControlGroup()` instead of translating the
  path locally. This check still reads the leaf. Measured 2026-11-13 on systemd 255 with the
  unified hierarchy: the managed user unit has `Delegate=no`, its cgroup leaf is the unit name,
  and `GetUnitByControlGroup` on the user bus returns that unit. The same call on the user bus
  rejects a system-unit path (`systemd-journald.service` is not managed by that instance); the
  system bus accepts it. A bus lookup would need both managers and would fail when the bus is
  down. The leaf is the unit name in the layout that page documents (`.../quux.service/`). A
  later layout that nests the main PID under the unit would mislabel supervision. That limit is
  accepted because the non-goal is no bus dependency.
No optimization applies: one small procfs read per axis process doctor already enumerates.

Tests (`cmd/axis/doctor_supervision_test.go`): parse cgroup v2, v1, hybrid (v2 wins), container
`/`, no systemd hierarchy, empty; unit leaf for user service, system service, session scope,
`init.scope`, `/`, ""; check results: user service pass, system `axis-daemon.service` pass, login
session warn, `ssh.service` warn "inside", `AXIS.service` pass (case-insensitive), one of two
unsupervised warn, `/` and unread skip, no owners skip; through `daemonOwnershipChecks`:
unsupervised daemon warns (regression), no cgroup adds no check.

## Review

None yet. A fresh-context spec review and a diff review are required (AGENTS.md §8) before merge.
