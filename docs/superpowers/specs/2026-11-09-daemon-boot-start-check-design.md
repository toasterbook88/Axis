# Warn when the AXIS user service will not start at boot

Date: 2026-11-09. Base: `origin/main` e49d8aba. Status: draft.

## Problem

`axis daemon service install` installs a per-user service and reports success
(`cmd/axis/daemon_service.go:97-114`), but neither it nor `axis doctor` checks whether that
service starts when the machine boots with nobody logged in:

- Linux: the unit is a systemd **user** unit with `WantedBy=default.target`
  (`daemon_service.go:326-346`), enabled with `systemctl --user enable` (`:218-227`). A user
  manager runs at boot only when lingering is enabled for the user (`Linger=yes`); otherwise the
  daemon starts at the first login. `rg -i linger cmd internal docs` at Base: 0 hits.
- macOS: the plist is a LaunchAgent in `~/Library/LaunchAgents` bootstrapped into `gui/<uid>`
  (`daemon_service.go:131-132`, `:203-213`). launchd loads it only in a GUI login session
  (`launchctl print` reports `LimitLoadToSessionType = Aqua` for it), so it starts at boot only if
  the Mac logs a user in automatically.

Field data (one private fleet, measured read-only 2026-11-09): every Linux host had
`Linger=yes`, so they restart unattended; two of three Macs had no auto-login and start AXIS only
after someone logs in. Nothing in AXIS tells an operator which case a host is in.

## Non-goals

- Never changing the system: no `loginctl enable-linger`, no auto-login change, no LaunchDaemon
  install. The checks only read state and print the command or option.
- No change to the unit or plist templates, or to install/uninstall behavior and exit codes.
- No check on Linux hosts that run AXIS outside the user service (system units already have
  their own doctor check, `doctor_ownership.go:254-269`).

## Design

One function, `daemonBootStartCheck(ctx, deps daemonServiceDependencies) *DoctorCheck`, used by
both install output and doctor. It returns nil (no check) unless the AXIS-managed service file
exists at `daemonServicePath` and contains `daemonServiceMarker`. All commands run through the
existing `deps.run` seam, each with a short timeout.

- Linux: `systemctl --user is-enabled axis.service`. If not `enabled`, nil (nothing to start).
  Then `loginctl show-user <uid> --property=Linger --value`.
  - `yes`: pass, "user service starts at boot (lingering on)".
  - `no`: warn, "user service starts at login only (lingering off)", fix
    `loginctl enable-linger $USER`.
  - error or other output: warn, "could not read lingering state", with the probe error.
- macOS: `defaults read /Library/Preferences/com.apple.loginwindow autoLoginUser` (readable
  without root).
  - a user name: pass, "LaunchAgent starts at boot via automatic login".
  - error (key absent): warn, "LaunchAgent starts at login only (no automatic login)", fix text
    naming the two options (enable automatic login, or run the daemon as a LaunchDaemon) without a
    command, since both need an administrator.
- Doctor adds the check after the daemon ownership checks through a seam
  `doctorProbeBootStart`. `daemon service install` prints `Warning: <message>` and `Fix: <fix>`
  after its success line when the check warns; it never fails the install.

## Alternatives

- Run `loginctl enable-linger` during install. Rejected: it changes system state and may need
  polkit or root; the operator decides.
- Install a system unit or LaunchDaemon instead. Rejected here: it needs root and changes the
  supervision model the user service was chosen for.
- Check `/var/lib/systemd/linger/<user>` directly. Rejected: an implementation detail;
  `loginctl show-user` is the documented interface.

## Research

- systemd user managers start at boot only with lingering:
  [loginctl(1) enable-linger](https://www.freedesktop.org/software/systemd/man/latest/loginctl.html)
  and [user@.service(5)](https://www.freedesktop.org/software/systemd/man/latest/user@.service.html).
- launchd LaunchAgents run in the user's session; LaunchDaemons run at boot:
  [Apple, Creating Launch Daemons and Agents](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html).
No optimization applies; at most two short read-only commands per run.

Tests (stubbed `deps.run`, temp HOME): Linux enabled+`yes` pass; enabled+`no` warn with the
linger command; `is-enabled` disabled gives nil; loginctl error warns; no service file gives nil
and runs no command; macOS auto-login set passes, key absent warns; install output contains the
warning on Linux `no` and not on `yes`; doctor wiring through the seam.

## Review
