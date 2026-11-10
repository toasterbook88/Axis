# Warn when the AXIS user service will not start at boot

Date: 2026-11-09. Base: `origin/main` e49d8aba. Status: implemented in this PR; spec review 1 approved with changes, findings resolved.

## Problem

`axis daemon service install` installs a per-user service and reports success
(`cmd/axis/daemon_service.go:97-114`), but neither it nor `axis doctor` checks whether that
service starts when the machine boots with nobody logged in:

- Linux: the unit is a systemd **user** unit with `WantedBy=default.target`
  (`daemon_service.go:326-346`), enabled with `systemctl --user enable` (`:218-227`). A user
  manager runs at boot only when lingering is enabled for the user (`Linger=yes`); otherwise the
  daemon starts at the first login. `rg -i linger cmd internal docs` at Base: 0 hits.
- macOS: the plist is a LaunchAgent in `~/Library/LaunchAgents` bootstrapped into `gui/<uid>`
  (`daemon_service.go:148-149`, `:203-213`). launchd loads it only in a GUI login session
  (`launchctl print` reports `LimitLoadToSessionType = Aqua` for it), so it starts at boot only if
  the Mac logs a user in automatically.

Field data (one private fleet, measured read-only 2026-11-09): every Linux host had
`Linger=yes`, so they restart unattended; two of three Macs had no auto-login and start AXIS only
after someone logs in. Nothing in AXIS tells an operator which case a host is in.

## Non-goals

- Never changing the system: no `loginctl enable-linger`, no auto-login change, no LaunchDaemon
  install. The checks only read state and print the command or option.
- No change to the unit or plist templates, or to install/uninstall behavior and exit codes.
- No check on Linux hosts that run AXIS outside the user service: system units start at boot
  without lingering (`doctor_ownership.go:254-269` only reports their contention with the user
  service).
- No check that the macOS agent is enabled in launchd: install always runs `launchctl enable`
  (`daemon_service.go:208-210`).

## Design

One function, `daemonBootStartCheck(ctx, deps daemonServiceDependencies) *DoctorCheck`, used by
both install output and doctor. It returns nil (no check) unless the AXIS-managed service file
exists at `daemonServicePath` and contains `daemonServiceMarker`; then it runs no command. All
commands run through the existing `deps.run` seam, each with a 5 s timeout (as
`doctor_restart_loops.go:90`); a timeout is a "could not read" warning.

- Linux: `systemctl --user is-enabled axis.service`. If not `enabled`, nil (nothing to start).
  Then `loginctl show-user <uid> --property=Linger --value`.
  - `yes`: pass, "user service starts at boot (lingering on)".
  - `no`: warn, "user service starts at login only (lingering off)", fix
    `loginctl enable-linger $USER`.
  - error or other output: warn, "could not read lingering state", with the probe error.
- macOS: `defaults read /Library/Preferences/com.apple.loginwindow autoLoginUser`.
  - the current user's name (a new `deps.userName` seam, `os/user.Current` by default): pass,
    "automatic login is set for this user; the LaunchAgent starts when it logs in at boot".
  - another name: warn, "automatic login is set for <name>, not this user".
  - an error whose output says the pair "does not exist": warn, "LaunchAgent starts at login only
    (no automatic login)", fix text naming the two options (enable automatic login for this user,
    or run the daemon as a LaunchDaemon) without a command, since both need an administrator.
  - any other error: warn, "could not read automatic-login state".
- Doctor adds the check after the daemon ownership checks through a seam
  `doctorProbeBootStart`. `daemon service install` prints `Warning: <message>` and `Fix: <fix>`
  after its success line when the check warns; it never fails the install.
- Docs: `docs/current-state.md` (the doctor line and the `axis doctor` table row) and
  `docs/daemon-service.md` (boot versus login start per OS, with the remedies).

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
linger command; `is-enabled` disabled gives nil; loginctl error and odd output (`maybe`) warn
"could not read"; a blocking `deps.run` returns within the timeout and warns; no service file and
an unmarked file give nil and run no command; macOS auto-login for this user passes, for another
user warns, key absent warns login-only, another error warns could-not-read; install output
contains the warning on Linux `no`, a loginctl error and macOS key absent, never fails, and has
none on `yes`; doctor shows the check through the seam.

## Review

Kind: spec
Reviewer: new session (Antigravity CLI, plan mode)
Model: Anthropic Claude (Opus 5.5 high; cross-family to the author)
Base: e49d8aba
Head: 66512f4e
Date: 2026-11-09
Verdict: approve-with-changes

1. Major: the macOS pass did not show that this user's agent starts: any `autoLoginUser` passed,
   and the agent's launchd enable state was not checked. Fixed: pass only when it names the
   current user, warn for another user; tests for both. The enable-state gate is declined: install
   always runs `launchctl enable` for the label, `sed -n 203-213p cmd/axis/daemon_service.go`
   (recorded as a non-goal).
2. Minor: any `defaults` error was reported as "no automatic login". Fixed: only the "does not
   exist" output means that; other errors warn "could not read"; tests for each.
3. Minor: no doc updates named. Fixed: Design lists `docs/current-state.md` and
   `docs/daemon-service.md`; both updated in this PR.
4. Minor: two citations did not support their claims. Fixed: plist path cited at :148-149; the
   system-unit non-goal now gives the real reason.
5. Minor: tests did not cover never-fail install, unmarked file, odd loginctl output, timeout, and
   the macOS cases. Fixed: each is in the test list and in `daemon_boot_start_test.go`.

Questions:
- Does auto-login work with FileVault on or without `/etc/kcpassword`? Open: needs a Mac
  reboot drill; the pass message says the agent starts "when it logs in at boot", not that it
  will.
- Is `com.apple.loginwindow.plist` readable without admin? Open: no macOS host was probed; a
  permission error is reported as "could not read", not as a state.
- Should loginctl's "not logged in or lingering" error count as lingering off? Open: not probed
  on a systemd host without a session; it is reported as "could not read" with the error text.
- Which timeout? Became Design: 5 s per command, as `doctor_restart_loops.go:90`.
