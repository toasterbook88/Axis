# Daemon User Service

`axis daemon service install` installs and starts a supervised service for the
current user. It uses an absolute path to the currently running AXIS binary,
the private default Unix socket, and a one-minute refresh interval unless the
operator supplies `--addr` or `--refresh`.

| Platform | Service file | Manager |
| --- | --- | --- |
| macOS | `~/Library/LaunchAgents/com.toasterbook88.axis.daemon.plist` | `launchctl` GUI user domain |
| Linux | `~/.config/systemd/user/axis.service` | `systemctl --user` |

The generated service invokes `axis daemon start` directly without a shell and
applies an owner-only umask. The macOS service sets deterministic PATH/HOME
values and writes stdout/stderr beneath private `~/Library/Logs/Axis/`. The
Linux service writes to the user journal and includes `/usr/local/bin` plus the
NixOS system profile in PATH.

Install is idempotent for files carrying the `Managed by AXIS` marker and
refuses to overwrite an unrecognized service file. Uninstall has the same
ownership check, stops the job, removes its service file, and reloads the user
manager. It does not remove configuration, state, logs, tokens, or the AXIS
binary.

## Boot start versus login start

Both services belong to the user, so whether they start at boot depends on the
host. `axis daemon service install` and `axis doctor` report it (check
"Daemon boot start"). The check only reads state; it never changes it.

| Platform | Starts at boot when | Otherwise | Remedy (run it yourself) |
| --- | --- | --- | --- |
| Linux | lingering is on for the user (`loginctl show-user <uid> --property=Linger` is `yes`) | starts at the user's first login | `loginctl enable-linger $USER` |
| macOS | automatic login is set for this user (`autoLoginUser` in `/Library/Preferences/com.apple.loginwindow`) | starts when the user logs in to the GUI | enable automatic login for this user, or run the daemon as a LaunchDaemon (both need an administrator) |

On Linux the check runs only when `axis.service` is enabled. If it cannot read
the state, it warns "could not read" rather than guessing.

A daemon started by hand (`axis daemon start` from a shell, `nohup`, tmux) has
no service to restart it after a crash or start it after a reboot. On Linux,
`axis doctor` reads each serving daemon's systemd cgroup from
`/proc/<pid>/cgroup` (check "Daemon supervision"). It passes when the daemon
runs in its own service whose name contains `axis` (the user service or a
system unit), and warns when the daemon runs in a login session or another
scope, or inside an unrelated service such as `ssh.service`. It reports
nothing when the cgroup cannot be read or is hidden by a container's cgroup
namespace. To fix it, stop that daemon, then run `axis daemon service install`
(a running hand-started daemon would hold the address the service needs).

Install does not terminate a listener owned by an unknown process. If service
startup reports that the address is already in use, verify and stop that
process explicitly, then rerun install.

```bash
axis daemon service install
axis daemon service status
axis daemon status
axis daemon service uninstall
```

`axis daemon restart` directly replaces a listener process and is useful for
development or recovery. It does not install boot/login persistence. Prefer
the native service command for normal operation.
